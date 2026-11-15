package billing

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/config"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/database"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const (
	topic         = "arn:aws:sns:sa-east-1:000000000000:billing-events.fifo"
	webhookSecret = "local-webhook-secret"
)

type fakeGateway struct {
	mu        sync.Mutex
	created   []mercadopago.QROrder
	keys      []string
	orders    map[string]mercadopago.Order
	cancels   []string
	refunds   []string
	createErr error
	getErr    error
	cancelErr error
	refundErr error
}

func (g *fakeGateway) CreateQROrder(_ context.Context, req mercadopago.QROrder, key string) (mercadopago.Order, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.createErr != nil {
		return mercadopago.Order{}, g.createErr
	}
	g.created = append(g.created, req)
	g.keys = append(g.keys, key)
	if g.orders == nil {
		g.orders = map[string]mercadopago.Order{}
	}
	o := mercadopago.Order{
		ID: "ORDTST" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:26], Status: mercadopago.OrderCreated,
		ExternalReference: req.ExternalReference,
		Transactions:      mercadopago.Transactions{Payments: []mercadopago.OrderPayment{{ID: "PAY" + strings.ReplaceAll(uuid.NewString(), "-", "")}}},
		TypeResponse:      mercadopago.TypeResponse{QRData: "000201-qr-" + req.ExternalReference},
	}
	g.orders[o.ID] = o
	return o, nil
}

func (g *fakeGateway) GetOrder(_ context.Context, id string) (mercadopago.Order, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.getErr != nil {
		return mercadopago.Order{}, g.getErr
	}
	o, ok := g.orders[id]
	if !ok {
		return mercadopago.Order{}, mercadopago.ErrNotFound
	}
	return o, nil
}

func (g *fakeGateway) CancelOrder(_ context.Context, id, key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancelErr != nil {
		return g.cancelErr
	}
	g.cancels = append(g.cancels, id+"|"+key)
	return nil
}

func (g *fakeGateway) RefundOrder(_ context.Context, id, key string) (mercadopago.Order, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refundErr != nil {
		return mercadopago.Order{}, g.refundErr
	}
	g.refunds = append(g.refunds, id+"|"+key)
	return mercadopago.Order{ID: id, Status: mercadopago.OrderProcessed,
		Transactions: mercadopago.Transactions{Refunds: []mercadopago.OrderRefund{{ID: "REF777", Status: "processing"}}}}, nil
}

func (g *fakeGateway) paymentID(orderID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.orders[orderID].PaymentID()
}

func (g *fakeGateway) setStatus(id, status string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	o := g.orders[id]
	o.Status = status
	if status == mercadopago.OrderRefunded {
		o.Transactions.Refunds = []mercadopago.OrderRefund{{ID: "REF-WEBHOOK"}}
	}
	g.orders[id] = o
}

type fakeNotifier struct {
	mu       sync.Mutex
	quotes   []Quote
	payments []Payment
	err      error
}

func (n *fakeNotifier) QuoteReady(_ context.Context, q Quote) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.quotes = append(n.quotes, q)
	return n.err
}

func (n *fakeNotifier) PaymentReady(_ context.Context, _ Quote, p Payment) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.payments = append(n.payments, p)
	return n.err
}

type fixture struct {
	t        *testing.T
	db       *pgxpool.Pool
	svc      *Service
	gateway  *fakeGateway
	notifier *fakeNotifier
	sagaID   uuid.UUID
	wo       uuid.UUID
	items    []messaging.QuoteItem
}

var errBoom = errors.New("boom")

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if os.Getenv("DATABASE_HOST") == "" {
		t.Skip("DATABASE_HOST nao definido")
	}
	db, err := database.NewConnection(&config.DatabaseConfig{
		User: os.Getenv("DATABASE_USER"), Password: os.Getenv("DATABASE_PASSWORD"),
		Host: os.Getenv("DATABASE_HOST"), Port: os.Getenv("DATABASE_PORT"), Name: os.Getenv("DATABASE_NAME"),
	})
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(db))
	t.Cleanup(db.Close)

	f := &fixture{
		t: t, db: db, gateway: &fakeGateway{}, notifier: &fakeNotifier{},
		sagaID: uuid.New(), wo: uuid.New(),
		items: []messaging.QuoteItem{
			{WorkOrderServiceID: uuid.New(), Title: "Alinhamento", PriceCents: 8000, EstimatedTimeMinutes: 40},
			{WorkOrderServiceID: uuid.New(), Title: "Troca de oleo", PriceCents: 15050, EstimatedTimeMinutes: 30},
		},
	}
	f.svc = NewService(db, f.gateway, f.notifier, Config{
		EventsTopic:       topic,
		BaseURL:           "http://localhost:8080/api",
		POSExternalID:     "OFICINAMATRIZPOS01",
		PaymentExpiration: 48 * time.Hour,
		WebhookSecret:     webhookSecret,
	}, nil)
	return f
}

func (f *fixture) command(messageType string, payload any) messaging.Envelope {
	f.t.Helper()
	env, err := messaging.NewSagaStart(messageType, f.sagaID, f.wo, "req-"+messageType, payload)
	require.NoError(f.t, err)
	return env
}

func (f *fixture) handle(messageType string, payload any) messaging.Envelope {
	f.t.Helper()
	env := f.command(messageType, payload)
	require.NoError(f.t, f.svc.Handle(context.Background(), env))
	return env
}

func (f *fixture) createQuote() Quote {
	f.t.Helper()
	f.handle(messaging.TypeCreateQuote, messaging.CreateQuote{
		WorkOrderCode: "OS-" + f.wo.String()[:8],
		Customer:      messaging.Customer{Name: "Maria", Email: "maria@example.com"},
		Items:         f.items,
	})
	q, err := f.svc.QuoteByWorkOrder(context.Background(), f.wo)
	require.NoError(f.t, err)
	return q
}

func (f *fixture) approvedQuote() Quote {
	f.t.Helper()
	q := f.createQuote()
	_, err := f.svc.DecideItem(context.Background(), q.ID, f.items[1].WorkOrderServiceID, ItemApproved, "req-http")
	require.NoError(f.t, err)
	_, err = f.svc.DecideItem(context.Background(), q.ID, f.items[0].WorkOrderServiceID, ItemRejected, "req-http")
	require.NoError(f.t, err)
	q, err = f.svc.QuoteByWorkOrder(context.Background(), f.wo)
	require.NoError(f.t, err)
	return q
}

func (f *fixture) pendingPayment() (Quote, Payment) {
	f.t.Helper()
	q := f.approvedQuote()
	f.handle(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: q.ID})
	q, err := f.svc.QuoteByWorkOrder(context.Background(), f.wo)
	require.NoError(f.t, err)
	require.Len(f.t, q.Payments, 1)
	return q, q.Payments[0]
}

func (f *fixture) webhook(orderID string) error {
	requestID := uuid.NewString()
	return f.svc.HandleWebhook(context.Background(), Notification{
		DataID: orderID, Type: NotificationTypeOrder, RequestID: requestID,
		Signature: mercadopago.SignatureHeader(webhookSecret, strings.ToLower(orderID), requestID, "1700000000"),
	})
}

func (f *fixture) settle(orderID, status string) {
	f.t.Helper()
	f.gateway.setStatus(orderID, status)
	require.NoError(f.t, f.webhook(orderID))
}

func (f *fixture) payment() Payment {
	f.t.Helper()
	q, err := f.svc.QuoteByWorkOrder(context.Background(), f.wo)
	require.NoError(f.t, err)
	require.NotEmpty(f.t, q.Payments)
	return q.Payments[len(q.Payments)-1]
}

func (f *fixture) outbox() []messaging.Envelope {
	f.t.Helper()
	rows, err := f.db.Query(context.Background(),
		`SELECT destination, body FROM outbox WHERE group_id = $1 ORDER BY created_at, id`, f.wo.String())
	require.NoError(f.t, err)
	defer rows.Close()

	var out []messaging.Envelope
	for rows.Next() {
		var destination string
		var body []byte
		require.NoError(f.t, rows.Scan(&destination, &body))
		require.Equal(f.t, topic, destination)
		var env messaging.Envelope
		require.NoError(f.t, json.Unmarshal(body, &env))
		out = append(out, env)
	}
	require.NoError(f.t, rows.Err())
	return out
}

func (f *fixture) last(messageType string, payload any) messaging.Envelope {
	f.t.Helper()
	msgs := f.outbox()
	require.NotEmpty(f.t, msgs)
	env := msgs[len(msgs)-1]
	require.Equal(f.t, messageType, env.Type)
	if payload != nil {
		require.NoError(f.t, env.DecodePayload(payload))
	}
	return env
}

func (f *fixture) isProcessed(id uuid.UUID) bool {
	done, err := f.svc.processed(context.Background(), id)
	require.NoError(f.t, err)
	return done
}
