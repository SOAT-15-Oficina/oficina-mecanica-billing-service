package billing

import (
	"context"
	"errors"
	"log/slog"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging/pgoutbox"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/observability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) requestPayment(ctx context.Context, env messaging.Envelope) error {
	var cmd messaging.RequestPayment
	if err := env.DecodePayload(&cmd); err != nil {
		return err
	}
	if done, err := s.processed(ctx, env.MessageID); err != nil || done {
		return err
	}

	logger := s.log(env).With(slog.String(observability.KeyQuoteID, cmd.QuoteID.String()))
	q, err := quoteByID(ctx, s.db, cmd.QuoteID, false)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil || q.WorkOrderID != env.WorkOrderID || q.Status != QuoteApproved || q.ApprovedTotalCents <= 0 {
		logger.ErrorContext(ctx, "billing: request-payment for a quote that is not payable",
			observability.Event("billing.payment_not_payable"), slog.String("quote_status", q.Status))
		return s.respond(ctx, env, messaging.TypePaymentFailed, messaging.PaymentFailed{Reason: messaging.ReasonDeclined})
	}

	if existing := q.payment(PaymentPending, PaymentApproved); existing != nil && existing.ExpiresAt.After(s.now()) {
		return s.respond(ctx, env, messaging.TypePaymentRequested, paymentRequested(*existing))
	}

	now := s.now()
	expiresAt := now.Add(s.cfg.PaymentExpiration)
	paymentID := uuid.NewSHA1(env.MessageID, []byte("payment"))
	approved := q.approvedItems()
	items := make([]mercadopago.QRItem, 0, len(approved))
	for _, it := range approved {
		items = append(items, mercadopago.QRItem{Title: it.Title, PriceCents: it.PriceCents})
	}
	order, err := s.gateway.CreateQROrder(ctx, mercadopago.QROrder{
		ExternalReference: paymentID.String(),
		Description:       "Ordem de servico " + q.WorkOrderCode,
		ExternalPOSID:     s.cfg.POSExternalID,
		Expiration:        s.cfg.PaymentExpiration,
		Items:             items,
	}, env.MessageID.String())
	if err != nil {
		logger.ErrorContext(ctx, "billing: create qr order failed",
			observability.Integration(observability.IntegrationMercadoPago), observability.Err(err))
		return err
	}

	payment := Payment{
		ID:              paymentID,
		Status:          PaymentPending,
		AmountCents:     q.ApprovedTotalCents,
		ProviderOrderID: order.ID,
		QRCodeURL:       s.links.PaymentQR(paymentID),
		QRData:          order.TypeResponse.QRData,
		ExpiresAt:       expiresAt,
		CreatedAt:       now,
	}
	created := false
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		fresh, err := pgoutbox.MarkProcessed(ctx, tx, env)
		if err != nil || !fresh {
			return err
		}
		if err := insertPayment(ctx, tx, q.ID, payment); err != nil {
			return err
		}
		created = true
		return s.emit(ctx, tx, env, messaging.TypePaymentRequested, paymentRequested(payment))
	})
	if err != nil || !created {
		return err
	}

	logger = logger.With(slog.String(observability.KeyPaymentID, payment.ID.String()), slog.String("provider_order_id", order.ID))
	logger.InfoContext(ctx, "billing: payment requested", observability.Event("billing.payment_requested"))
	if err := s.notifier.PaymentReady(ctx, q, payment); err != nil {
		logger.ErrorContext(ctx, "billing: payment email failed", observability.Event("billing.email_failed"), observability.Err(err))
	}
	return nil
}

func paymentRequested(p Payment) messaging.PaymentRequested {
	return messaging.PaymentRequested{PaymentID: p.ID, CheckoutURL: p.QRCodeURL, AmountCents: p.AmountCents, ExpiresAt: p.ExpiresAt}
}

func (s *Service) refundPayment(ctx context.Context, env messaging.Envelope) error {
	var cmd messaging.RefundPayment
	if err := env.DecodePayload(&cmd); err != nil {
		return err
	}
	if done, err := s.processed(ctx, env.MessageID); err != nil || done {
		return err
	}

	logger := s.log(env)
	q, err := quoteByWorkOrder(ctx, s.db, env.WorkOrderID, false)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}

	approved := q.payment(PaymentApproved)
	if approved == nil {
		if refunded := q.payment(PaymentRefunded); refunded != nil {
			return s.respond(ctx, env, messaging.TypePaymentRefunded, paymentRefunded(*refunded))
		}
		logger.WarnContext(ctx, "billing: refund-payment without an approved payment",
			observability.Event("billing.refund_without_payment"), slog.String("reason", cmd.Reason))
		return s.respond(ctx, env, messaging.TypePaymentRefunded, messaging.PaymentRefunded{})
	}

	order, err := s.gateway.RefundOrder(ctx, approved.ProviderOrderID, env.MessageID.String())
	if err != nil {
		logger.ErrorContext(ctx, "billing: refund failed",
			observability.Integration(observability.IntegrationMercadoPago), observability.Err(err))
		return err
	}

	refundID := order.RefundID()
	refundedAt := s.now()
	approved.Status = PaymentRefunded
	approved.ProviderRefundID = &refundID
	approved.RefundedAt = &refundedAt

	err = s.inTx(ctx, func(tx pgx.Tx) error {
		fresh, err := pgoutbox.MarkProcessed(ctx, tx, env)
		if err != nil || !fresh {
			return err
		}
		if err := exec(ctx, tx, "refund payment",
			`UPDATE payments SET status = $2, provider_refund_id = $3, refunded_at = $4, failure_reason = $5, updated_at = $4
			 WHERE id = $1 AND status = $6`,
			approved.ID, PaymentRefunded, refundID, refundedAt, nullable(cmd.Reason), PaymentApproved); err != nil {
			return err
		}
		return s.emit(ctx, tx, env, messaging.TypePaymentRefunded, paymentRefunded(*approved))
	})
	if err == nil {
		logger.InfoContext(ctx, "billing: payment refunded", observability.Event("billing.payment_refunded"),
			slog.String(observability.KeyPaymentID, approved.ID.String()), slog.String("provider_refund_id", refundID))
	}
	return err
}

func paymentRefunded(p Payment) messaging.PaymentRefunded {
	out := messaging.PaymentRefunded{PaymentID: p.ID}
	if p.ProviderRefundID != nil {
		out.ProviderRefundID = *p.ProviderRefundID
	}
	if p.RefundedAt != nil {
		out.RefundedAt = *p.RefundedAt
	}
	return out
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
