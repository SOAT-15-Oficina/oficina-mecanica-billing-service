package billing

import (
	"context"
	"errors"
	"log/slog"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging/pgoutbox"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/observability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) createQuote(ctx context.Context, env messaging.Envelope) error {
	var cmd messaging.CreateQuote
	if err := env.DecodePayload(&cmd); err != nil {
		return err
	}

	var created *Quote
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		fresh, err := pgoutbox.MarkProcessed(ctx, tx, env)
		if err != nil || !fresh {
			return err
		}

		q, err := quoteByWorkOrder(ctx, tx, env.WorkOrderID, true)
		switch {
		case errors.Is(err, ErrNotFound):
			q = s.newQuote(env, cmd)
			if err := insertQuote(ctx, tx, q); err != nil {
				return err
			}
			created = &q
		case err != nil:
			return err
		}

		return s.emit(ctx, tx, env, messaging.TypeQuoteSent, messaging.QuoteSent{
			QuoteID: q.ID, TotalCents: q.TotalCents, SentAt: q.SentAt,
		})
	})
	if err != nil || created == nil {
		return err
	}

	logger := s.log(env).With(slog.String(observability.KeyQuoteID, created.ID.String()))
	logger.InfoContext(ctx, "billing: quote sent", observability.Event("billing.quote_sent"))
	if err := s.notifier.QuoteReady(ctx, *created); err != nil {
		logger.ErrorContext(ctx, "billing: quote email failed", observability.Event("billing.email_failed"), observability.Err(err))
	}
	return nil
}

func (s *Service) newQuote(env messaging.Envelope, cmd messaging.CreateQuote) Quote {
	now := s.now()
	q := Quote{
		ID:             newID(),
		WorkOrderID:    env.WorkOrderID,
		SagaID:         env.SagaID,
		WorkOrderCode:  cmd.WorkOrderCode,
		CustomerName:   cmd.Customer.Name,
		CustomerEmail:  cmd.Customer.Email,
		Status:         QuoteSent,
		SupplyShortage: cmd.SupplyShortage,
		SentAt:         now,
		CreatedAt:      now,
		UpdatedAt:      now,
		Items:          make([]Item, 0, len(cmd.Items)),
	}
	for _, it := range cmd.Items {
		q.TotalCents += it.PriceCents
		q.Items = append(q.Items, Item{
			ID:                   newID(),
			WorkOrderServiceID:   it.WorkOrderServiceID,
			Title:                it.Title,
			PriceCents:           it.PriceCents,
			EstimatedTimeMinutes: it.EstimatedTimeMinutes,
			Decision:             ItemPending,
		})
	}
	return q
}

func (s *Service) cancelQuote(ctx context.Context, env messaging.Envelope) error {
	var cmd messaging.CancelQuote
	if err := env.DecodePayload(&cmd); err != nil {
		return err
	}
	reason := cmd.Reason
	if reason == "" {
		reason = messaging.ReasonSagaCompensation
	}

	return s.inTx(ctx, func(tx pgx.Tx) error {
		fresh, err := pgoutbox.MarkProcessed(ctx, tx, env)
		if err != nil || !fresh {
			return err
		}

		var quoteID *uuid.UUID
		q, err := quoteByWorkOrder(ctx, tx, env.WorkOrderID, true)
		switch {
		case errors.Is(err, ErrNotFound):
			s.log(env).WarnContext(ctx, "billing: cancel-quote without quote", observability.Event("billing.cancel_without_quote"))
		case err != nil:
			return err
		default:
			now := s.now()
			if err := exec(ctx, tx, "cancel quote",
				`UPDATE quotes SET status = $2, updated_at = $3 WHERE id = $1`, q.ID, QuoteCanceled, now); err != nil {
				return err
			}
			if err := exec(ctx, tx, "expire pending payments",
				`UPDATE payments SET status = $2, failure_reason = $3, updated_at = $4 WHERE quote_id = $1 AND status = $5`,
				q.ID, PaymentExpired, reason, now, PaymentPending); err != nil {
				return err
			}
			quoteID = &q.ID
			s.log(env).InfoContext(ctx, "billing: quote canceled", observability.Event("billing.quote_canceled"),
				slog.String(observability.KeyQuoteID, q.ID.String()), slog.String("reason", reason))
		}

		return s.emit(ctx, tx, env, messaging.TypeQuoteCanceled, messaging.QuoteCanceled{QuoteID: quoteID})
	})
}
