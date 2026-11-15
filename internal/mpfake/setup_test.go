package mpfake

import (
	"context"
	"net/http"
	"testing"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/mercadopago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupAgainstFakeIsIdempotent(t *testing.T) {
	e := setup(t, "/hook")
	ctx := context.Background()

	first, err := mercadopago.Setup(ctx, e.client, mercadopago.DefaultStoreExternalID, mercadopago.DefaultPOSExternalID)
	require.NoError(t, err)
	assert.Equal(t, fakeUserID, first.UserID)
	assert.True(t, first.StoreCreated)
	assert.True(t, first.POSCreated)
	assert.Equal(t, "active", first.POS.Status)
	assert.Equal(t, first.Store.ID.String(), first.POS.StoreID)

	second, err := mercadopago.Setup(ctx, e.client, mercadopago.DefaultStoreExternalID, mercadopago.DefaultPOSExternalID)
	require.NoError(t, err)
	assert.False(t, second.StoreCreated)
	assert.False(t, second.POSCreated)
	assert.Equal(t, first.Store.ID, second.Store.ID)
	assert.Equal(t, first.POS.ID, second.POS.ID)

	again, err := e.client.CreatePOS(ctx, mercadopago.NewPOS(first.Store.ID.String(), "OUTRO"), "setup-"+fakeUserID+"-"+mercadopago.DefaultPOSExternalID)
	require.NoError(t, err)
	assert.Equal(t, first.POS.ID, again.ID)
}

func TestSetupEndpointsValidate(t *testing.T) {
	e := setup(t, "/hook")
	ctx := context.Background()

	_, err := e.client.CreateStore(ctx, "999", mercadopago.NewStore("X"))
	assert.ErrorContains(t, err, "returned 403")
	_, err = e.client.CreateStore(ctx, fakeUserID, mercadopago.Store{Name: "x"})
	assert.ErrorContains(t, err, "returned 400")
	_, err = e.client.CreateStore(ctx, fakeUserID, mercadopago.NewStore("X"))
	require.NoError(t, err)
	_, err = e.client.CreateStore(ctx, fakeUserID, mercadopago.NewStore("X"))
	assert.ErrorContains(t, err, "returned 409")

	_, err = e.client.CreatePOS(ctx, mercadopago.NewPOS("1", "P"), "")
	assert.ErrorContains(t, err, "returned 400")
	_, err = e.client.CreatePOS(ctx, mercadopago.POS{Name: "x"}, "k1")
	assert.ErrorContains(t, err, "returned 400")
	_, err = e.client.CreatePOS(ctx, mercadopago.NewPOS("1", "P"), "k2")
	assert.ErrorContains(t, err, "store_not_found")

	pos, err := e.client.FindPOS(ctx, "NAO-EXISTE")
	require.NoError(t, err)
	assert.Nil(t, pos)
	code, _ := get(t, e.srv.URL+"/pos")
	assert.Equal(t, http.StatusUnauthorized, code)
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/users/"+fakeUserID+"/stores/search", nil)
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
