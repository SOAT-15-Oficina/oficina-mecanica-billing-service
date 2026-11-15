package billing

import (
	"context"
	"testing"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateQuoteStoresItemsAndRespondsQuoteSent(t *testing.T) {
	f := newFixture(t)
	env := f.command(messaging.TypeCreateQuote, messaging.CreateQuote{
		WorkOrderCode:  "OS-1",
		Customer:       messaging.Customer{Name: "Maria", Email: "maria@example.com"},
		Items:          f.items,
		SupplyShortage: true,
	})
	require.NoError(t, f.svc.Handle(context.Background(), env))

	q, err := f.svc.QuoteByWorkOrder(context.Background(), f.wo)
	require.NoError(t, err)
	assert.Equal(t, QuoteSent, q.Status)
	assert.Equal(t, 23050, q.TotalCents)
	assert.Equal(t, f.sagaID, q.SagaID)
	assert.True(t, q.SupplyShortage)
	require.Len(t, q.Items, 2)
	assert.Equal(t, ItemPending, q.Items[0].Decision)
	assert.Empty(t, q.Payments)

	var sent messaging.QuoteSent
	out := f.last(messaging.TypeQuoteSent, &sent)
	assert.Equal(t, q.ID, sent.QuoteID)
	assert.Equal(t, 23050, sent.TotalCents)
	require.NotNil(t, out.CausationID)
	assert.Equal(t, env.MessageID, *out.CausationID)
	assert.Equal(t, env.CorrelationID, out.CorrelationID)
	assert.Len(t, f.notifier.quotes, 1)

	require.NoError(t, f.svc.Handle(context.Background(), env))
	assert.Len(t, f.outbox(), 1)

	f.handle(messaging.TypeCreateQuote, messaging.CreateQuote{WorkOrderCode: "OS-1", Items: f.items})
	msgs := f.outbox()
	require.Len(t, msgs, 2)
	require.NoError(t, msgs[1].DecodePayload(&sent))
	assert.Equal(t, q.ID, sent.QuoteID)
	assert.Len(t, f.notifier.quotes, 1)
}

func TestCreateQuoteSurvivesEmailFailure(t *testing.T) {
	f := newFixture(t)
	f.notifier.err = errBoom
	f.createQuote()
	f.last(messaging.TypeQuoteSent, nil)
}

func TestHandleRejectsInvalidPayloads(t *testing.T) {
	f := newFixture(t)
	for _, messageType := range []string{
		messaging.TypeCreateQuote, messaging.TypeRequestPayment, messaging.TypeRefundPayment, messaging.TypeCancelQuote,
	} {
		env := f.command(messageType, nil)
		env.Payload = []byte(`"not an object"`)
		assert.Error(t, f.svc.Handle(context.Background(), env), messageType)
	}
}

func TestUnknownMessageIsMarkedProcessed(t *testing.T) {
	f := newFixture(t)
	env := f.handle(messaging.TypeQuoteSent, messaging.QuoteSent{})
	assert.True(t, f.isProcessed(env.MessageID))
	assert.Empty(t, f.outbox())
}

func TestCustomerApprovesItemByItem(t *testing.T) {
	f := newFixture(t)
	q := f.createQuote()
	ctx := context.Background()

	view, err := f.svc.DecideItem(ctx, q.ID, f.items[1].WorkOrderServiceID, ItemApproved, "req-http")
	require.NoError(t, err)
	assert.Equal(t, QuoteSent, view.Status)
	assert.Len(t, f.outbox(), 1)

	_, err = f.svc.DecideItem(ctx, q.ID, f.items[1].WorkOrderServiceID, ItemRejected, "req-http")
	assert.ErrorIs(t, err, ErrConflict)
	_, err = f.svc.DecideItem(ctx, q.ID, uuid.New(), ItemApproved, "req-http")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = f.svc.DecideItem(ctx, uuid.New(), f.items[0].WorkOrderServiceID, ItemApproved, "req-http")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = f.svc.DecideItem(ctx, q.ID, f.items[0].WorkOrderServiceID, "TALVEZ", "req-http")
	assert.ErrorIs(t, err, ErrInvalidDecision)

	view, err = f.svc.DecideItem(ctx, q.ID, f.items[0].WorkOrderServiceID, ItemRejected, "req-http")
	require.NoError(t, err)
	assert.Equal(t, QuoteApproved, view.Status)
	assert.Equal(t, 15050, view.ApprovedTotalCents)
	assert.NotNil(t, view.DecidedAt)

	var approved messaging.QuoteApproved
	out := f.last(messaging.TypeQuoteApproved, &approved)
	assert.Equal(t, q.ID, approved.QuoteID)
	assert.Equal(t, []uuid.UUID{f.items[1].WorkOrderServiceID}, approved.ApprovedItems)
	assert.Equal(t, []uuid.UUID{f.items[0].WorkOrderServiceID}, approved.RejectedItems)
	assert.Equal(t, 15050, approved.ApprovedTotalCents)
	assert.Equal(t, f.sagaID, out.SagaID)
	assert.Equal(t, f.wo, out.WorkOrderID)
	assert.Equal(t, "req-http", out.CorrelationID)
	assert.Nil(t, out.CausationID)

	_, err = f.svc.DecideAll(ctx, q.ID, ItemApproved, "")
	assert.ErrorIs(t, err, ErrConflict)
}

func TestCustomerApprovesAll(t *testing.T) {
	f := newFixture(t)
	q := f.createQuote()

	view, err := f.svc.DecideAll(context.Background(), q.ID, ItemApproved, "")
	require.NoError(t, err)
	assert.Equal(t, QuoteApproved, view.Status)
	assert.Equal(t, 23050, view.ApprovedTotalCents)

	var approved messaging.QuoteApproved
	f.last(messaging.TypeQuoteApproved, &approved)
	assert.Len(t, approved.ApprovedItems, 2)
	assert.Empty(t, approved.RejectedItems)
	assert.NotNil(t, approved.RejectedItems)
}

func TestCustomerRejectsAll(t *testing.T) {
	f := newFixture(t)
	q := f.createQuote()

	view, err := f.svc.DecideAll(context.Background(), q.ID, ItemRejected, "")
	require.NoError(t, err)
	assert.Equal(t, QuoteRejected, view.Status)
	assert.Zero(t, view.ApprovedTotalCents)

	var rejected messaging.QuoteRejected
	f.last(messaging.TypeQuoteRejected, &rejected)
	assert.Equal(t, q.ID, rejected.QuoteID)
	assert.Equal(t, messaging.ReasonCustomer, rejected.Reason)

	_, err = f.svc.DecideItem(context.Background(), q.ID, f.items[0].WorkOrderServiceID, ItemApproved, "")
	assert.ErrorIs(t, err, ErrConflict)
}

func TestCancelQuoteExpiresPendingPayment(t *testing.T) {
	f := newFixture(t)
	q, p := f.pendingPayment()

	env := f.handle(messaging.TypeCancelQuote, messaging.CancelQuote{Reason: messaging.ReasonExpired})
	require.Len(t, f.gateway.cancels, 1)
	assert.Equal(t, p.ProviderOrderID+"|"+uuid.NewSHA1(env.MessageID, []byte(p.ProviderOrderID)).String(), f.gateway.cancels[0])
	require.NoError(t, f.svc.Handle(context.Background(), env))
	assert.Len(t, f.gateway.cancels, 1)

	var canceled messaging.QuoteCanceled
	f.last(messaging.TypeQuoteCanceled, &canceled)
	require.NotNil(t, canceled.QuoteID)
	assert.Equal(t, q.ID, *canceled.QuoteID)

	q, err := f.svc.QuoteByWorkOrder(context.Background(), f.wo)
	require.NoError(t, err)
	assert.Equal(t, QuoteCanceled, q.Status)
	assert.Equal(t, PaymentExpired, q.Payments[0].Status)
	require.NotNil(t, q.Payments[0].FailureReason)
	assert.Equal(t, messaging.ReasonExpired, *q.Payments[0].FailureReason)

	_, err = f.svc.DecideAll(context.Background(), q.ID, ItemApproved, "")
	assert.ErrorIs(t, err, ErrConflict)
}

func TestCancelQuoteRetriesWhenOrderCannotBeCanceled(t *testing.T) {
	f := newFixture(t)
	f.pendingPayment()
	f.gateway.cancelErr = errBoom

	env := f.command(messaging.TypeCancelQuote, messaging.CancelQuote{})
	assert.ErrorIs(t, f.svc.Handle(context.Background(), env), errBoom)
	assert.False(t, f.isProcessed(env.MessageID))
	assert.Equal(t, PaymentPending, f.payment().Status)

	f.gateway.cancelErr = mercadopago.ErrNotFound
	require.NoError(t, f.svc.Handle(context.Background(), env))
	assert.Equal(t, PaymentExpired, f.payment().Status)
	require.NotNil(t, f.payment().FailureReason)
	assert.Equal(t, messaging.ReasonSagaCompensation, *f.payment().FailureReason)
}

func TestCancelQuoteSkipsSettledPayments(t *testing.T) {
	f := newFixture(t)
	_, p := f.pendingPayment()
	f.settle(p.ProviderOrderID, mercadopago.OrderProcessed)

	f.handle(messaging.TypeCancelQuote, messaging.CancelQuote{})
	assert.Empty(t, f.gateway.cancels)
	assert.Equal(t, PaymentApproved, f.payment().Status)
}

func TestCancelQuoteWithoutQuoteStillResponds(t *testing.T) {
	f := newFixture(t)
	env := f.handle(messaging.TypeCancelQuote, messaging.CancelQuote{})

	var canceled messaging.QuoteCanceled
	f.last(messaging.TypeQuoteCanceled, &canceled)
	assert.Nil(t, canceled.QuoteID)

	require.NoError(t, f.svc.Handle(context.Background(), env))
	assert.Len(t, f.outbox(), 1)
}

func TestViews(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.QuoteByWorkOrder(context.Background(), f.wo)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = f.svc.PublicQuote(context.Background(), uuid.New())
	assert.ErrorIs(t, err, ErrNotFound)

	q, p := f.pendingPayment()
	view, err := f.svc.PublicQuote(context.Background(), q.ID)
	require.NoError(t, err)
	assert.Equal(t, q.ID, view.ID)
	assert.Equal(t, "Maria", view.CustomerName)
	assert.Equal(t, p.QRCodeURL, view.PaymentURL)
	assert.Len(t, view.Items, 2)
	require.Len(t, view.Payments, 1)
	assert.Equal(t, PublicPayment{
		ID: p.ID, Status: PaymentPending, AmountCents: 15050, ProviderOrderID: p.ProviderOrderID,
		QRCodeURL: p.QRCodeURL, QRData: p.QRData, ExpiresAt: p.ExpiresAt,
	}, view.Payments[0])

	page, err := f.svc.PaymentPage(context.Background(), p.ID)
	require.NoError(t, err)
	assert.Equal(t, p, page.Payment)
	assert.Equal(t, q.WorkOrderCode, page.WorkOrderCode)
	assert.Equal(t, "Maria", page.CustomerName)

	_, err = f.svc.PaymentPage(context.Background(), uuid.New())
	assert.ErrorIs(t, err, ErrNotFound)

	f.settle(p.ProviderOrderID, mercadopago.OrderProcessed)
	view, err = f.svc.PublicQuote(context.Background(), q.ID)
	require.NoError(t, err)
	assert.Empty(t, view.PaymentURL)
	assert.Equal(t, PaymentApproved, view.Payments[0].Status)
}
