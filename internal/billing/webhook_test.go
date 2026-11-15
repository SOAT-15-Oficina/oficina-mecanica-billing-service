package billing

import (
	"context"
	"testing"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookConfirmsProcessedOrder(t *testing.T) {
	f := newFixture(t)
	_, p := f.pendingPayment()

	f.settle(p.ProviderOrderID, mercadopago.OrderProcessed)

	var confirmed messaging.PaymentConfirmed
	out := f.last(messaging.TypePaymentConfirmed, &confirmed)
	assert.Equal(t, p.ID, confirmed.PaymentID)
	payID := f.gateway.paymentID(p.ProviderOrderID)
	assert.Regexp(t, "^PAY", payID)
	assert.Equal(t, payID, confirmed.ProviderPaymentID)
	assert.Equal(t, 15050, confirmed.AmountCents)
	assert.Equal(t, f.sagaID, out.SagaID)

	got := f.payment()
	assert.Equal(t, PaymentApproved, got.Status)
	require.NotNil(t, got.ProviderPaymentID)
	assert.Equal(t, payID, *got.ProviderPaymentID)
	assert.NotNil(t, got.ConfirmedAt)

	count := len(f.outbox())
	require.NoError(t, f.webhook(p.ProviderOrderID))
	f.settle(p.ProviderOrderID, mercadopago.OrderExpired)
	f.settle(p.ProviderOrderID, mercadopago.OrderCanceled)
	assert.Len(t, f.outbox(), count)
	assert.Equal(t, PaymentApproved, f.payment().Status)
}

func TestWebhookFailsExpiredAndCanceledOrders(t *testing.T) {
	cases := map[string]struct{ status, reason string }{
		mercadopago.OrderExpired:  {PaymentExpired, messaging.ReasonExpired},
		mercadopago.OrderCanceled: {PaymentDeclined, messaging.ReasonDeclined},
	}
	for orderStatus, want := range cases {
		t.Run(orderStatus, func(t *testing.T) {
			f := newFixture(t)
			_, p := f.pendingPayment()

			f.settle(p.ProviderOrderID, orderStatus)

			var failed messaging.PaymentFailed
			f.last(messaging.TypePaymentFailed, &failed)
			assert.Equal(t, p.ID, failed.PaymentID)
			assert.Equal(t, want.reason, failed.Reason)

			got := f.payment()
			assert.Equal(t, want.status, got.Status)
			require.NotNil(t, got.FailureReason)
			assert.Equal(t, want.reason, *got.FailureReason)

			count := len(f.outbox())
			require.NoError(t, f.webhook(p.ProviderOrderID))
			assert.Len(t, f.outbox(), count)
		})
	}
}

func TestWebhookCanceledByUsIsIgnored(t *testing.T) {
	f := newFixture(t)
	_, p := f.pendingPayment()
	f.handle(messaging.TypeCancelQuote, messaging.CancelQuote{Reason: messaging.ReasonExpired})
	count := len(f.outbox())

	f.settle(p.ProviderOrderID, mercadopago.OrderCanceled)
	assert.Len(t, f.outbox(), count)
	assert.Equal(t, PaymentExpired, f.payment().Status)
}

func TestWebhookRefundedOrderMarksPaymentWithoutEvent(t *testing.T) {
	f := newFixture(t)
	_, p := f.pendingPayment()
	f.settle(p.ProviderOrderID, mercadopago.OrderProcessed)
	count := len(f.outbox())

	f.settle(p.ProviderOrderID, mercadopago.OrderRefunded)
	assert.Len(t, f.outbox(), count)
	got := f.payment()
	assert.Equal(t, PaymentRefunded, got.Status)
	require.NotNil(t, got.ProviderRefundID)
	assert.Equal(t, "REF-WEBHOOK", *got.ProviderRefundID)
	assert.NotNil(t, got.RefundedAt)

	require.NoError(t, f.webhook(p.ProviderOrderID))
	assert.Len(t, f.outbox(), count)

	env := f.handle(messaging.TypeRefundPayment, messaging.RefundPayment{})
	assert.Empty(t, f.gateway.refunds)
	var refunded messaging.PaymentRefunded
	f.last(messaging.TypePaymentRefunded, &refunded)
	assert.Equal(t, p.ID, refunded.PaymentID)
	assert.Equal(t, "REF-WEBHOOK", refunded.ProviderRefundID)
	assert.True(t, f.isProcessed(env.MessageID))
}

func TestWebhookSignature(t *testing.T) {
	f := newFixture(t)
	_, p := f.pendingPayment()
	f.gateway.setStatus(p.ProviderOrderID, mercadopago.OrderProcessed)

	err := f.svc.HandleWebhook(context.Background(), Notification{
		DataID: p.ProviderOrderID, Type: NotificationTypeOrder, RequestID: "r", Signature: "ts=1,v1=00",
	})
	assert.ErrorIs(t, err, ErrInvalidSignature)

	err = f.svc.HandleWebhook(context.Background(), Notification{
		DataID: p.ProviderOrderID, Type: NotificationTypeOrder, RequestID: "r",
		Signature: mercadopago.SignatureHeader("outro-segredo", p.ProviderOrderID, "r", "1"),
	})
	assert.ErrorIs(t, err, ErrInvalidSignature)
	assert.Equal(t, PaymentPending, f.payment().Status)

	require.NoError(t, f.svc.HandleWebhook(context.Background(), Notification{
		DataID: p.ProviderOrderID, Type: NotificationTypeOrder,
		Signature: mercadopago.SignatureHeader(webhookSecret, p.ProviderOrderID, "", "1"),
	}))
	assert.Equal(t, PaymentApproved, f.payment().Status)
}

func TestWebhookIgnoresWhatItCannotUse(t *testing.T) {
	f := newFixture(t)
	_, p := f.pendingPayment()
	count := len(f.outbox())

	sig := mercadopago.SignatureHeader(webhookSecret, "1", "r", "1")
	require.NoError(t, f.svc.HandleWebhook(context.Background(), Notification{DataID: "1", Type: "payment", RequestID: "r", Signature: sig}))

	require.NoError(t, f.webhook(p.ProviderOrderID))

	other, err := f.gateway.CreateQROrder(context.Background(), mercadopago.QROrder{ExternalReference: "x"}, "k")
	require.NoError(t, err)
	f.settle(other.ID, mercadopago.OrderProcessed)

	f.gateway.setStatus(p.ProviderOrderID, "action_required")
	require.NoError(t, f.webhook(p.ProviderOrderID))
	assert.Len(t, f.outbox(), count)
	assert.Equal(t, PaymentPending, f.payment().Status)

	f.gateway.getErr = errBoom
	assert.ErrorIs(t, f.webhook(p.ProviderOrderID), errBoom)
}

func TestWebhookConfirmsPaymentProcessedWhileTheQuoteWasCanceled(t *testing.T) {
	f := newFixture(t)
	_, p := f.pendingPayment()
	f.gateway.setStatus(p.ProviderOrderID, mercadopago.OrderProcessed)
	f.handle(messaging.TypeCancelQuote, messaging.CancelQuote{Reason: messaging.ReasonExpired})
	require.Equal(t, PaymentExpired, f.payment().Status)

	require.NoError(t, f.webhook(p.ProviderOrderID))
	var confirmed messaging.PaymentConfirmed
	f.last(messaging.TypePaymentConfirmed, &confirmed)
	assert.Equal(t, p.ID, confirmed.PaymentID)
	assert.Equal(t, PaymentApproved, f.payment().Status)

	f.handle(messaging.TypeRefundPayment, messaging.RefundPayment{Reason: messaging.ReasonExpired})
	require.Len(t, f.gateway.refunds, 1)
	assert.Equal(t, PaymentRefunded, f.payment().Status)
	var refunded messaging.PaymentRefunded
	f.last(messaging.TypePaymentRefunded, &refunded)
	assert.Equal(t, p.ID, refunded.PaymentID)
}
