package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type DB interface {
	querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

const quoteColumns = `id, work_order_id, saga_id, work_order_code, customer_name, customer_email, status,
	total_cents, approved_total_cents, has_supply_shortage, sent_at, decided_at, created_at, updated_at`

const paymentColumns = `id, status, amount_cents, preference_id, checkout_url, provider_payment_id,
	provider_refund_id, failure_reason, expires_at, confirmed_at, refunded_at, created_at`

func loadQuote(ctx context.Context, db querier, column string, value uuid.UUID, forUpdate bool) (Quote, error) {
	sql := `SELECT ` + quoteColumns + ` FROM quotes WHERE ` + column + ` = $1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}

	var q Quote
	err := db.QueryRow(ctx, sql, value).Scan(
		&q.ID, &q.WorkOrderID, &q.SagaID, &q.WorkOrderCode, &q.CustomerName, &q.CustomerEmail, &q.Status,
		&q.TotalCents, &q.ApprovedTotalCents, &q.SupplyShortage, &q.SentAt, &q.DecidedAt, &q.CreatedAt, &q.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Quote{}, ErrNotFound
	}
	if err != nil {
		return Quote{}, fmt.Errorf("load quote: %w", err)
	}

	if q.Items, err = loadItems(ctx, db, q.ID); err != nil {
		return Quote{}, err
	}
	if q.Payments, err = loadPayments(ctx, db, q.ID); err != nil {
		return Quote{}, err
	}
	if q.Items == nil {
		q.Items = []Item{}
	}
	if q.Payments == nil {
		q.Payments = []Payment{}
	}
	return q, nil
}

func quoteByID(ctx context.Context, db querier, id uuid.UUID, forUpdate bool) (Quote, error) {
	return loadQuote(ctx, db, "id", id, forUpdate)
}

func quoteByWorkOrder(ctx context.Context, db querier, workOrderID uuid.UUID, forUpdate bool) (Quote, error) {
	return loadQuote(ctx, db, "work_order_id", workOrderID, forUpdate)
}

func loadItems(ctx context.Context, db querier, quoteID uuid.UUID) ([]Item, error) {
	rows, err := db.Query(ctx,
		`SELECT id, work_order_service_id, title, price_cents, estimated_time_minutes, decision, decided_at
		 FROM quote_items WHERE quote_id = $1 ORDER BY title, id`, quoteID)
	if err != nil {
		return nil, fmt.Errorf("load quote items: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Item, error) {
		var it Item
		err := row.Scan(&it.ID, &it.WorkOrderServiceID, &it.Title, &it.PriceCents, &it.EstimatedTimeMinutes, &it.Decision, &it.DecidedAt)
		return it, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan quote items: %w", err)
	}
	return items, nil
}

func loadPayments(ctx context.Context, db querier, quoteID uuid.UUID) ([]Payment, error) {
	rows, err := db.Query(ctx,
		`SELECT `+paymentColumns+` FROM payments WHERE quote_id = $1 ORDER BY created_at DESC, id DESC`, quoteID)
	if err != nil {
		return nil, fmt.Errorf("load payments: %w", err)
	}
	payments, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Payment, error) {
		var p Payment
		err := row.Scan(&p.ID, &p.Status, &p.AmountCents, &p.PreferenceID, &p.CheckoutURL, &p.ProviderPaymentID,
			&p.ProviderRefundID, &p.FailureReason, &p.ExpiresAt, &p.ConfirmedAt, &p.RefundedAt, &p.CreatedAt)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan payments: %w", err)
	}
	return payments, nil
}

func insertQuote(ctx context.Context, db querier, q Quote) error {
	if _, err := db.Exec(ctx,
		`INSERT INTO quotes (id, work_order_id, saga_id, work_order_code, customer_name, customer_email, status,
		                     total_cents, has_supply_shortage, sent_at, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, $10)`,
		q.ID, q.WorkOrderID, q.SagaID, q.WorkOrderCode, q.CustomerName, q.CustomerEmail, q.Status,
		q.TotalCents, q.SupplyShortage, q.SentAt,
	); err != nil {
		return fmt.Errorf("insert quote: %w", err)
	}
	for _, it := range q.Items {
		if _, err := db.Exec(ctx,
			`INSERT INTO quote_items (id, quote_id, work_order_service_id, title, price_cents, estimated_time_minutes, decision)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			it.ID, q.ID, it.WorkOrderServiceID, it.Title, it.PriceCents, it.EstimatedTimeMinutes, it.Decision,
		); err != nil {
			return fmt.Errorf("insert quote item: %w", err)
		}
	}
	return nil
}

func insertPayment(ctx context.Context, db querier, quoteID uuid.UUID, p Payment) error {
	if _, err := db.Exec(ctx,
		`INSERT INTO payments (id, quote_id, status, amount_cents, preference_id, checkout_url, expires_at, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`,
		p.ID, quoteID, p.Status, p.AmountCents, p.PreferenceID, p.CheckoutURL, p.ExpiresAt, p.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert payment: %w", err)
	}
	return nil
}

func exec(ctx context.Context, db querier, what, sql string, args ...any) error {
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
