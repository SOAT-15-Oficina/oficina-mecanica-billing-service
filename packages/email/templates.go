package email

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
)

//go:embed templates/*.html
var templatesFS embed.FS

var templates = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

type QuoteEmailItem struct {
	Title       string
	Amount      string
	Estimated   string
	ApproveLink string
	RejectLink  string
}

type QuoteEmailData struct {
	CustomerName   string
	WorkOrderCode  string
	Total          string
	QuoteLink      string
	Items          []QuoteEmailItem
	ApproveAllLink string
	RejectAllLink  string
	SupplyShortage bool
}

func RenderQuoteEmail(data QuoteEmailData) (string, error) {
	return render("quote.html", data)
}

type PaymentEmailData struct {
	CustomerName  string
	WorkOrderCode string
	Amount        string
	CheckoutURL   string
	ExpiresAt     string
}

func RenderPaymentEmail(data PaymentEmailData) (string, error) {
	return render("payment.html", data)
}

func render(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("email: render %s: %w", name, err)
	}
	return buf.String(), nil
}
