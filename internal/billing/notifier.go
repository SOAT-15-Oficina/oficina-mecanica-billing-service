package billing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/packages/email"
	"github.com/google/uuid"
)

type Links struct {
	baseURL string
}

func NewLinks(baseURL string) Links { return Links{baseURL: strings.TrimRight(baseURL, "/")} }

func (l Links) Quote(quoteID uuid.UUID) string {
	return fmt.Sprintf("%s/billing/public/quotes/%s", l.baseURL, quoteID)
}

func (l Links) Item(quoteID, workOrderServiceID uuid.UUID, action string) string {
	return fmt.Sprintf("%s/items/%s/%s", l.Quote(quoteID), workOrderServiceID, action)
}

func (l Links) All(quoteID uuid.UUID, action string) string {
	return fmt.Sprintf("%s/%s-all", l.Quote(quoteID), action)
}

func (l Links) Page(quoteID uuid.UUID) string {
	return fmt.Sprintf("%s/aprovacao.html?quote=%s", strings.TrimSuffix(l.baseURL, "/api"), quoteID)
}

type EmailNotifier struct {
	provider email.Provider
	links    Links
	location *time.Location
}

func NewEmailNotifier(provider email.Provider, baseURL string) *EmailNotifier {
	location, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		location = time.FixedZone("BRT", -3*60*60)
	}
	return &EmailNotifier{provider: provider, links: NewLinks(baseURL), location: location}
}

func (n *EmailNotifier) QuoteReady(ctx context.Context, q Quote) error {
	data := email.QuoteEmailData{
		CustomerName:   q.CustomerName,
		WorkOrderCode:  q.WorkOrderCode,
		Total:          FormatBRL(q.TotalCents),
		QuoteLink:      n.links.Page(q.ID),
		ApproveAllLink: n.links.All(q.ID, "approve"),
		RejectAllLink:  n.links.All(q.ID, "reject"),
		SupplyShortage: q.SupplyShortage,
	}
	for _, it := range q.Items {
		data.Items = append(data.Items, email.QuoteEmailItem{
			Title:       it.Title,
			Amount:      FormatBRL(it.PriceCents),
			Estimated:   FormatMinutes(it.EstimatedTimeMinutes),
			ApproveLink: n.links.Item(q.ID, it.WorkOrderServiceID, "approve"),
			RejectLink:  n.links.Item(q.ID, it.WorkOrderServiceID, "reject"),
		})
	}
	body, err := email.RenderQuoteEmail(data)
	if err != nil {
		return err
	}
	return n.send(ctx, q.CustomerEmail, "Orçamento da ordem de serviço "+q.WorkOrderCode, body)
}

func (n *EmailNotifier) PaymentReady(ctx context.Context, q Quote, p Payment) error {
	body, err := email.RenderPaymentEmail(email.PaymentEmailData{
		CustomerName:  q.CustomerName,
		WorkOrderCode: q.WorkOrderCode,
		Amount:        FormatBRL(p.AmountCents),
		CheckoutURL:   p.CheckoutURL,
		ExpiresAt:     p.ExpiresAt.In(n.location).Format("02/01/2006 15:04"),
	})
	if err != nil {
		return err
	}
	return n.send(ctx, q.CustomerEmail, "Pagamento da ordem de serviço "+q.WorkOrderCode, body)
}

func (n *EmailNotifier) send(ctx context.Context, to, subject, body string) error {
	if n == nil || n.provider == nil {
		return nil
	}
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("billing: customer has no e-mail")
	}
	return n.provider.Send(ctx, email.Message{To: []string{to}, Subject: subject, Body: body, HTML: true})
}

func FormatBRL(cents int) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	integer := fmt.Sprintf("%d", cents/100)
	var groups []string
	for len(integer) > 3 {
		groups = append([]string{integer[len(integer)-3:]}, groups...)
		integer = integer[:len(integer)-3]
	}
	groups = append([]string{integer}, groups...)
	return fmt.Sprintf("%sR$ %s,%02d", sign, strings.Join(groups, "."), cents%100)
}

func FormatMinutes(minutes int) string {
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dh%02d", minutes/60, minutes%60)
}
