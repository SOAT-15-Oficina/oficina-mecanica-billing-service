package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/auth"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/billing"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secret = "test-secret"

type stubBilling struct {
	err          error
	decision     string
	quoteID      uuid.UUID
	itemID       uuid.UUID
	correlation  string
	notification billing.Notification
	page         billing.PaymentPage
}

func (s *stubBilling) QuoteByWorkOrder(_ context.Context, id uuid.UUID) (billing.Quote, error) {
	return billing.Quote{WorkOrderID: id, CustomerEmail: "maria@example.com"}, s.err
}

func (s *stubBilling) PublicQuote(_ context.Context, id uuid.UUID) (billing.PublicQuote, error) {
	return billing.PublicQuote{ID: id, Status: billing.QuoteSent}, s.err
}

func (s *stubBilling) DecideItem(_ context.Context, quoteID, itemID uuid.UUID, decision, correlationID string) (billing.PublicQuote, error) {
	s.quoteID, s.itemID, s.decision, s.correlation = quoteID, itemID, decision, correlationID
	return billing.PublicQuote{ID: quoteID}, s.err
}

func (s *stubBilling) DecideAll(_ context.Context, quoteID uuid.UUID, decision, correlationID string) (billing.PublicQuote, error) {
	s.quoteID, s.decision, s.correlation = quoteID, decision, correlationID
	return billing.PublicQuote{ID: quoteID}, s.err
}

func (s *stubBilling) HandleWebhook(_ context.Context, n billing.Notification) error {
	s.notification = n
	return s.err
}

func (s *stubBilling) PaymentPage(_ context.Context, id uuid.UUID) (billing.PaymentPage, error) {
	if s.err != nil {
		return billing.PaymentPage{}, s.err
	}
	page := s.page
	page.Payment.ID = id
	return page, nil
}

func newBillingApp(svc *stubBilling) *fiber.App {
	app := fiber.New()
	Register(app, Deps{Billing: svc, JWTSecretKey: secret})
	return app
}

func request(t *testing.T, app *fiber.App, method, path string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body
}

func bearer(t *testing.T, role string) map[string]string {
	t.Helper()
	token, err := auth.GenerateToken("alice", role, secret)
	require.NoError(t, err)
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestQuoteByWorkOrderRequiresOperator(t *testing.T) {
	svc := &stubBilling{}
	app := newBillingApp(svc)
	id := uuid.New()
	path := Prefix + "/quotes/by-work-order/" + id.String()

	status, _ := request(t, app, http.MethodGet, path, nil)
	assert.Equal(t, fiber.StatusUnauthorized, status)

	status, _ = request(t, app, http.MethodGet, path, bearer(t, "customer"))
	assert.Equal(t, fiber.StatusForbidden, status)

	for _, role := range []string{"admin", "employee"} {
		status, body := request(t, app, http.MethodGet, path, bearer(t, role))
		assert.Equal(t, fiber.StatusOK, status)
		assert.Equal(t, id.String(), body["work_order_id"])
		assert.Equal(t, "maria@example.com", body["customer_email"])
	}

	status, _ = request(t, app, http.MethodGet, Prefix+"/quotes/by-work-order/abc", bearer(t, "admin"))
	assert.Equal(t, fiber.StatusBadRequest, status)

	svc.err = billing.ErrNotFound
	status, body := request(t, app, http.MethodGet, path, bearer(t, "admin"))
	assert.Equal(t, fiber.StatusNotFound, status)
	assert.Equal(t, "quote not found", body["error"])
}

func TestPublicQuote(t *testing.T) {
	svc := &stubBilling{}
	app := newBillingApp(svc)
	id := uuid.New()

	status, body := request(t, app, http.MethodGet, Prefix+"/public/quotes/"+id.String(), nil)
	assert.Equal(t, fiber.StatusOK, status)
	assert.Equal(t, id.String(), body["id"])
	assert.NotContains(t, body, "customer_email")

	status, _ = request(t, app, http.MethodGet, Prefix+"/public/quotes/abc", nil)
	assert.Equal(t, fiber.StatusBadRequest, status)

	svc.err = errors.New("db down")
	status, _ = request(t, app, http.MethodGet, Prefix+"/public/quotes/"+id.String(), nil)
	assert.Equal(t, fiber.StatusInternalServerError, status)
}

func TestPublicDecisions(t *testing.T) {
	quoteID, itemID := uuid.New(), uuid.New()
	base := Prefix + "/public/quotes/" + quoteID.String()

	cases := []struct {
		path, decision, message string
		item                    bool
	}{
		{base + "/items/" + itemID.String() + "/approve", billing.ItemApproved, "Serviço aprovado com sucesso", true},
		{base + "/items/" + itemID.String() + "/reject", billing.ItemRejected, "Serviço reprovado com sucesso", true},
		{base + "/approve-all", billing.ItemApproved, "Todos os serviços pendentes foram aprovados", false},
		{base + "/reject-all", billing.ItemRejected, "Todos os serviços pendentes foram reprovados", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			svc := &stubBilling{}
			app := newBillingApp(svc)

			status, body := request(t, app, http.MethodGet, c.path, map[string]string{"X-Request-Id": "req-9"})
			assert.Equal(t, fiber.StatusOK, status)
			assert.Equal(t, c.message, body["message"])
			assert.Equal(t, c.decision, svc.decision)
			assert.Equal(t, quoteID, svc.quoteID)
			assert.Equal(t, "req-9", svc.correlation)
			if c.item {
				assert.Equal(t, itemID, svc.itemID)
			}

			for err, want := range map[error]int{
				billing.ErrConflict:        fiber.StatusConflict,
				billing.ErrNotFound:        fiber.StatusNotFound,
				billing.ErrInvalidDecision: fiber.StatusBadRequest,
			} {
				svc.err = err
				status, _ := request(t, app, http.MethodGet, c.path, nil)
				assert.Equal(t, want, status)
			}
		})
	}

	app := newBillingApp(&stubBilling{})
	for _, path := range []string{
		Prefix + "/public/quotes/abc/items/" + itemID.String() + "/approve",
		base + "/items/abc/approve",
		Prefix + "/public/quotes/abc/approve-all",
	} {
		status, _ := request(t, app, http.MethodGet, path, nil)
		assert.Equal(t, fiber.StatusBadRequest, status, path)
	}
}

func TestMercadoPagoWebhook(t *testing.T) {
	svc := &stubBilling{}
	app := newBillingApp(svc)
	path := Prefix + "/webhooks/mercadopago?data.id=ORDTST01&type=order"
	headers := map[string]string{"x-signature": "ts=1,v1=ab", "x-request-id": "req-1"}

	status, body := request(t, app, http.MethodPost, path, headers)
	assert.Equal(t, fiber.StatusOK, status)
	assert.Equal(t, "ok", body["status"])
	assert.Equal(t, billing.Notification{DataID: "ORDTST01", Type: "order", Signature: "ts=1,v1=ab", RequestID: "req-1"}, svc.notification)

	svc.err = billing.ErrInvalidSignature
	status, _ = request(t, app, http.MethodPost, path, headers)
	assert.Equal(t, fiber.StatusUnauthorized, status)

	svc.err = errors.New("mercado pago down")
	status, _ = request(t, app, http.MethodPost, path, headers)
	assert.Equal(t, fiber.StatusInternalServerError, status)
}
