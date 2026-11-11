package billing

import (
	"context"
	"testing"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestPaymentCreatesPreferenceAndRespondsPaymentRequested(t *testing.T) {
	f := newFixture(t)
	q := f.approvedQuote()
	before := time.Now()

	env := f.handle(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: q.ID})

	require.Len(t, f.gateway.preferences, 1)
	pref := f.gateway.preferences[0]
	assert.Equal(t, q.ID.String(), pref.ExternalReference)
	assert.Equal(t, "http://billing:8080/billing/webhooks/mercadopago", pref.NotificationURL)
	assert.True(t, pref.Expires)
	require.Len(t, pref.Items, 1)
	assert.Equal(t, "Troca de oleo", pref.Items[0].Title)
	assert.Equal(t, 150.50, pref.Items[0].UnitPrice)
	expiration, err := time.Parse(mercadopago.DateLayout, pref.ExpirationDateTo)
	require.NoError(t, err)
	assert.WithinDuration(t, before.Add(48*time.Hour), expiration, time.Minute)

	var requested messaging.PaymentRequested
	f.last(messaging.TypePaymentRequested, &requested)
	assert.Equal(t, 15050, requested.AmountCents)
	assert.Contains(t, requested.CheckoutURL, "http://mp.local/checkout/")

	q, err = f.svc.QuoteByWorkOrder(context.Background(), f.wo)
	require.NoError(t, err)
	require.Len(t, q.Payments, 1)
	assert.Equal(t, PaymentPending, q.Payments[0].Status)
	assert.Equal(t, requested.PaymentID, q.Payments[0].ID)
	assert.Len(t, f.notifier.payments, 1)

	require.NoError(t, f.svc.Handle(context.Background(), env))
	assert.Len(t, f.gateway.preferences, 1)

	f.handle(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: q.ID})
	assert.Len(t, f.gateway.preferences, 1)
	var again messaging.PaymentRequested
	f.last(messaging.TypePaymentRequested, &again)
	assert.Equal(t, requested.PaymentID, again.PaymentID)
}

func TestRequestPaymentSurvivesEmailFailure(t *testing.T) {
	f := newFixture(t)
	q := f.approvedQuote()
	f.notifier.err = errBoom
	f.handle(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: q.ID})
	f.last(messaging.TypePaymentRequested, nil)
}

func TestRequestPaymentForQuoteThatIsNotPayable(t *testing.T) {
	f := newFixture(t)
	q := f.createQuote()

	f.handle(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: q.ID})
	var failed messaging.PaymentFailed
	f.last(messaging.TypePaymentFailed, &failed)
	assert.Equal(t, messaging.ReasonDeclined, failed.Reason)
	assert.Equal(t, uuid.Nil, failed.PaymentID)

	f.handle(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: uuid.New()})
	f.last(messaging.TypePaymentFailed, nil)
	assert.Empty(t, f.gateway.preferences)
}

func TestRequestPaymentGatewayFailureIsRetried(t *testing.T) {
	f := newFixture(t)
	q := f.approvedQuote()
	f.gateway.prefErr = errBoom

	env := f.command(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: q.ID})
	assert.ErrorIs(t, f.svc.Handle(context.Background(), env), errBoom)
	assert.False(t, f.isProcessed(env.MessageID))
}

func TestRefundPaymentRefundsApprovedPayment(t *testing.T) {
	f := newFixture(t)
	q, p := f.pendingPayment()
	providerID := f.gateway.addPayment(mercadopago.StatusApproved, q.ID.String())
	require.NoError(t, f.webhook(providerID))

	env := f.handle(messaging.TypeRefundPayment, messaging.RefundPayment{Reason: messaging.ReasonBayUnavailable})
	assert.Equal(t, []string{providerID + "|" + env.MessageID.String()}, f.gateway.refunds)

	var refunded messaging.PaymentRefunded
	f.last(messaging.TypePaymentRefunded, &refunded)
	assert.Equal(t, p.ID, refunded.PaymentID)
	assert.Equal(t, "777", refunded.ProviderRefundID)
	assert.False(t, refunded.RefundedAt.IsZero())

	q, err := f.svc.QuoteByWorkOrder(context.Background(), f.wo)
	require.NoError(t, err)
	assert.Equal(t, PaymentRefunded, q.Payments[0].Status)

	require.NoError(t, f.svc.Handle(context.Background(), env))
	f.handle(messaging.TypeRefundPayment, messaging.RefundPayment{})
	assert.Len(t, f.gateway.refunds, 1)
	var again messaging.PaymentRefunded
	f.last(messaging.TypePaymentRefunded, &again)
	assert.Equal(t, refunded.PaymentID, again.PaymentID)
	assert.Equal(t, "777", again.ProviderRefundID)
}

func TestRefundWithoutApprovedPaymentStillResponds(t *testing.T) {
	f := newFixture(t)
	f.handle(messaging.TypeRefundPayment, messaging.RefundPayment{Reason: messaging.ReasonBayUnavailable})

	var refunded messaging.PaymentRefunded
	f.last(messaging.TypePaymentRefunded, &refunded)
	assert.Equal(t, uuid.Nil, refunded.PaymentID)
	assert.Empty(t, refunded.ProviderRefundID)
	assert.Empty(t, f.gateway.refunds)
}

func TestRefundGatewayFailureIsRetried(t *testing.T) {
	f := newFixture(t)
	q, _ := f.pendingPayment()
	require.NoError(t, f.webhook(f.gateway.addPayment(mercadopago.StatusApproved, q.ID.String())))
	f.gateway.refundErr = errBoom

	env := f.command(messaging.TypeRefundPayment, messaging.RefundPayment{})
	assert.ErrorIs(t, f.svc.Handle(context.Background(), env), errBoom)
	assert.False(t, f.isProcessed(env.MessageID))
}
