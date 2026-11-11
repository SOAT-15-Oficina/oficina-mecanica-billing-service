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

func TestWebhookConfirmsApprovedPayment(t *testing.T) {
	f := newFixture(t)
	q, p := f.pendingPayment()
	providerID := f.gateway.addPayment(mercadopago.StatusApproved, q.ID.String())

	require.NoError(t, f.webhook(providerID))

	var confirmed messaging.PaymentConfirmed
	out := f.last(messaging.TypePaymentConfirmed, &confirmed)
	assert.Equal(t, p.ID, confirmed.PaymentID)
	assert.Equal(t, providerID, confirmed.ProviderPaymentID)
	assert.Equal(t, 15050, confirmed.AmountCents)
	assert.Equal(t, f.sagaID, out.SagaID)

	q, err := f.svc.QuoteByWorkOrder(context.Background(), f.wo)
	require.NoError(t, err)
	assert.Equal(t, PaymentApproved, q.Payments[0].Status)
	require.NotNil(t, q.Payments[0].ProviderPaymentID)
	assert.Equal(t, providerID, *q.Payments[0].ProviderPaymentID)

	count := len(f.outbox())
	require.NoError(t, f.webhook(providerID))
	assert.Len(t, f.outbox(), count)

	other := f.gateway.addPayment(mercadopago.StatusApproved, q.ID.String())
	require.NoError(t, f.webhook(other))
	assert.Len(t, f.outbox(), count)
}

func TestWebhookDeclinesRejectedPayment(t *testing.T) {
	for _, status := range []string{mercadopago.StatusRejected, mercadopago.StatusCancelled} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			q, p := f.pendingPayment()

			require.NoError(t, f.webhook(f.gateway.addPayment(status, q.ID.String())))

			var failed messaging.PaymentFailed
			f.last(messaging.TypePaymentFailed, &failed)
			assert.Equal(t, p.ID, failed.PaymentID)
			assert.Equal(t, messaging.ReasonDeclined, failed.Reason)

			q, err := f.svc.QuoteByWorkOrder(context.Background(), f.wo)
			require.NoError(t, err)
			assert.Equal(t, PaymentDeclined, q.Payments[0].Status)
		})
	}
}

func TestWebhookIgnoresWhatItCannotUse(t *testing.T) {
	f := newFixture(t)
	q, _ := f.pendingPayment()
	count := len(f.outbox())

	err := f.svc.HandleWebhook(context.Background(), Notification{DataID: "1", Type: "payment", RequestID: "r", Signature: "ts=1,v1=00"})
	assert.ErrorIs(t, err, ErrInvalidSignature)

	sig := mercadopago.SignatureHeader(webhookSecret, "1", "r", "1")
	require.NoError(t, f.svc.HandleWebhook(context.Background(), Notification{DataID: "1", Type: "merchant_order", RequestID: "r", Signature: sig}))

	require.NoError(t, f.webhook(f.gateway.addPayment("in_process", q.ID.String())))
	require.NoError(t, f.webhook(f.gateway.addPayment(mercadopago.StatusApproved, "not-a-uuid")))
	require.NoError(t, f.webhook(f.gateway.addPayment(mercadopago.StatusApproved, uuid.NewString())))
	assert.Len(t, f.outbox(), count)

	f.gateway.paymentErr = errBoom
	assert.ErrorIs(t, f.webhook("123"), errBoom)
}
