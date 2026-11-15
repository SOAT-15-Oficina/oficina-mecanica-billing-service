package mpfake

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secret = "local-webhook-secret"

type notification struct {
	dataID, kind, signature, requestID, action string
}

type receiver struct {
	mu   sync.Mutex
	got  []notification
	code int
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, notification{
		dataID:    req.URL.Query().Get("data.id"),
		kind:      req.URL.Query().Get("type"),
		signature: req.Header.Get("x-signature"),
		requestID: req.Header.Get("x-request-id"),
		action:    body.Action,
	})
	if r.code != 0 {
		w.WriteHeader(r.code)
	}
}

type env struct {
	fake   *Server
	srv    *httptest.Server
	client *mercadopago.Client
	recv   *receiver
}

func setup(t *testing.T, webhookPath string) env {
	t.Helper()
	recv := &receiver{}
	hook := httptest.NewServer(recv)
	t.Cleanup(hook.Close)

	webhookURL := ""
	if webhookPath != "-" {
		webhookURL = hook.URL + webhookPath
	}
	fake := New(Options{WebhookURL: webhookURL, Secret: secret})
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return env{fake: fake, srv: srv, client: mercadopago.NewClient(srv.URL, "TEST-token", nil), recv: recv}
}

func (e env) order(t *testing.T, key string) mercadopago.Order {
	t.Helper()
	o, err := e.client.CreateQROrder(context.Background(), mercadopago.QROrder{
		ExternalReference: "pay-1",
		Description:       "OS-1",
		ExternalPOSID:     mercadopago.DefaultPOSExternalID,
		Expiration:        30 * time.Minute,
		Items:             []mercadopago.QRItem{{Title: "Troca de oleo", PriceCents: 10000}, {Title: "Filtro", PriceCents: 2550}},
	}, key)
	require.NoError(t, err)
	return o
}

func post(t *testing.T, target, contentType, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(target, contentType, strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func get(t *testing.T, target string) (int, string) {
	t.Helper()
	resp, err := http.Get(target)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestCreateOrderReturnsQRAndIsIdempotent(t *testing.T) {
	e := setup(t, "/hook")
	o := e.order(t, "msg-1")

	assert.True(t, strings.HasPrefix(o.ID, "ORDTST"))
	assert.Len(t, o.ID, 32)
	assert.Equal(t, mercadopago.OrderCreated, o.Status)
	assert.Equal(t, "125.50", o.TotalAmount)
	assert.True(t, strings.HasPrefix(o.PaymentID(), "PAY"))
	assert.True(t, strings.HasPrefix(o.TypeResponse.QRData, "000201010212"))
	assert.Contains(t, o.TypeResponse.QRData, "COM.MERCADOLIBRE")

	again := e.order(t, "msg-1")
	assert.Equal(t, o.ID, again.ID)
	assert.NotEqual(t, o.ID, e.order(t, "msg-2").ID)

	got, err := e.client.GetOrder(context.Background(), o.ID)
	require.NoError(t, err)
	assert.Equal(t, "pay-1", got.ExternalReference)
}

func TestPayNotifiesSignedWebhookAndCanBeRefunded(t *testing.T) {
	e := setup(t, "/billing/webhooks/mercadopago?source=mp")
	o := e.order(t, "msg-1")

	code, body := post(t, e.srv.URL+"/__orders/"+o.ID+"/pay", "", "")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"status":"processed"`)
	assert.Contains(t, body, `"notified":true`)

	require.Len(t, e.recv.got, 1)
	n := e.recv.got[0]
	assert.Equal(t, o.ID, n.dataID)
	assert.Equal(t, "order", n.kind)
	assert.Equal(t, "order.processed", n.action)
	assert.True(t, mercadopago.VerifySignature(secret, n.signature, n.dataID, n.requestID))
	ts := strings.TrimPrefix(strings.Split(n.signature, ",")[0], "ts=")
	assert.Equal(t, mercadopago.SignatureHeader(secret, strings.ToLower(o.ID), n.requestID, ts), n.signature)

	got, err := e.client.GetOrder(context.Background(), o.ID)
	require.NoError(t, err)
	assert.Equal(t, mercadopago.OrderProcessed, got.Status)
	assert.Equal(t, "accredited", got.Transactions.Payments[0].StatusDetail)

	assert.NoError(t, e.client.CancelOrder(context.Background(), o.ID, "k"))
	code, _ = post(t, e.srv.URL+"/__orders/"+o.ID+"/expire", "", "")
	assert.Equal(t, http.StatusConflict, code)

	refunded, err := e.client.RefundOrder(context.Background(), o.ID, "msg-3")
	require.NoError(t, err)
	assert.Equal(t, mercadopago.OrderRefunded, refunded.Status)
	assert.True(t, strings.HasPrefix(refunded.RefundID(), "REF"))

	again, err := e.client.RefundOrder(context.Background(), o.ID, "msg-4")
	require.NoError(t, err)
	assert.Equal(t, mercadopago.OrderRefunded, again.Status)
}

func TestExpireNotifiesAndCancelWorksOnlyOnCreated(t *testing.T) {
	e := setup(t, "/hook")
	o := e.order(t, "msg-1")

	code, body := post(t, e.srv.URL+"/__orders/"+o.ID+"/expire", "", "")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"status":"expired"`)
	require.Len(t, e.recv.got, 1)
	assert.Equal(t, "order.expired", e.recv.got[0].action)

	other := e.order(t, "msg-2")
	require.NoError(t, e.client.CancelOrder(context.Background(), other.ID, "k"))
	got, err := e.client.GetOrder(context.Background(), other.ID)
	require.NoError(t, err)
	assert.Equal(t, mercadopago.OrderCanceled, got.Status)
	assert.Equal(t, "canceled_by_api", got.Transactions.Payments[0].StatusDetail)

	_, err = e.client.RefundOrder(context.Background(), other.ID, "k")
	assert.ErrorContains(t, err, "returned 409")
}

func TestOrderPageAndFormButtons(t *testing.T) {
	e := setup(t, "/hook")
	o := e.order(t, "msg-1")

	code, page := get(t, e.srv.URL+"/__orders/"+o.ID)
	require.Equal(t, http.StatusOK, code)
	for _, want := range []string{o.ID, "Troca de oleo", "125.50", "/__orders/" + o.ID + "/pay", "/__orders/" + o.ID + "/expire"} {
		assert.Contains(t, page, want)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.PostForm(e.srv.URL+"/__orders/"+o.ID+"/pay", url.Values{})
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/__orders/"+o.ID, resp.Header.Get("Location"))

	_, page = get(t, e.srv.URL+"/__orders/"+o.ID)
	assert.Contains(t, page, "processed")
	assert.NotContains(t, page, "Expirar")

	code, _ = get(t, e.srv.URL+"/__orders/unknown")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestWebhookFailuresAreReported(t *testing.T) {
	for name, path := range map[string]string{"rejected": "/hook", "no url": "-"} {
		t.Run(name, func(t *testing.T) {
			e := setup(t, path)
			e.recv.code = http.StatusUnauthorized
			o := e.order(t, "msg-1")
			_, body := post(t, e.srv.URL+"/__orders/"+o.ID+"/pay", "", "")
			assert.Contains(t, body, `"notified":false`)
		})
	}

	for _, target := range []string{"http://127.0.0.1:1/hook", "http://bad host/hook"} {
		fake := New(Options{WebhookURL: target, Secret: secret})
		srv := httptest.NewServer(fake.Handler())
		e := env{fake: fake, srv: srv, client: mercadopago.NewClient(srv.URL, "t", nil)}
		o := e.order(t, "msg-1")
		_, body := post(t, srv.URL+"/__orders/"+o.ID+"/pay", "", "")
		assert.Contains(t, body, `"notified":false`)
		srv.Close()
	}
}

func TestOrderValidation(t *testing.T) {
	e := setup(t, "/hook")
	valid := mercadopago.QROrder{
		ExternalReference: "pay-1", ExternalPOSID: mercadopago.DefaultPOSExternalID, Expiration: time.Hour,
		Items: []mercadopago.QRItem{{Title: "x", PriceCents: 100}},
	}.Request()

	mutate := map[string]func(r *mercadopago.OrderRequest){
		"type must be qr":         func(r *mercadopago.OrderRequest) { r.Type = "point" },
		"unknown external_pos_id": func(r *mercadopago.OrderRequest) { r.Config.QR.ExternalPOSID = "OUTRO" },
		"mode must be dynamic":    func(r *mercadopago.OrderRequest) { r.Config.QR.Mode = "static" },
		"invalid external_ref":    func(r *mercadopago.OrderRequest) { r.ExternalReference = "" },
		"single payment":          func(r *mercadopago.OrderRequest) { r.TotalAmount = "9.99" },
		"invalid expiration_time": func(r *mercadopago.OrderRequest) { r.ExpirationTime = "PT10S" },
	}
	for name, fn := range mutate {
		t.Run(name, func(t *testing.T) {
			req := valid
			fn(&req)
			code, body := send(t, e.srv.URL+"/v1/orders", "k-"+name, req)
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Contains(t, body, "invalid_order")
		})
	}

	noExpiration := valid
	noExpiration.ExpirationTime = ""
	code, _ := send(t, e.srv.URL+"/v1/orders", "k-default", noExpiration)
	assert.Equal(t, http.StatusCreated, code)

	code, body := send(t, e.srv.URL+"/v1/orders", "", valid)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "idempotency_key_required")

	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/v1/orders", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("X-Idempotency-Key", "k")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, err = http.Post(e.srv.URL+"/v1/orders", "application/json", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestUnknownOrdersAndStateErrors(t *testing.T) {
	e := setup(t, "/hook")
	ctx := context.Background()

	_, err := e.client.GetOrder(ctx, "ORDX")
	assert.ErrorIs(t, err, mercadopago.ErrNotFound)
	assert.ErrorIs(t, e.client.CancelOrder(ctx, "ORDX", "k"), mercadopago.ErrNotFound)
	_, err = e.client.RefundOrder(ctx, "ORDX", "k")
	assert.ErrorIs(t, err, mercadopago.ErrNotFound)
	code, _ := post(t, e.srv.URL+"/__orders/ORDX/pay", "", "")
	assert.Equal(t, http.StatusNotFound, code)

	o := e.order(t, "msg-1")
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/v1/orders/"+o.ID+"/cancel", nil)
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	e.fake.now = func() time.Time { return time.Now().Add(time.Hour) }
	code, _ = post(t, e.srv.URL+"/__orders/"+o.ID+"/pay", "", "")
	assert.Equal(t, http.StatusGone, code)

	code, _ = get(t, e.srv.URL+"/__health")
	assert.Equal(t, http.StatusOK, code)
}

func TestEMVHasValidCRC(t *testing.T) {
	sample := "00020101021243650016COM.MERCADOLIBRE02013063649e57dc5-c855-4020-98ad-f814ff91dbb15204000053039865802BR5909Test Test6009SAO PAULO62070503***6304"
	assert.Equal(t, uint16(0x3630), crc16(sample))

	qr := EMV("ORDTST1", "10.00")
	assert.Contains(t, qr, "540510.00")
	body, crc := qr[:len(qr)-4], qr[len(qr)-4:]
	assert.Equal(t, fmt.Sprintf("%04X", crc16(body)), crc)
	assert.NotContains(t, EMV("ORDTST1", ""), "5405")
}

func send(t *testing.T, target, key string, body any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer t")
	if key != "" {
		req.Header.Set("X-Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}
