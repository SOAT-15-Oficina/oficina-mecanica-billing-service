package mpfake

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/google/uuid"
)

var orderPage = template.Must(template.New("order").Parse(`<!doctype html>
<html lang="pt-BR">
<head><meta charset="UTF-8"><title>Mercado Pago (fake) - {{.ID}}</title></head>
<body style="font-family: Arial, Helvetica, sans-serif; max-width: 520px; margin: 40px auto">
  <h1>Mercado Pago (fake)</h1>
  <p>Order <code>{{.ID}}</code></p>
  <p>Referência: {{.ExternalReference}}</p>
  <ul>{{range .Items}}<li>{{.Title}}: R$ {{.UnitPrice}}</li>{{end}}</ul>
  <p><strong>Total: R$ {{.TotalAmount}}</strong></p>
  <p>Status: <strong>{{.Status}}</strong> ({{.StatusDetail}})</p>
  {{if eq .Status "created"}}
  <form method="post" action="/__orders/{{.ID}}/pay" style="display:inline"><button type="submit">Pagar</button></form>
  <form method="post" action="/__orders/{{.ID}}/expire" style="display:inline"><button type="submit">Expirar</button></form>
  {{end}}
</body>
</html>`))

func (s *Server) orderPage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	o, ok := s.orders[r.PathValue("id")]
	var snapshot mercadopago.Order
	if ok {
		snapshot = o.Order
	}
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "order_not_found", "order not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = orderPage.Execute(w, snapshot)
}

func (s *Server) pay(w http.ResponseWriter, r *http.Request) {
	s.settle(w, r, "order.processed", func(o *order) {
		o.Status, o.StatusDetail = mercadopago.OrderProcessed, "accredited"
		o.setPayment("processed", "accredited")
	})
}

func (s *Server) expire(w http.ResponseWriter, r *http.Request) {
	s.settle(w, r, "order.expired", func(o *order) {
		o.Status, o.StatusDetail = mercadopago.OrderExpired, "expired"
		o.setPayment("expired", "expired")
	})
}

func (s *Server) settle(w http.ResponseWriter, r *http.Request, action string, apply func(*order)) {
	id := r.PathValue("id")
	s.mu.Lock()
	o, ok := s.orders[id]
	switch {
	case !ok:
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "order_not_found", "order not found")
		return
	case o.Status != mercadopago.OrderCreated:
		status := o.Status
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "invalid_status", "order is "+status)
		return
	case action == "order.processed" && s.now().After(o.expiresAt):
		s.mu.Unlock()
		writeError(w, http.StatusGone, "order_expired", "order expired")
		return
	}
	apply(o)
	status := o.Status
	s.mu.Unlock()

	notified := s.notify(r.Context(), id, action)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		http.Redirect(w, r, "/__orders/"+id, http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order_id": id, "status": status, "notified": notified})
}

func (s *Server) notify(ctx context.Context, orderID, action string) bool {
	if s.webhookURL == "" {
		return false
	}
	requestID := uuid.NewString()
	ts := strconv.FormatInt(s.now().UnixMilli(), 10)

	sep := "?"
	if strings.Contains(s.webhookURL, "?") {
		sep = "&"
	}
	target := s.webhookURL + sep + "data.id=" + orderID + "&type=order"
	body := fmt.Sprintf(`{"action":%q,"api_version":"v1","type":"order","live_mode":false,"data":{"id":%q}}`, action, orderID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		s.logger.Error("mpfake: invalid webhook url", slog.String("error", err.Error()))
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-request-id", requestID)
	req.Header.Set("x-signature", mercadopago.SignatureHeader(s.secret, strings.ToLower(orderID), requestID, ts))

	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Error("mpfake: webhook failed", slog.String("error", err.Error()))
		return false
	}
	defer resp.Body.Close()
	s.logger.Info("mpfake: webhook delivered", slog.Int("status", resp.StatusCode), slog.String("order_id", orderID), slog.String("action", action))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
