package billing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/observability"
	"github.com/jackc/pgx/v5"
)

const NotificationTypeOrder = "order"

type Notification struct {
	DataID    string
	Type      string
	Signature string
	RequestID string
}

type orderUpdate struct {
	tx     pgx.Tx
	pay    *Payment
	order  mercadopago.Order
	cause  messaging.Envelope
	now    time.Time
	logger *slog.Logger
}

func (s *Service) HandleWebhook(ctx context.Context, n Notification) error {
	if !mercadopago.VerifySignature(s.cfg.WebhookSecret, n.Signature, n.DataID, n.RequestID) {
		return ErrInvalidSignature
	}

	logger := s.logger.With(slog.String("provider_order_id", n.DataID), slog.String("notification_type", n.Type),
		observability.Integration(observability.IntegrationMercadoPago))
	if n.Type != NotificationTypeOrder {
		logger.InfoContext(ctx, "billing: webhook ignored", observability.Event("billing.webhook_ignored"))
		return nil
	}

	order, err := s.gateway.GetOrder(ctx, n.DataID)
	if err != nil {
		logger.ErrorContext(ctx, "billing: fetch order failed", observability.Err(err))
		return err
	}
	logger = logger.With(slog.String("provider_status", order.Status), slog.String("provider_status_detail", order.StatusDetail))

	switch order.Status {
	case mercadopago.OrderProcessed, mercadopago.OrderExpired, mercadopago.OrderCanceled, mercadopago.OrderRefunded:
	default:
		logger.InfoContext(ctx, "billing: webhook ignored", observability.Event("billing.webhook_ignored"))
		return nil
	}

	quoteID, err := paymentQuoteID(ctx, s.db, "provider_order_id", order.ID)
	if errors.Is(err, ErrNotFound) {
		logger.WarnContext(ctx, "billing: order for unknown payment", observability.Event("billing.webhook_ignored"))
		return nil
	}
	if err != nil {
		return err
	}

	return s.inTx(ctx, func(tx pgx.Tx) error {
		q, err := quoteByID(ctx, tx, quoteID, true)
		if err != nil {
			return err
		}
		p := q.paymentByOrder(order.ID)
		u := orderUpdate{
			tx: tx, pay: p, order: order, cause: sagaCause(q, n.RequestID), now: s.now(),
			logger: logger.With(slog.String(observability.KeyPaymentID, p.ID.String()), slog.String("payment_status", p.Status)),
		}
		switch order.Status {
		case mercadopago.OrderProcessed:
			return s.confirm(ctx, u)
		case mercadopago.OrderExpired:
			return s.fail(ctx, u, PaymentExpired, messaging.ReasonExpired)
		case mercadopago.OrderCanceled:
			return s.fail(ctx, u, PaymentDeclined, messaging.ReasonDeclined)
		default:
			return s.markRefunded(ctx, u)
		}
	})
}

func (s *Service) confirm(ctx context.Context, u orderUpdate) error {
	if u.pay.Status != PaymentPending {
		u.logger.InfoContext(ctx, "billing: order already settled", observability.Event("billing.webhook_duplicated"))
		return nil
	}
	providerPaymentID := u.order.PaymentID()
	if err := exec(ctx, u.tx, "confirm payment",
		`UPDATE payments SET status = $2, provider_payment_id = $3, confirmed_at = $4, updated_at = $4 WHERE id = $1`,
		u.pay.ID, PaymentApproved, nullable(providerPaymentID), u.now); err != nil {
		return err
	}
	u.logger.InfoContext(ctx, "billing: payment confirmed", observability.Event("billing.payment_confirmed"))
	return s.emit(ctx, u.tx, u.cause, messaging.TypePaymentConfirmed, messaging.PaymentConfirmed{
		PaymentID: u.pay.ID, ProviderPaymentID: providerPaymentID, AmountCents: u.pay.AmountCents, ConfirmedAt: u.now,
	})
}

func (s *Service) fail(ctx context.Context, u orderUpdate, status, reason string) error {
	if u.pay.Status != PaymentPending {
		u.logger.InfoContext(ctx, "billing: order already settled", observability.Event("billing.webhook_duplicated"))
		return nil
	}
	if err := exec(ctx, u.tx, "fail payment",
		`UPDATE payments SET status = $2, failure_reason = $3, updated_at = $4 WHERE id = $1`,
		u.pay.ID, status, reason, u.now); err != nil {
		return err
	}
	u.logger.InfoContext(ctx, "billing: payment failed", observability.Event("billing.payment_failed"), slog.String("reason", reason))
	return s.emit(ctx, u.tx, u.cause, messaging.TypePaymentFailed, messaging.PaymentFailed{PaymentID: u.pay.ID, Reason: reason})
}

func (s *Service) markRefunded(ctx context.Context, u orderUpdate) error {
	if u.pay.Status != PaymentApproved {
		u.logger.InfoContext(ctx, "billing: refund notification without approved payment", observability.Event("billing.webhook_duplicated"))
		return nil
	}
	if err := exec(ctx, u.tx, "mark payment refunded",
		`UPDATE payments SET status = $2, provider_refund_id = COALESCE(provider_refund_id, $3), refunded_at = COALESCE(refunded_at, $4), updated_at = $4
		 WHERE id = $1`,
		u.pay.ID, PaymentRefunded, u.order.RefundID(), u.now); err != nil {
		return err
	}
	u.logger.InfoContext(ctx, "billing: payment refunded by provider", observability.Event("billing.payment_refunded_by_provider"))
	return nil
}
