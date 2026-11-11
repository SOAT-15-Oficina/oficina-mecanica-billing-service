package mpfake

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
	dataID, kind, signature, requestID string
}

type receiver struct {
	mu   sync.Mutex
	got  []notification
	code int
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, notification{
		dataID:    req.URL.Query().Get("data.id"),
		kind:      req.URL.Query().Get("type"),
		signature: req.Header.Get("x-signature"),
		requestID: req.Header.Get("x-request-id"),
	})
	if r.code != 0 {
		w.WriteHeader(r.code)
	}
}

func setup(t *testing.T) (*Server, *httptest.Server, *mercadopago.Client, *receiver, *httptest.Server) {
	t.Helper()
	recv := &receiver{}
	hook := httptest.NewServer(recv)
	t.Cleanup(hook.Close)

	fake := New("http://mp.local/", secret, nil, nil)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return fake, srv, mercadopago.NewClient(srv.URL, "TEST-token", nil), recv, hook
}

func createPreference(t *testing.T, c *mercadopago.Client, notificationURL string, exp time.Time) mercadopago.Preference {
	t.Helper()
	pref, err := c.CreatePreference(context.Background(), mercadopago.PreferenceRequest{
		Items:             []mercadopago.Item{mercadopago.NewItem("i1", "Troca de oleo", 10000), mercadopago.NewItem("i2", "Filtro", 2550)},
		ExternalReference: "quote-1",
		NotificationURL:   notificationURL,
		Expires:           true,
		ExpirationDateTo:  mercadopago.ExpirationDate(exp),
	})
	require.NoError(t, err)
	return pref
}

func post(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestApprovedPaymentNotifiesSignedWebhookAndCanBeRefunded(t *testing.T) {
	_, srv, client, recv, hook := setup(t)
	pref := createPreference(t, client, hook.URL+"/billing/webhooks/mercadopago", time.Now().Add(time.Hour))
	assert.Equal(t, "http://mp.local/checkout/"+pref.ID, pref.InitPoint)
	assert.Equal(t, pref.InitPoint, pref.SandboxInitPoint)

	resp, err := http.Get(srv.URL + "/checkout/" + pref.ID)
	require.NoError(t, err)
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Contains(t, string(page), "Troca de oleo")
	assert.Contains(t, string(page), "125.50")
	assert.Contains(t, string(page), "status=approved")

	code, body := post(t, srv.URL+"/checkout/"+pref.ID+"/pay?status=approved")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"status":"approved"`)
	assert.Contains(t, body, `"notified":true`)

	require.Len(t, recv.got, 1)
	n := recv.got[0]
	assert.Equal(t, "payment", n.kind)
	assert.True(t, mercadopago.VerifySignature(secret, n.signature, n.dataID, n.requestID))

	payment, err := client.GetPayment(context.Background(), n.dataID)
	require.NoError(t, err)
	assert.Equal(t, mercadopago.StatusApproved, payment.Status)
	assert.Equal(t, "quote-1", payment.ExternalReference)
	assert.Equal(t, 125.5, payment.TransactionAmount)

	first, err := client.Refund(context.Background(), n.dataID, "msg-1")
	require.NoError(t, err)
	again, err := client.Refund(context.Background(), n.dataID, "msg-1")
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)

	payment, err = client.GetPayment(context.Background(), n.dataID)
	require.NoError(t, err)
	assert.Equal(t, mercadopago.StatusRefunded, payment.Status)

	_, err = client.Refund(context.Background(), n.dataID, "msg-2")
	assert.ErrorContains(t, err, "returned 400")
}

func TestRejectedPaymentAndQueryStringNotificationURL(t *testing.T) {
	_, srv, client, recv, hook := setup(t)
	pref := createPreference(t, client, hook.URL+"/hook?source=mp", time.Now().Add(time.Hour))

	code, body := post(t, srv.URL+"/checkout/"+pref.ID+"/pay?status=rejected")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"status":"rejected"`)
	require.Len(t, recv.got, 1)
	assert.NotEmpty(t, recv.got[0].dataID)

	payment, err := client.GetPayment(context.Background(), recv.got[0].dataID)
	require.NoError(t, err)
	assert.Equal(t, mercadopago.StatusRejected, payment.Status)
}

func TestNotificationFailuresAreReported(t *testing.T) {
	_, srv, client, recv, hook := setup(t)
	recv.code = http.StatusUnauthorized

	pref := createPreference(t, client, hook.URL, time.Now().Add(time.Hour))
	_, body := post(t, srv.URL+"/checkout/"+pref.ID+"/pay?status=approved")
	assert.Contains(t, body, `"notified":false`)

	pref = createPreference(t, client, "http://127.0.0.1:1/hook", time.Now().Add(time.Hour))
	_, body = post(t, srv.URL+"/checkout/"+pref.ID+"/pay?status=approved")
	assert.Contains(t, body, `"notified":false`)

	pref = createPreference(t, client, "http://bad host/hook", time.Now().Add(time.Hour))
	_, body = post(t, srv.URL+"/checkout/"+pref.ID+"/pay?status=approved")
	assert.Contains(t, body, `"notified":false`)

	pref = createPreference(t, client, "", time.Now().Add(time.Hour))
	_, body = post(t, srv.URL+"/checkout/"+pref.ID+"/pay?status=approved")
	assert.Contains(t, body, `"notified":false`)
}

func TestValidationErrors(t *testing.T) {
	fake, srv, client, _, hook := setup(t)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/checkout/preferences", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	for body, want := range map[string]string{
		`{`:            "invalid body",
		`{"items":[]}`: "items are required",
		`{"items":[{"title":"x","quantity":1,"unit_price":1}],"expiration_date_to":"amanha"}`: "invalid expiration_date_to",
	} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/checkout/preferences", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer t")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Contains(t, string(raw), want)
	}

	resp, err = http.Get(srv.URL + "/checkout/unknown")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	code, _ := post(t, srv.URL+"/checkout/unknown/pay?status=approved")
	assert.Equal(t, http.StatusNotFound, code)

	pref := createPreference(t, client, hook.URL, time.Now().Add(time.Hour))
	code, _ = post(t, srv.URL+"/checkout/"+pref.ID+"/pay?status=pending")
	assert.Equal(t, http.StatusBadRequest, code)

	fake.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	code, _ = post(t, srv.URL+"/checkout/"+pref.ID+"/pay?status=approved")
	assert.Equal(t, http.StatusGone, code)

	_, err = client.GetPayment(context.Background(), "abc")
	assert.ErrorIs(t, err, mercadopago.ErrNotFound)
	_, err = client.GetPayment(context.Background(), "42")
	assert.ErrorIs(t, err, mercadopago.ErrNotFound)
	_, err = client.Refund(context.Background(), "42", "k")
	assert.ErrorIs(t, err, mercadopago.ErrNotFound)

	resp, err = http.Get(srv.URL + "/__health")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
