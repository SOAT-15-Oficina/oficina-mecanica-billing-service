package routes

import (
	"bytes"
	"errors"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/billing"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const qrData = "00020101021243650016COM.MERCADOLIBRE02013063649e57dc5-c855-4020-98ad-f814ff91dbb15204000053039865802BR5909Test Test6009SAO PAULO62070503***63043630"

func raw(t *testing.T, app *fiber.App, path string) (int, string, []byte) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), body
}

func pendingPage() billing.PaymentPage {
	return billing.PaymentPage{
		WorkOrderCode: "OS-7",
		Payment: billing.Payment{
			Status: billing.PaymentPending, AmountCents: 15050, QRData: qrData,
			ExpiresAt: time.Date(2099, 11, 13, 13, 0, 0, 0, time.UTC),
		},
	}
}

func TestQRPageShowsCodeWhilePending(t *testing.T) {
	svc := &stubBilling{page: pendingPage()}
	app := newBillingApp(svc)
	id := uuid.New()

	status, contentType, body := raw(t, app, Prefix+"/public/payments/"+id.String()+"/qr")
	assert.Equal(t, fiber.StatusOK, status)
	assert.Contains(t, contentType, "text/html")
	page := string(body)
	for _, want := range []string{"OS-7", "R$ 150,50", "Aguardando pagamento", "data:image/png;base64,", qrData, "copia e cola", "13/11/2099 10:00"} {
		assert.Contains(t, page, want)
	}
}

func TestQRPageShowsOnlyStatusWhenSettled(t *testing.T) {
	cases := map[string]string{
		billing.PaymentApproved: "Pagamento confirmado",
		billing.PaymentExpired:  "QR Code expirado ou cancelado",
		billing.PaymentDeclined: "Pagamento não concluído",
		billing.PaymentRefunded: "Pagamento estornado",
		"OUTRO":                 "OUTRO",
	}
	for status, label := range cases {
		t.Run(status, func(t *testing.T) {
			page := pendingPage()
			page.Payment.Status = status
			app := newBillingApp(&stubBilling{page: page})
			code, _, body := raw(t, app, Prefix+"/public/payments/"+uuid.NewString()+"/qr")
			assert.Equal(t, fiber.StatusOK, code)
			assert.Contains(t, string(body), label)
			assert.NotContains(t, string(body), qrData)
			assert.NotContains(t, string(body), "data:image/png")
		})
	}

	page := pendingPage()
	page.Payment.ExpiresAt = time.Now().Add(-time.Minute)
	_, _, body := raw(t, newBillingApp(&stubBilling{page: page}), Prefix+"/public/payments/"+uuid.NewString()+"/qr")
	assert.Contains(t, string(body), "QR Code expirado")
	assert.NotContains(t, string(body), qrData)
}

func TestQRImage(t *testing.T) {
	app := newBillingApp(&stubBilling{page: pendingPage()})
	status, contentType, body := raw(t, app, Prefix+"/public/payments/"+uuid.NewString()+"/qr.png")
	assert.Equal(t, fiber.StatusOK, status)
	assert.Equal(t, "image/png", contentType)
	img, err := png.Decode(bytes.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, qrSize, img.Bounds().Dx())
}

func TestQRErrors(t *testing.T) {
	for _, suffix := range []string{"/qr", "/qr.png"} {
		app := newBillingApp(&stubBilling{page: pendingPage()})
		status, _, _ := raw(t, app, Prefix+"/public/payments/abc"+suffix)
		assert.Equal(t, fiber.StatusBadRequest, status, suffix)

		app = newBillingApp(&stubBilling{err: billing.ErrNotFound})
		status, _, body := raw(t, app, Prefix+"/public/payments/"+uuid.NewString()+suffix)
		assert.Equal(t, fiber.StatusNotFound, status, suffix)
		assert.Contains(t, string(body), "payment not found")

		app = newBillingApp(&stubBilling{err: errors.New("db down")})
		status, _, _ = raw(t, app, Prefix+"/public/payments/"+uuid.NewString()+suffix)
		assert.Equal(t, fiber.StatusInternalServerError, status, suffix)

		empty := pendingPage()
		empty.Payment.QRData = ""
		app = newBillingApp(&stubBilling{page: empty})
		status, _, _ = raw(t, app, Prefix+"/public/payments/"+uuid.NewString()+suffix)
		assert.Equal(t, fiber.StatusInternalServerError, status, suffix)
	}
}
