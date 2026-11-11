package email

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderQuoteEmail(t *testing.T) {
	body, err := RenderQuoteEmail(QuoteEmailData{
		CustomerName:  "Maria",
		WorkOrderCode: "OS-1",
		Total:         "R$ 150,00",
		QuoteLink:     "http://x/api/billing/public/quotes/q",
		Items: []QuoteEmailItem{{
			Title: "Troca de oleo", Amount: "R$ 150,00", Estimated: "30 min",
			ApproveLink: "http://x/approve", RejectLink: "http://x/reject",
		}},
		ApproveAllLink: "http://x/approve-all",
		RejectAllLink:  "http://x/reject-all",
		SupplyShortage: true,
	})
	require.NoError(t, err)
	for _, want := range []string{"Maria", "OS-1", "Troca de oleo", "http://x/approve", "http://x/reject", "http://x/approve-all", "http://x/reject-all", "R$ 150,00", "insumos"} {
		assert.Contains(t, body, want)
	}
}

func TestRenderPaymentEmail(t *testing.T) {
	body, err := RenderPaymentEmail(PaymentEmailData{
		CustomerName: "Maria", WorkOrderCode: "OS-1", Amount: "R$ 90,00",
		CheckoutURL: "http://mp/checkout/pref", ExpiresAt: "13/11/2026 10:00",
	})
	require.NoError(t, err)
	for _, want := range []string{"Maria", "OS-1", "R$ 90,00", "http://mp/checkout/pref", "13/11/2026 10:00"} {
		assert.Contains(t, body, want)
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	_, err := render("missing.html", nil)
	assert.Error(t, err)
}
