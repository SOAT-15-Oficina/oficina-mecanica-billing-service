package mercadopago

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreatePreference(t *testing.T) {
	var got PreferenceRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/checkout/preferences", r.URL.Path)
		assert.Equal(t, "Bearer TEST-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = w.Write([]byte(`{"id":"pref-1","init_point":"https://mp/checkout/pref-1","sandbox_init_point":"https://sandbox/pref-1"}`))
	}))
	defer srv.Close()

	exp := time.Date(2026, 11, 13, 10, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	pref, err := NewClient(srv.URL+"/", "TEST-token", nil).CreatePreference(context.Background(), PreferenceRequest{
		Items:             []Item{NewItem("i1", "Troca de oleo", 15050)},
		ExternalReference: "quote-1",
		NotificationURL:   "http://billing/webhook",
		Expires:           true,
		ExpirationDateTo:  ExpirationDate(exp),
	})
	require.NoError(t, err)
	assert.Equal(t, "pref-1", pref.ID)
	assert.Equal(t, "https://mp/checkout/pref-1", pref.InitPoint)
	assert.Equal(t, "quote-1", got.ExternalReference)
	assert.Equal(t, "2026-11-13T10:00:00.000-03:00", got.ExpirationDateTo)
	require.Len(t, got.Items, 1)
	assert.Equal(t, 150.50, got.Items[0].UnitPrice)
	assert.Equal(t, 1, got.Items[0].Quantity)
	assert.Equal(t, "BRL", got.Items[0].CurrencyID)
}

func TestCreatePreferenceFallsBackToSandboxInitPoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"pref-1","sandbox_init_point":"https://sandbox/pref-1"}`))
	}))
	defer srv.Close()

	pref, err := NewClient(srv.URL, "t", srv.Client()).CreatePreference(context.Background(), PreferenceRequest{})
	require.NoError(t, err)
	assert.Equal(t, "https://sandbox/pref-1", pref.InitPoint)
}

func TestGetPayment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/payments/123", r.URL.Path)
		_, _ = w.Write([]byte(`{"id":123,"status":"approved","status_detail":"accredited","external_reference":"quote-1","transaction_amount":150.5}`))
	}))
	defer srv.Close()

	p, err := NewClient(srv.URL, "t", nil).GetPayment(context.Background(), "123")
	require.NoError(t, err)
	assert.Equal(t, "123", p.ID.String())
	assert.Equal(t, StatusApproved, p.Status)
	assert.Equal(t, "quote-1", p.ExternalReference)
	assert.Equal(t, 150.5, p.TransactionAmount)
}

func TestGetPaymentNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "t", nil).GetPayment(context.Background(), "9")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRefundSendsIdempotencyKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/payments/123/refunds", r.URL.Path)
		assert.Equal(t, "msg-1", r.Header.Get("X-Idempotency-Key"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":555,"status":"approved"}`))
	}))
	defer srv.Close()

	refund, err := NewClient(srv.URL, "t", nil).Refund(context.Background(), "123", "msg-1")
	require.NoError(t, err)
	assert.Equal(t, "555", refund.ID.String())
	assert.Equal(t, "approved", refund.Status)
}

func TestErrorsAreReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/payments/bad-json" {
			_, _ = w.Write([]byte(`{`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", nil)

	_, err := c.Refund(context.Background(), "1", "k")
	assert.ErrorContains(t, err, "returned 400")

	_, err = c.GetPayment(context.Background(), "bad-json")
	assert.ErrorContains(t, err, "decode response")

	_, err = NewClient("http://127.0.0.1:1", "t", nil).GetPayment(context.Background(), "1")
	assert.Error(t, err)

	_, err = NewClient("http://bad host", "t", nil).GetPayment(context.Background(), "1")
	assert.ErrorContains(t, err, "build request")

	_, err = c.CreatePreference(context.Background(), PreferenceRequest{Items: []Item{{UnitPrice: nanValue()}}})
	assert.ErrorContains(t, err, "marshal request")
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abcdef", 3))
	assert.Equal(t, "ab", truncate("ab", 3))
}

func nanValue() float64 {
	zero := 0.0
	return zero / zero
}
