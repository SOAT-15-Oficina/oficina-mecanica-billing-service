package billing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestPaymentCreatesQROrderAndRespondsPaymentRequested(t *testing.T) {
	f := newFixture(t)
	q := f.approvedQuote()
	before := time.Now()

	env := f.handle(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: q.ID})

	require.Len(t, f.gateway.created, 1)
	order := f.gateway.created[0]
	assert.Equal(t, env.MessageID.String(), f.gateway.keys[0])
	assert.Equal(t, "OFICINAMATRIZPOS01", order.ExternalPOSID)
	assert.Equal(t, 48*time.Hour, order.Expiration)
	assert.Equal(t, []mercadopago.QRItem{{Title: "Troca de oleo", PriceCents: 15050}}, order.Items)
	assert.Equal(t, "150.50", order.Request().TotalAmount)

	var requested messaging.PaymentRequested
	f.last(messaging.TypePaymentRequested, &requested)
	assert.Equal(t, 15050, requested.AmountCents)
	assert.Equal(t, requested.PaymentID.String(), order.ExternalReference)
	assert.Equal(t, "http://localhost:8080/api/billing/public/payments/"+requested.PaymentID.String()+"/qr", requested.CheckoutURL)
	assert.WithinDuration(t, before.Add(48*time.Hour), requested.ExpiresAt, time.Minute)

	p := f.payment()
	assert.Equal(t, PaymentPending, p.Status)
	assert.Equal(t, requested.PaymentID, p.ID)
	assert.True(t, strings.HasPrefix(p.ProviderOrderID, "ORDTST"))
	assert.Equal(t, "000201-qr-"+p.ID.String(), p.QRData)
	assert.Equal(t, requested.CheckoutURL, p.QRCodeURL)
	assert.Nil(t, p.ProviderPaymentID)
	require.Len(t, f.notifier.payments, 1)
	assert.Equal(t, p.QRCodeURL, f.notifier.payments[0].QRCodeURL)

	require.NoError(t, f.svc.Handle(context.Background(), env))
	assert.Len(t, f.gateway.created, 1)

	f.handle(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: q.ID})
	assert.Len(t, f.gateway.created, 1)
	var again messaging.PaymentRequested
	f.last(messaging.TypePaymentRequested, &again)
	assert.Equal(t, requested.PaymentID, again.PaymentID)
}

func TestRequestPaymentRetryReusesPaymentID(t *testing.T) {
	f := newFixture(t)
	q := f.approvedQuote()
	env := f.command(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: q.ID})

	f.gateway.createErr = errBoom
	assert.ErrorIs(t, f.svc.Handle(context.Background(), env), errBoom)
	f.gateway.createErr = nil
	require.NoError(t, f.svc.Handle(context.Background(), env))
	require.NoError(t, f.svc.Handle(context.Background(), env))

	require.Len(t, f.gateway.created, 1)
	assert.Equal(t, f.payment().ID.String(), f.gateway.created[0].ExternalReference)
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
	assert.Empty(t, f.gateway.created)
}

func TestRequestPaymentGatewayFailureIsRetried(t *testing.T) {
	f := newFixture(t)
	q := f.approvedQuote()
	f.gateway.createErr = errBoom

	env := f.command(messaging.TypeRequestPayment, messaging.RequestPayment{QuoteID: q.ID})
	assert.ErrorIs(t, f.svc.Handle(context.Background(), env), errBoom)
	assert.False(t, f.isProcessed(env.MessageID))
}

func TestRefundPaymentRefundsApprovedPayment(t *testing.T) {
	f := newFixture(t)
	_, p := f.pendingPayment()
	f.settle(p.ProviderOrderID, mercadopago.OrderProcessed)

	env := f.handle(messaging.TypeRefundPayment, messaging.RefundPayment{Reason: messaging.ReasonBayUnavailable})
	assert.Equal(t, []string{p.ProviderOrderID + "|" + env.MessageID.String()}, f.gateway.refunds)

	var refunded messaging.PaymentRefunded
	f.last(messaging.TypePaymentRefunded, &refunded)
	assert.Equal(t, p.ID, refunded.PaymentID)
	assert.Equal(t, "REF777", refunded.ProviderRefundID)
	assert.False(t, refunded.RefundedAt.IsZero())
	assert.Equal(t, PaymentRefunded, f.payment().Status)

	require.NoError(t, f.svc.Handle(context.Background(), env))
	f.handle(messaging.TypeRefundPayment, messaging.RefundPayment{})
	assert.Len(t, f.gateway.refunds, 1)
	var again messaging.PaymentRefunded
	f.last(messaging.TypePaymentRefunded, &again)
	assert.Equal(t, refunded.PaymentID, again.PaymentID)
	assert.Equal(t, "REF777", again.ProviderRefundID)

	count := len(f.outbox())
	f.settle(p.ProviderOrderID, mercadopago.OrderRefunded)
	assert.Len(t, f.outbox(), count)
	assert.Equal(t, "REF777", *f.payment().ProviderRefundID)
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
	_, p := f.pendingPayment()
	f.settle(p.ProviderOrderID, mercadopago.OrderProcessed)
	f.gateway.refundErr = errBoom

	env := f.command(messaging.TypeRefundPayment, messaging.RefundPayment{})
	assert.ErrorIs(t, f.svc.Handle(context.Background(), env), errBoom)
	assert.False(t, f.isProcessed(env.MessageID))
}
