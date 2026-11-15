package billing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging/pgoutbox"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/observability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Gateway interface {
	CreateQROrder(ctx context.Context, order mercadopago.QROrder, idempotencyKey string) (mercadopago.Order, error)
	GetOrder(ctx context.Context, id string) (mercadopago.Order, error)
	CancelOrder(ctx context.Context, id, idempotencyKey string) error
	RefundOrder(ctx context.Context, id, idempotencyKey string) (mercadopago.Order, error)
}

type Notifier interface {
	QuoteReady(ctx context.Context, q Quote) error
	PaymentReady(ctx context.Context, q Quote, p Payment) error
}

type Config struct {
	EventsTopic       string
	BaseURL           string
	POSExternalID     string
	PaymentExpiration time.Duration
	WebhookSecret     string
}

type Service struct {
	db       DB
	gateway  Gateway
	notifier Notifier
	cfg      Config
	links    Links
	logger   *slog.Logger
	now      func() time.Time
}

func NewService(db DB, gateway Gateway, notifier Notifier, cfg Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		db:       db,
		gateway:  gateway,
		notifier: notifier,
		cfg:      cfg,
		links:    NewLinks(cfg.BaseURL),
		logger:   logger,
		now:      func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
	}
}

func (s *Service) Handle(ctx context.Context, env messaging.Envelope) error {
	switch env.Type {
	case messaging.TypeCreateQuote:
		return s.createQuote(ctx, env)
	case messaging.TypeRequestPayment:
		return s.requestPayment(ctx, env)
	case messaging.TypeRefundPayment:
		return s.refundPayment(ctx, env)
	case messaging.TypeCancelQuote:
		return s.cancelQuote(ctx, env)
	}

	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := pgoutbox.MarkProcessed(ctx, tx, env); err != nil {
			return err
		}
		s.log(env).WarnContext(ctx, "billing: unknown message type ignored", observability.Event("billing.message_ignored"))
		return nil
	})
}

func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (s *Service) processed(ctx context.Context, messageID uuid.UUID) (bool, error) {
	var exists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM processed_messages WHERE message_id = $1)`, messageID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check processed message: %w", err)
	}
	return exists, nil
}

func (s *Service) emit(ctx context.Context, tx pgx.Tx, cause messaging.Envelope, messageType string, payload any) error {
	env, err := messaging.New(messageType, cause, payload)
	if err != nil {
		return err
	}
	return pgoutbox.Enqueue(ctx, tx, s.cfg.EventsTopic, env)
}

func (s *Service) respond(ctx context.Context, env messaging.Envelope, messageType string, payload any) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		fresh, err := pgoutbox.MarkProcessed(ctx, tx, env)
		if err != nil || !fresh {
			return err
		}
		return s.emit(ctx, tx, env, messageType, payload)
	})
}

func (s *Service) log(env messaging.Envelope) *slog.Logger {
	return s.logger.With(
		slog.String(observability.KeyMessageID, env.MessageID.String()),
		slog.String(observability.KeyMessageType, env.Type),
		slog.String(observability.KeySagaID, env.SagaID.String()),
		slog.String(observability.KeyWorkOrderID, env.WorkOrderID.String()),
	)
}

func sagaCause(q Quote, correlationID string) messaging.Envelope {
	return messaging.Envelope{SagaID: q.SagaID, WorkOrderID: q.WorkOrderID, CorrelationID: correlationID}
}

func newID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}
