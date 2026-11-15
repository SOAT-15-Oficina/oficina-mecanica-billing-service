package routes

import (
	"bytes"
	"encoding/base64"
	"html/template"
	"time"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/billing"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"
)

const qrSize = 320

var saoPaulo = loadLocation()

func loadLocation() *time.Location {
	location, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		return time.FixedZone("BRT", -3*60*60)
	}
	return location
}

var qrPage = template.Must(template.New("qr").Parse(`<!doctype html>
<html lang="pt-BR">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Pagamento da ordem de serviço {{.WorkOrderCode}}</title>
</head>
<body style="font-family: Arial, Helvetica, sans-serif; color: #333333; background-color: #f4f4f7; margin: 0; padding: 16px">
  <main style="max-width: 480px; margin: 0 auto; background-color: #ffffff; border-radius: 8px; padding: 24px; text-align: center">
    <h1 style="font-size: 20px; margin: 0 0 8px 0">Pagamento da ordem de serviço {{.WorkOrderCode}}</h1>
    <p style="margin: 0 0 16px 0">Valor: <strong>{{.Amount}}</strong></p>
    <p id="status" style="margin: 0 0 16px 0">Situação: <strong>{{.StatusLabel}}</strong></p>
    {{if .Payable}}
    <img src="{{.Image}}" alt="QR Code de pagamento do Mercado Pago" width="{{.Size}}" height="{{.Size}}" style="max-width: 100%; height: auto">
    <p>Escaneie o QR Code com o app do Mercado Pago ou do seu banco.</p>
    <label for="copia-e-cola" style="display: block; font-weight: bold; margin-top: 16px">Pix copia e cola</label>
    <textarea id="copia-e-cola" readonly rows="4" style="width: 100%; box-sizing: border-box; font-family: monospace; font-size: 12px">{{.QRData}}</textarea>
    <button type="button" onclick="navigator.clipboard.writeText(document.getElementById('copia-e-cola').value)" style="margin-top: 8px; padding: 10px 16px">Copiar código</button>
    <p style="font-size: 13px; color: #6b7280">Válido até {{.ExpiresAt}}.</p>
    {{end}}
  </main>
</body>
</html>`))

type qrView struct {
	WorkOrderCode string
	Amount        string
	StatusLabel   string
	Payable       bool
	Image         template.URL
	Size          int
	QRData        string
	ExpiresAt     string
}

func statusLabel(p billing.Payment, now time.Time) (string, bool) {
	switch p.Status {
	case billing.PaymentPending:
		if !p.ExpiresAt.After(now) {
			return "QR Code expirado", false
		}
		return "Aguardando pagamento", true
	case billing.PaymentApproved:
		return "Pagamento confirmado", false
	case billing.PaymentExpired:
		return "QR Code expirado ou cancelado", false
	case billing.PaymentDeclined:
		return "Pagamento não concluído", false
	case billing.PaymentRefunded:
		return "Pagamento estornado", false
	}
	return p.Status, false
}

func (h billingHandler) paymentPage(c fiber.Ctx) (billing.PaymentPage, bool, error) {
	id, err := uuid.Parse(c.Params("paymentId"))
	if err != nil {
		return billing.PaymentPage{}, false, badRequest(c, "invalid payment id")
	}
	page, err := h.svc.PaymentPage(c.Context(), id)
	if err != nil {
		return billing.PaymentPage{}, false, failure(c, err, "payment not found")
	}
	return page, true, nil
}

func (h billingHandler) qrHTML(c fiber.Ctx) error {
	page, ok, err := h.paymentPage(c)
	if !ok {
		return err
	}
	label, payable := statusLabel(page.Payment, time.Now())
	view := qrView{
		WorkOrderCode: page.WorkOrderCode,
		Amount:        billing.FormatBRL(page.Payment.AmountCents),
		StatusLabel:   label,
		Payable:       payable,
		Size:          qrSize,
		QRData:        page.Payment.QRData,
		ExpiresAt:     page.Payment.ExpiresAt.In(saoPaulo).Format("02/01/2006 15:04"),
	}
	if payable {
		png, err := qrcode.Encode(page.Payment.QRData, qrcode.Medium, qrSize)
		if err != nil {
			return internalError(c, err)
		}
		view.Image = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
	}

	var buf bytes.Buffer
	if err := qrPage.Execute(&buf, view); err != nil {
		return internalError(c, err)
	}
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Send(buf.Bytes())
}

func (h billingHandler) qrPNG(c fiber.Ctx) error {
	page, ok, err := h.paymentPage(c)
	if !ok {
		return err
	}
	png, err := qrcode.Encode(page.Payment.QRData, qrcode.Medium, qrSize)
	if err != nil {
		return internalError(c, err)
	}
	c.Set(fiber.HeaderContentType, "image/png")
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Send(png)
}
