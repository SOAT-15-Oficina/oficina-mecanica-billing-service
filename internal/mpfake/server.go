package mpfake

import (
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
)

const idAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

type Options struct {
	WebhookURL    string
	Secret        string
	POSExternalID string
	Client        *http.Client
	Logger        *slog.Logger
}

type order struct {
	mercadopago.Order
	expiresAt time.Time
}

type Server struct {
	webhookURL    string
	secret        string
	posExternalID string
	client        *http.Client
	logger        *slog.Logger
	now           func() time.Time

	mu        sync.Mutex
	orders    map[string]*order
	byKey     map[string]string
	stores    []mercadopago.Store
	pos       []mercadopago.POS
	posByKey  map[string]mercadopago.POS
	nextIDNum int64
}

func New(opts Options) *Server {
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.POSExternalID == "" {
		opts.POSExternalID = mercadopago.DefaultPOSExternalID
	}
	return &Server{
		webhookURL:    opts.WebhookURL,
		secret:        opts.Secret,
		posExternalID: opts.POSExternalID,
		client:        opts.Client,
		logger:        opts.Logger,
		now:           time.Now,
		orders:        map[string]*order{},
		byKey:         map[string]string{},
		posByKey:      map[string]mercadopago.POS{},
		nextIDNum:     80000000,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/orders", s.authorized(s.createOrder))
	mux.HandleFunc("GET /v1/orders/{id}", s.authorized(s.getOrder))
	mux.HandleFunc("POST /v1/orders/{id}/cancel", s.authorized(s.cancelOrder))
	mux.HandleFunc("POST /v1/orders/{id}/refund", s.authorized(s.refundOrder))
	mux.HandleFunc("GET /__orders/{id}", s.orderPage)
	mux.HandleFunc("POST /__orders/{id}/pay", s.pay)
	mux.HandleFunc("POST /__orders/{id}/expire", s.expire)
	mux.HandleFunc("GET /users/me", s.authorized(s.me))
	mux.HandleFunc("GET /users/{userID}/stores/search", s.authorized(s.searchStores))
	mux.HandleFunc("POST /users/{userID}/stores", s.authorized(s.createStore))
	mux.HandleFunc("GET /pos", s.authorized(s.searchPOS))
	mux.HandleFunc("POST /v2/pos", s.authorized(s.createPOS))
	return mux
}

func (s *Server) authorized(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		next(w, r)
	}
}

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-Idempotency-Key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "X-Idempotency-Key is required")
		return
	}
	var req mercadopago.OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid body")
		return
	}
	expiration, problem := s.validate(req)
	if problem != "" {
		writeError(w, http.StatusBadRequest, "invalid_order", problem)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byKey[key]; ok {
		writeJSON(w, http.StatusCreated, s.orders[id].Order)
		return
	}
	o := &order{expiresAt: s.now().Add(expiration), Order: mercadopago.Order{
		ID:                "ORDTST" + randomID(26),
		Type:              req.Type,
		Status:            mercadopago.OrderCreated,
		StatusDetail:      "created",
		ExternalReference: req.ExternalReference,
		Description:       req.Description,
		TotalAmount:       req.TotalAmount,
		ExpirationTime:    req.ExpirationTime,
		Config:            req.Config,
		Items:             req.Items,
		Transactions: mercadopago.Transactions{Payments: []mercadopago.OrderPayment{{
			ID: "PAY" + randomID(26), Amount: req.TotalAmount, Status: "created", StatusDetail: "ready_to_process",
		}}},
	}}
	o.TypeResponse.QRData = EMV(o.ID, req.TotalAmount)
	s.orders[o.ID] = o
	s.byKey[key] = o.ID
	s.logger.Info("mpfake: order created", slog.String("order_id", o.ID), slog.String("external_reference", o.ExternalReference))
	writeJSON(w, http.StatusCreated, o.Order)
}

func (s *Server) validate(req mercadopago.OrderRequest) (time.Duration, string) {
	if req.Type != mercadopago.OrderTypeQR {
		return 0, "type must be qr"
	}
	if req.Config.QR.ExternalPOSID != s.posExternalID {
		return 0, "unknown external_pos_id"
	}
	if req.Config.QR.Mode != mercadopago.QRModeDynamic {
		return 0, "mode must be dynamic"
	}
	if req.ExternalReference == "" || len(req.ExternalReference) > 64 {
		return 0, "invalid external_reference"
	}
	if len(req.Items) == 0 || len(req.Transactions.Payments) != 1 || req.Transactions.Payments[0].Amount != req.TotalAmount {
		return 0, "items and a single payment matching total_amount are required"
	}
	if req.ExpirationTime == "" {
		return 15 * time.Minute, ""
	}
	d, err := mercadopago.ParseISODuration(req.ExpirationTime)
	if err != nil || d < 30*time.Second || d > 3600*time.Hour {
		return 0, "invalid expiration_time"
	}
	return d, ""
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[r.PathValue("id")]
	if !ok {
		writeError(w, http.StatusNotFound, "order_not_found", "order not found")
		return
	}
	writeJSON(w, http.StatusOK, o.Order)
}

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, mercadopago.OrderCreated, func(o *order) {
		o.Status, o.StatusDetail = mercadopago.OrderCanceled, "canceled"
		o.setPayment("canceled", "canceled_by_api")
	}, "order_not_cancelable")
}

func (s *Server) refundOrder(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	o, ok := s.orders[r.PathValue("id")]
	refunded := ok && o.Status == mercadopago.OrderRefunded
	s.mu.Unlock()
	if refunded {
		writeError(w, http.StatusConflict, "order_already_refunded", "order already refunded")
		return
	}
	s.transition(w, r, mercadopago.OrderProcessed, func(o *order) {
		o.Status, o.StatusDetail = mercadopago.OrderRefunded, "refunded"
		o.setPayment("refunded", "refunded")
		o.Transactions.Refunds = []mercadopago.OrderRefund{{ID: "REF" + randomID(26), Amount: o.TotalAmount, Status: "processed"}}
	}, "order_not_refundable")
}

func (s *Server) transition(w http.ResponseWriter, r *http.Request, from string, apply func(*order), conflict string) {
	if r.Header.Get("X-Idempotency-Key") == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "X-Idempotency-Key is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[r.PathValue("id")]
	if !ok {
		writeError(w, http.StatusNotFound, "order_not_found", "order not found")
		return
	}
	if o.Status != from {
		writeError(w, http.StatusConflict, conflict, "order is "+o.Status)
		return
	}
	apply(o)
	status := http.StatusOK
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/refund") {
		status = http.StatusCreated
	}
	writeJSON(w, status, o.Order)
}

func (o *order) setPayment(status, detail string) {
	for i := range o.Transactions.Payments {
		o.Transactions.Payments[i].Status = status
		o.Transactions.Payments[i].StatusDetail = detail
	}
}

func randomID(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = idAlphabet[rand.IntN(len(idAlphabet))]
	}
	return string(b)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"status": status, "message": message,
		"errors": []map[string]string{{"code": code, "message": message}},
	})
}
