package billing

import (
	"context"
	"testing"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/packages/email"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingProvider struct {
	sent []email.Message
	err  error
}

func (p *recordingProvider) Send(_ context.Context, msg email.Message) error {
	p.sent = append(p.sent, msg)
	return p.err
}

func sampleQuote() Quote {
	return Quote{
		ID: uuid.MustParse("0195a000-0000-7000-8000-000000000001"), WorkOrderCode: "OS-7", CustomerName: "Maria",
		CustomerEmail: "maria@example.com", TotalCents: 23050,
		Items: []Item{{WorkOrderServiceID: uuid.MustParse("0195a000-0000-7000-8000-000000000002"), Title: "Troca de oleo", PriceCents: 15050, EstimatedTimeMinutes: 90}},
	}
}

func TestEmailNotifierSendsQuoteWithPublicLinks(t *testing.T) {
	provider := &recordingProvider{}
	n := NewEmailNotifier(provider, "http://localhost:8080/api/")

	require.NoError(t, n.QuoteReady(context.Background(), sampleQuote()))
	require.Len(t, provider.sent, 1)
	msg := provider.sent[0]
	assert.Equal(t, []string{"maria@example.com"}, msg.To)
	assert.True(t, msg.HTML)
	assert.Contains(t, msg.Subject, "OS-7")
	base := "http://localhost:8080/api/billing/public/quotes/0195a000-0000-7000-8000-000000000001"
	for _, want := range []string{
		base + "/items/0195a000-0000-7000-8000-000000000002/approve",
		base + "/items/0195a000-0000-7000-8000-000000000002/reject",
		base + "/approve-all",
		base + "/reject-all",
		"http://localhost:8080/aprovacao.html?quote=0195a000-0000-7000-8000-000000000001",
		"R$ 230,50",
		"1h30",
	} {
		assert.Contains(t, msg.Body, want)
	}
}

func TestEmailNotifierSendsQRCodeLink(t *testing.T) {
	provider := &recordingProvider{}
	n := NewEmailNotifier(provider, "http://localhost:8080/api")
	paymentID := uuid.MustParse("0195a000-0000-7000-8000-000000000003")
	link := NewLinks("http://localhost:8080/api/").PaymentQR(paymentID)
	assert.Equal(t, "http://localhost:8080/api/billing/public/payments/0195a000-0000-7000-8000-000000000003/qr", link)
	p := Payment{ID: paymentID, AmountCents: 15050, QRCodeURL: link, ExpiresAt: time.Date(2026, 11, 13, 13, 0, 0, 0, time.UTC)}

	require.NoError(t, n.PaymentReady(context.Background(), sampleQuote(), p))
	require.Len(t, provider.sent, 1)
	assert.Contains(t, provider.sent[0].Body, link)
	assert.Contains(t, provider.sent[0].Body, "QR Code")
	assert.Contains(t, provider.sent[0].Body, "R$ 150,50")
	assert.Contains(t, provider.sent[0].Body, "13/11/2026 10:00")
}

func TestEmailNotifierErrors(t *testing.T) {
	provider := &recordingProvider{err: errBoom}
	n := NewEmailNotifier(provider, "http://x")
	assert.ErrorIs(t, n.QuoteReady(context.Background(), sampleQuote()), errBoom)

	q := sampleQuote()
	q.CustomerEmail = " "
	assert.ErrorContains(t, n.PaymentReady(context.Background(), q, Payment{}), "no e-mail")

	assert.NoError(t, NewEmailNotifier(nil, "http://x").QuoteReady(context.Background(), sampleQuote()))
	var nilNotifier *EmailNotifier
	assert.NoError(t, nilNotifier.send(context.Background(), "a@b.c", "s", "b"))
}

func TestFormatting(t *testing.T) {
	assert.Equal(t, "R$ 0,05", FormatBRL(5))
	assert.Equal(t, "R$ 150,50", FormatBRL(15050))
	assert.Equal(t, "R$ 1.234.567,89", FormatBRL(123456789))
	assert.Equal(t, "-R$ 10,00", FormatBRL(-1000))
	assert.Equal(t, "45 min", FormatMinutes(45))
	assert.Equal(t, "2h", FormatMinutes(120))
	assert.Equal(t, "1h05", FormatMinutes(65))
}
