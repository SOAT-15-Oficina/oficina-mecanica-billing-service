package mercadopago

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type call struct {
	method, path, key string
	body              []byte
}

type recorder struct {
	mu    sync.Mutex
	calls []call
}

func (r *recorder) server(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.calls = append(r.calls, call{method: req.Method, path: req.URL.RequestURI(), key: req.Header.Get("X-Idempotency-Key"), body: body})
		r.mu.Unlock()
		handler(w, req)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const orderJSON = `{"id":"ORDTST01M7ERPPW2WBHKKR91AB0PZ2JB","status":"created","status_detail":"created","expiration_time":"PT30M",
 "external_reference":"pay-1","total_amount":"150.50",
 "transactions":{"payments":[{"id":"PAY01M7ERPPWKRAMMFC3N54WVA2J2","amount":"150.50","status":"created","status_detail":"ready_to_process"}]},
 "type_response":{"qr_data":"00020101021243650016COM.MERCADOLIBRE"}}`

func TestCreateQROrder(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer TEST-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(orderJSON))
	})

	order, err := NewClient(srv.URL+"/", "TEST-token", nil).CreateQROrder(context.Background(), QROrder{
		ExternalReference: "pay-1",
		Description:       "OS-1",
		ExternalPOSID:     DefaultPOSExternalID,
		Expiration:        48 * time.Hour,
		Items:             []QRItem{{Title: "Troca de oleo", PriceCents: 15000}, {Title: "Filtro", PriceCents: 50}},
	}, "msg-1")
	require.NoError(t, err)
	assert.Equal(t, "ORDTST01M7ERPPW2WBHKKR91AB0PZ2JB", order.ID)
	assert.Equal(t, "PAY01M7ERPPWKRAMMFC3N54WVA2J2", order.PaymentID())
	assert.Equal(t, "00020101021243650016COM.MERCADOLIBRE", order.TypeResponse.QRData)

	require.Len(t, rec.calls, 1)
	c := rec.calls[0]
	assert.Equal(t, http.MethodPost, c.method)
	assert.Equal(t, "/v1/orders", c.path)
	assert.Equal(t, "msg-1", c.key)
	assert.JSONEq(t, `{"type":"qr","total_amount":"150.50","description":"OS-1","external_reference":"pay-1",
		"expiration_time":"PT48H","config":{"qr":{"external_pos_id":"OFICINAMATRIZPOS01","mode":"dynamic"}},
		"transactions":{"payments":[{"amount":"150.50"}]},
		"items":[{"title":"Troca de oleo","unit_price":"150.00","quantity":1,"unit_measure":"unit"},
		         {"title":"Filtro","unit_price":"0.50","quantity":1,"unit_measure":"unit"}]}`, string(c.body))
}

func TestGetOrder(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(orderJSON)) })

	order, err := NewClient(srv.URL, "t", nil).GetOrder(context.Background(), "ORD1")
	require.NoError(t, err)
	assert.Equal(t, OrderCreated, order.Status)
	assert.Equal(t, "pay-1", order.ExternalReference)
	assert.Equal(t, "/v1/orders/ORD1", rec.calls[0].path)
	assert.Equal(t, http.MethodGet, rec.calls[0].method)
}

func TestGetOrderNotFound(t *testing.T) {
	srv := (&recorder{}).server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	_, err := NewClient(srv.URL, "t", nil).GetOrder(context.Background(), "ORD9")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCancelOrder(t *testing.T) {
	for status, wantErr := range map[int]bool{http.StatusOK: false, http.StatusConflict: false, http.StatusInternalServerError: true} {
		rec := &recorder{}
		srv := rec.server(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"id":"ORD1","status":"canceled"}`))
		})
		err := NewClient(srv.URL, "t", nil).CancelOrder(context.Background(), "ORD1", "msg-2")
		assert.Equal(t, wantErr, err != nil, status)
		assert.Equal(t, "/v1/orders/ORD1/cancel", rec.calls[0].path)
		assert.Equal(t, "msg-2", rec.calls[0].key)
	}
}

func TestRefundOrder(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"ORD1","status":"processed","transactions":{"refunds":[{"id":"REF1","status":"processing"}]}}`))
	})
	order, err := NewClient(srv.URL, "t", nil).RefundOrder(context.Background(), "ORD1", "msg-3")
	require.NoError(t, err)
	assert.Equal(t, "REF1", order.RefundID())
	require.Len(t, rec.calls, 1)
	assert.Equal(t, "/v1/orders/ORD1/refund", rec.calls[0].path)
	assert.Equal(t, "msg-3", rec.calls[0].key)
}

func TestRefundOrderFallsBackToRefunds(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		rec := &recorder{}
		srv := rec.server(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/orders/ORD1/refund" {
				w.WriteHeader(status)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"refunded"}`))
		})
		order, err := NewClient(srv.URL, "t", nil).RefundOrder(context.Background(), "ORD1", "k")
		require.NoError(t, err)
		assert.Equal(t, "ORD1", order.ID)
		assert.Equal(t, "ORD1", order.RefundID())
		require.Len(t, rec.calls, 2)
		assert.Equal(t, "/v1/orders/ORD1/refunds", rec.calls[1].path)
		assert.Equal(t, "k", rec.calls[1].key)
	}
}

func TestRefundOrderAlreadyRefundedIsSuccess(t *testing.T) {
	srv := (&recorder{}).server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"errors":[{"code":"order_already_refunded","message":"already refunded"}]}`))
	})
	order, err := NewClient(srv.URL, "t", nil).RefundOrder(context.Background(), "ORD1", "k")
	require.NoError(t, err)
	assert.Equal(t, OrderRefunded, order.Status)
}

func TestRefundOrderOtherConflictFails(t *testing.T) {
	srv := (&recorder{}).server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"errors":[{"code":"invalid_status"}]}`))
	})
	_, err := NewClient(srv.URL, "t", nil).RefundOrder(context.Background(), "ORD1", "k")
	assert.ErrorContains(t, err, "returned 409")
}

func TestErrorsAreReported(t *testing.T) {
	srv := (&recorder{}).server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/orders/bad-json" {
			_, _ = w.Write([]byte(`{`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid"}`))
	})
	c := NewClient(srv.URL, "t", nil)

	_, err := c.CreateQROrder(context.Background(), QROrder{}, "k")
	assert.ErrorContains(t, err, "returned 400")

	_, err = c.GetOrder(context.Background(), "bad-json")
	assert.ErrorContains(t, err, "decode response")

	_, err = NewClient("http://127.0.0.1:1", "t", nil).GetOrder(context.Background(), "1")
	assert.Error(t, err)

	_, err = NewClient("http://bad host", "t", nil).GetOrder(context.Background(), "1")
	assert.ErrorContains(t, err, "build request")

	err = c.do(context.Background(), http.MethodPost, "/x", map[string]float64{"v": nanValue()}, nil, nil)
	assert.ErrorContains(t, err, "marshal request")
}

func TestAmountAndDurations(t *testing.T) {
	assert.Equal(t, "150.50", Amount(15050))
	assert.Equal(t, "0.05", Amount(5))
	assert.Equal(t, "-10.00", Amount(-1000))

	assert.Equal(t, "PT48H", ISODuration(48*time.Hour))
	assert.Equal(t, "PT30M", ISODuration(30*time.Minute))
	assert.Equal(t, "PT1H30M15S", ISODuration(90*time.Minute+15*time.Second))
	assert.Equal(t, "PT1S", ISODuration(0))

	for in, want := range map[string]time.Duration{"PT48H": 48 * time.Hour, "PT30M": 30 * time.Minute, "pt1h30m15s": 90*time.Minute + 15*time.Second} {
		got, err := ParseISODuration(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "P1D", "PT", "PTH", "PT1X", "PT99999999999999999999H"} {
		_, err := ParseISODuration(in)
		assert.Error(t, err, in)
	}
}

func TestOrderHelpersWithoutTransactions(t *testing.T) {
	assert.Empty(t, Order{}.PaymentID())
	assert.Equal(t, "ORD1", Order{ID: "ORD1", Transactions: Transactions{Refunds: []OrderRefund{{}}}}.RefundID())
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abcdef", 3))
	assert.Equal(t, "ab", truncate("ab", 3))
}

func TestStoreIDAcceptsStringOrNumber(t *testing.T) {
	var s Store
	require.NoError(t, json.Unmarshal([]byte(`{"id":"83215069"}`), &s))
	assert.Equal(t, "83215069", s.ID.String())
	require.NoError(t, json.Unmarshal([]byte(`{"id":83215069}`), &s))
	assert.Equal(t, "83215069", s.ID.String())
}

func nanValue() float64 {
	zero := 0.0
	return zero / zero
}
