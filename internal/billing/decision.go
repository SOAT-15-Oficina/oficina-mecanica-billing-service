package billing

import (
	"context"
	"log/slog"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/observability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) DecideItem(ctx context.Context, quoteID, workOrderServiceID uuid.UUID, decision, correlationID string) (PublicQuote, error) {
	return s.decide(ctx, quoteID, decision, correlationID, func(q Quote) ([]uuid.UUID, error) {
		for _, it := range q.Items {
			if it.WorkOrderServiceID != workOrderServiceID {
				continue
			}
			if it.Decision != ItemPending {
				return nil, ErrConflict
			}
			return []uuid.UUID{it.WorkOrderServiceID}, nil
		}
		return nil, ErrNotFound
	})
}

func (s *Service) DecideAll(ctx context.Context, quoteID uuid.UUID, decision, correlationID string) (PublicQuote, error) {
	return s.decide(ctx, quoteID, decision, correlationID, func(q Quote) ([]uuid.UUID, error) {
		pending := q.pendingItems()
		if len(pending) == 0 {
			return nil, ErrConflict
		}
		return pending, nil
	})
}

func (s *Service) decide(ctx context.Context, quoteID uuid.UUID, decision, correlationID string, pick func(Quote) ([]uuid.UUID, error)) (PublicQuote, error) {
	if decision != ItemApproved && decision != ItemRejected {
		return PublicQuote{}, ErrInvalidDecision
	}

	var result Quote
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		q, err := quoteByID(ctx, tx, quoteID, true)
		if err != nil {
			return err
		}
		if q.Status != QuoteSent {
			return ErrConflict
		}
		targets, err := pick(q)
		if err != nil {
			return err
		}

		now := s.now()
		if err := exec(ctx, tx, "decide quote items",
			`UPDATE quote_items SET decision = $3, decided_at = $4 WHERE quote_id = $1 AND work_order_service_id = ANY($2)`,
			q.ID, targets, decision, now); err != nil {
			return err
		}
		markDecided(&q, targets, decision, now)
		q.UpdatedAt = now

		if len(q.pendingItems()) > 0 {
			result = q
			return exec(ctx, tx, "touch quote", `UPDATE quotes SET updated_at = $2 WHERE id = $1`, q.ID, now)
		}
		if err := s.conclude(ctx, tx, &q, correlationID, now); err != nil {
			return err
		}
		result = q
		return nil
	})
	if err != nil {
		return PublicQuote{}, err
	}
	return result.Public(), nil
}

func markDecided(q *Quote, targets []uuid.UUID, decision string, at time.Time) {
	set := make(map[uuid.UUID]bool, len(targets))
	for _, id := range targets {
		set[id] = true
	}
	for i := range q.Items {
		if set[q.Items[i].WorkOrderServiceID] {
			q.Items[i].Decision = decision
			decidedAt := at
			q.Items[i].DecidedAt = &decidedAt
		}
	}
}

func (s *Service) conclude(ctx context.Context, tx pgx.Tx, q *Quote, correlationID string, now time.Time) error {
	approved := []uuid.UUID{}
	rejected := []uuid.UUID{}
	total := 0
	for _, it := range q.Items {
		if it.Decision == ItemApproved {
			approved = append(approved, it.WorkOrderServiceID)
			total += it.PriceCents
		} else {
			rejected = append(rejected, it.WorkOrderServiceID)
		}
	}

	q.Status = QuoteRejected
	if len(approved) > 0 {
		q.Status = QuoteApproved
	}
	q.ApprovedTotalCents = total
	q.DecidedAt = &now

	if err := exec(ctx, tx, "conclude quote",
		`UPDATE quotes SET status = $2, approved_total_cents = $3, decided_at = $4, updated_at = $4 WHERE id = $1`,
		q.ID, q.Status, total, now); err != nil {
		return err
	}

	cause := sagaCause(*q, correlationID)
	logger := s.logger.With(
		slog.String(observability.KeyQuoteID, q.ID.String()),
		slog.String(observability.KeySagaID, q.SagaID.String()),
		slog.String(observability.KeyWorkOrderID, q.WorkOrderID.String()),
	)
	if q.Status == QuoteApproved {
		logger.InfoContext(ctx, "billing: quote approved by the customer", observability.Event("billing.quote_approved"), slog.Int("approved_total_cents", total))
		return s.emit(ctx, tx, cause, messaging.TypeQuoteApproved, messaging.QuoteApproved{
			QuoteID: q.ID, ApprovedItems: approved, RejectedItems: rejected, ApprovedTotalCents: total,
		})
	}
	logger.InfoContext(ctx, "billing: quote rejected by the customer", observability.Event("billing.quote_rejected"))
	return s.emit(ctx, tx, cause, messaging.TypeQuoteRejected, messaging.QuoteRejected{QuoteID: q.ID, Reason: messaging.ReasonCustomer})
}
