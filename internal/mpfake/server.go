package mpfake

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/google/uuid"
)

type preference struct {
	request mercadopago.PreferenceRequest
	id      string
	expires time.Time
}

type payment struct {
	ID                int64   `json:"id"`
	Status            string  `json:"status"`
	StatusDetail      string  `json:"status_detail"`
	ExternalReference string  `json:"external_reference"`
	TransactionAmount float64 `json:"transaction_amount"`
	PreferenceID      string  `json:"preference_id"`
}

type refund struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

type Server struct {
	publicURL string
	secret    string
	client    *http.Client
	logger    *slog.Logger
	now       func() time.Time

	mu          sync.Mutex
	preferences map[string]*preference
	payments    map[int64]*payment
	refunds     map[string]refund
	nextID      int64
}

func New(publicURL, secret string, client *http.Client, logger *slog.Logger) *Server {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		publicURL:   strings.TrimRight(publicURL, "/"),
		secret:      secret,
		client:      client,
		logger:      logger,
		now:         time.Now,
		preferences: map[string]*preference{},
		payments:    map[int64]*payment{},
		refunds:     map[string]refund{},
		nextID:      1000000,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /checkout/preferences", s.createPreference)
	mux.HandleFunc("GET /checkout/{id}", s.checkoutPage)
	mux.HandleFunc("POST /checkout/{id}/pay", s.pay)
	mux.HandleFunc("GET /v1/payments/{id}", s.getPayment)
	mux.HandleFunc("POST /v1/payments/{id}/refunds", s.refund)
	return mux
}

func (s *Server) createPreference(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	var req mercadopago.PreferenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "items are required")
		return
	}

	pref := &preference{request: req, id: uuid.NewString()}
	if req.ExpirationDateTo != "" {
		exp, err := time.Parse(mercadopago.DateLayout, req.ExpirationDateTo)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid expiration_date_to")
			return
		}
		pref.expires = exp
	}

	s.mu.Lock()
	s.preferences[pref.id] = pref
	s.mu.Unlock()

	link := s.publicURL + "/checkout/" + pref.id
	writeJSON(w, http.StatusCreated, mercadopago.Preference{ID: pref.id, InitPoint: link, SandboxInitPoint: link})
}

var checkoutPage = template.Must(template.New("checkout").Parse(`<!doctype html>
<html lang="pt-BR">
<head><meta charset="UTF-8"><title>Mercado Pago (fake)</title></head>
<body style="font-family: Arial, Helvetica, sans-serif; max-width: 480px; margin: 40px auto">
  <h1>Mercado Pago (fake)</h1>
  <p>Pedido {{.Reference}}</p>
  <ul>{{range .Items}}<li>{{.Title}}: R$ {{printf "%.2f" .UnitPrice}}</li>{{end}}</ul>
  <p><strong>Total: R$ {{printf "%.2f" .Total}}</strong></p>
  <form method="post" action="/checkout/{{.ID}}/pay?status=approved" style="display:inline"><button type="submit">Pagar</button></form>
  <form method="post" action="/checkout/{{.ID}}/pay?status=rejected" style="display:inline"><button type="submit">Recusar</button></form>
</body>
</html>`))

func (s *Server) checkoutPage(w http.ResponseWriter, r *http.Request) {
	pref, ok := s.preference(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "preference not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = checkoutPage.Execute(w, map[string]any{
		"ID":        pref.id,
		"Reference": pref.request.ExternalReference,
		"Items":     pref.request.Items,
		"Total":     total(pref.request.Items),
	})
}

func (s *Server) pay(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != mercadopago.StatusApproved && status != mercadopago.StatusRejected {
		writeError(w, http.StatusBadRequest, "status must be approved or rejected")
		return
	}
	pref, ok := s.preference(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "preference not found")
		return
	}
	if !pref.expires.IsZero() && s.now().After(pref.expires) {
		writeError(w, http.StatusGone, "preference expired")
		return
	}

	s.mu.Lock()
	s.nextID++
	p := &payment{
		ID:                s.nextID,
		Status:            status,
		StatusDetail:      detail(status),
		ExternalReference: pref.request.ExternalReference,
		TransactionAmount: total(pref.request.Items),
		PreferenceID:      pref.id,
	}
	s.payments[p.ID] = p
	s.mu.Unlock()

	notified := s.notify(r, pref.request.NotificationURL, p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"payment_id": p.ID, "status": p.Status, "notified": notified})
}

func (s *Server) notify(r *http.Request, notificationURL string, paymentID int64) bool {
	if notificationURL == "" {
		return false
	}
	dataID := strconv.FormatInt(paymentID, 10)
	requestID := uuid.NewString()
	ts := strconv.FormatInt(s.now().UnixMilli(), 10)

	sep := "?"
	if strings.Contains(notificationURL, "?") {
		sep = "&"
	}
	target := notificationURL + sep + "data.id=" + dataID + "&type=payment"
	body := fmt.Sprintf(`{"action":"payment.updated","type":"payment","data":{"id":%q}}`, dataID)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		s.logger.Error("mpfake: invalid notification url", slog.String("error", err.Error()))
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-request-id", requestID)
	req.Header.Set("x-signature", mercadopago.SignatureHeader(s.secret, dataID, requestID, ts))

	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Error("mpfake: notification failed", slog.String("error", err.Error()))
		return false
	}
	defer resp.Body.Close()
	s.logger.Info("mpfake: notification delivered", slog.Int("status", resp.StatusCode), slog.String("payment_id", dataID))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (s *Server) getPayment(w http.ResponseWriter, r *http.Request) {
	p, ok := s.payment(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "payment not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) refund(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-Idempotency-Key")
	s.mu.Lock()
	defer s.mu.Unlock()

	if previous, ok := s.refunds[key]; ok && key != "" {
		writeJSON(w, http.StatusCreated, previous)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	p, ok := s.payments[id]
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "payment not found")
		return
	}
	if p.Status != mercadopago.StatusApproved {
		writeError(w, http.StatusBadRequest, "payment is not refundable")
		return
	}

	s.nextID++
	out := refund{ID: s.nextID, Status: mercadopago.StatusApproved}
	p.Status = mercadopago.StatusRefunded
	p.StatusDetail = "refunded"
	if key != "" {
		s.refunds[key] = out
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) preference(id string) (*preference, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pref, ok := s.preferences[id]
	return pref, ok
}

func (s *Server) payment(raw string) (payment, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return payment{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[id]
	if !ok {
		return payment{}, false
	}
	return *p, true
}

func total(items []mercadopago.Item) float64 {
	var sum float64
	for _, it := range items {
		sum += it.UnitPrice * float64(it.Quantity)
	}
	return sum
}

func detail(status string) string {
	if status == mercadopago.StatusApproved {
		return "accredited"
	}
	return "cc_rejected_other_reason"
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"status": status, "message": message})
}
