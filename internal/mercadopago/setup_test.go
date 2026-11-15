package mercadopago

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupCreatesStoreAndPOSWhenMissing(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /users/me":
			_, _ = w.Write([]byte(`{"id":3846953128,"site_id":"MLB"}`))
		case "GET /users/3846953128/stores/search", "GET /pos":
			_, _ = w.Write([]byte(`{"paging":{"total":0},"results":[]}`))
		case "POST /users/3846953128/stores":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"83215069","name":"Oficina Matriz","external_id":"OFICINAMATRIZ"}`))
		case "POST /v2/pos":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":140866185,"name":"Caixa Oficina Matriz","store_id":"83215069","external_id":"OFICINAMATRIZPOS01","status":"active"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	res, err := Setup(context.Background(), NewClient(srv.URL, "t", nil), DefaultStoreExternalID, DefaultPOSExternalID)
	require.NoError(t, err)
	assert.Equal(t, "3846953128", res.UserID)
	assert.True(t, res.StoreCreated)
	assert.True(t, res.POSCreated)
	assert.Equal(t, "83215069", res.Store.ID.String())
	assert.Equal(t, "140866185", res.POS.ID.String())

	require.Len(t, rec.calls, 5)
	assert.Equal(t, "/users/3846953128/stores/search?external_id=OFICINAMATRIZ", rec.calls[1].path)
	assert.Equal(t, "/pos?external_id=OFICINAMATRIZPOS01", rec.calls[3].path)
	assert.NotEmpty(t, rec.calls[4].key)
	assert.JSONEq(t, `{"name":"Caixa Oficina Matriz","store_id":"83215069","external_id":"OFICINAMATRIZPOS01","config":{"qr":{"operating_mode":"pdv"}}}`,
		string(rec.calls[4].body))

	var store map[string]any
	require.NoError(t, json.Unmarshal(rec.calls[2].body, &store))
	assert.Equal(t, "OFICINAMATRIZ", store["external_id"])
	assert.Contains(t, store, "location")
}

func TestSetupReusesExistingStoreAndPOS(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/me":
			_, _ = w.Write([]byte(`{"id":1}`))
		case "/users/1/stores/search":
			_, _ = w.Write([]byte(`{"results":[{"id":"9","external_id":"OTHER"},{"id":"10","external_id":"OFICINAMATRIZ"}]}`))
		case "/pos":
			_, _ = w.Write([]byte(`{"results":[{"id":20,"external_id":"OFICINAMATRIZPOS01","store_id":"10"}]}`))
		}
	})

	res, err := Setup(context.Background(), NewClient(srv.URL, "t", nil), DefaultStoreExternalID, DefaultPOSExternalID)
	require.NoError(t, err)
	assert.False(t, res.StoreCreated)
	assert.False(t, res.POSCreated)
	assert.Equal(t, "10", res.Store.ID.String())
	assert.Equal(t, "20", res.POS.ID.String())
	assert.Len(t, rec.calls, 3)
}

func TestSetupReportsEachFailure(t *testing.T) {
	steps := map[string]string{
		"/users/me":              "users/me",
		"/users/1/stores/search": "search store",
		"POST /users/1/stores":   "create store",
		"/pos":                   "search pos",
		"POST /v2/pos":           "create pos",
	}
	for failing, want := range steps {
		t.Run(want, func(t *testing.T) {
			srv := (&recorder{}).server(t, func(w http.ResponseWriter, r *http.Request) {
				if failing == r.URL.Path || failing == r.Method+" "+r.URL.Path {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				switch r.URL.Path {
				case "/users/me":
					_, _ = w.Write([]byte(`{"id":1}`))
				case "/users/1/stores", "/v2/pos":
					_, _ = w.Write([]byte(`{"id":"5"}`))
				default:
					_, _ = w.Write([]byte(`{"results":[{"external_id":"OTHER"}]}`))
				}
			})
			_, err := Setup(context.Background(), NewClient(srv.URL, "t", nil), DefaultStoreExternalID, DefaultPOSExternalID)
			assert.ErrorContains(t, err, want)
		})
	}
}
