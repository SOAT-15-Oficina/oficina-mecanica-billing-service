package billing

import (
	"context"
	"errors"
	"log/slog"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/observability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Notification struct {
	DataID    string
	Type      string
	Signature string
	RequestID string
}

func (s *Service) HandleWebhook(ctx context.Context, n Notification) error {
	if !mercadopago.VerifySignature(s.cfg.WebhookSecret, n.Signature, n.DataID, n.RequestID) {
		return ErrInvalidSignature
	}

	logger := s.logger.With(slog.String("provider_payment_id", n.DataID), slog.String("notification_type", n.Type),
		observability.Integration(observability.IntegrationMercadoPago))
	if n.Type != "payment" {
		logger.InfoContext(ctx, "billing: webhook ignored", observability.Event("billing.webhook_ignored"))
		return nil
	}

	payment, err := s.gateway.GetPayment(ctx, n.DataID)
	if err != nil {
		logger.ErrorContext(ctx, "billing: fetch payment failed", observability.Err(err))
		return err
	}
	logger = logger.With(slog.String("provider_status", payment.Status), slog.String(observability.KeyQuoteID, payment.ExternalReference))

	switch payment.Status {
	case mercadopago.StatusApproved, mercadopago.StatusRejected, mercadopago.StatusCancelled:
	default:
		logger.InfoContext(ctx, "billing: webhook ignored", observability.Event("billing.webhook_ignored"))
		return nil
	}

	quoteID, err := uuid.Parse(payment.ExternalReference)
	if err != nil {
		logger.WarnContext(ctx, "billing: payment with unknown external_reference", observability.Event("billing.webhook_ignored"))
		return nil
	}

	return s.inTx(ctx, func(tx pgx.Tx) error {
		q, err := quoteByID(ctx, tx, quoteID, true)
		if errors.Is(err, ErrNotFound) {
			logger.WarnContext(ctx, "billing: payment for unknown quote", observability.Event("billing.webhook_ignored"))
			return nil
		}
		if err != nil {
			return err
		}
		if q.paymentByProviderID(n.DataID) != nil {
			logger.InfoContext(ctx, "billing: duplicated payment notification", observability.Event("billing.webhook_duplicated"))
			return nil
		}
		pending := q.payment(PaymentPending)
		if pending == nil {
			logger.WarnContext(ctx, "billing: payment notification without a pending payment", observability.Event("billing.payment_without_pending"))
			return nil
		}

		now := s.now()
		cause := sagaCause(q, n.RequestID)
		logger = logger.With(slog.String(observability.KeyPaymentID, pending.ID.String()))
		if payment.Status == mercadopago.StatusApproved {
			if err := exec(ctx, tx, "confirm payment",
				`UPDATE payments SET status = $2, provider_payment_id = $3, confirmed_at = $4, updated_at = $4 WHERE id = $1`,
				pending.ID, PaymentApproved, n.DataID, now); err != nil {
				return err
			}
			logger.InfoContext(ctx, "billing: payment confirmed", observability.Event("billing.payment_confirmed"))
			return s.emit(ctx, tx, cause, messaging.TypePaymentConfirmed, messaging.PaymentConfirmed{
				PaymentID: pending.ID, ProviderPaymentID: n.DataID, AmountCents: pending.AmountCents, ConfirmedAt: now,
			})
		}

		if err := exec(ctx, tx, "decline payment",
			`UPDATE payments SET status = $2, provider_payment_id = $3, failure_reason = $4, updated_at = $5 WHERE id = $1`,
			pending.ID, PaymentDeclined, n.DataID, messaging.ReasonDeclined, now); err != nil {
			return err
		}
		logger.InfoContext(ctx, "billing: payment declined", observability.Event("billing.payment_declined"))
		return s.emit(ctx, tx, cause, messaging.TypePaymentFailed, messaging.PaymentFailed{
			PaymentID: pending.ID, Reason: messaging.ReasonDeclined,
		})
	})
}
