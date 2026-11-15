package mercadopago

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	OrderCreated   = "created"
	OrderProcessed = "processed"
	OrderExpired   = "expired"
	OrderCanceled  = "canceled"
	OrderRefunded  = "refunded"

	OrderTypeQR   = "qr"
	QRModeDynamic = "dynamic"

	codeAlreadyRefunded = "order_already_refunded"
)

type OrderItem struct {
	Title       string `json:"title"`
	UnitPrice   string `json:"unit_price"`
	Quantity    int    `json:"quantity"`
	UnitMeasure string `json:"unit_measure,omitempty"`
}

type QRConfig struct {
	ExternalPOSID string `json:"external_pos_id"`
	Mode          string `json:"mode"`
}

type OrderConfig struct {
	QR QRConfig `json:"qr"`
}

type OrderPayment struct {
	ID           string `json:"id,omitempty"`
	Amount       string `json:"amount"`
	Status       string `json:"status,omitempty"`
	StatusDetail string `json:"status_detail,omitempty"`
}

type OrderRefund struct {
	ID     string `json:"id,omitempty"`
	Amount string `json:"amount,omitempty"`
	Status string `json:"status,omitempty"`
}

type Transactions struct {
	Payments []OrderPayment `json:"payments"`
	Refunds  []OrderRefund  `json:"refunds,omitempty"`
}

type TypeResponse struct {
	QRData string `json:"qr_data"`
}

type OrderRequest struct {
	Type              string       `json:"type"`
	TotalAmount       string       `json:"total_amount"`
	Description       string       `json:"description,omitempty"`
	ExternalReference string       `json:"external_reference"`
	ExpirationTime    string       `json:"expiration_time,omitempty"`
	Config            OrderConfig  `json:"config"`
	Transactions      Transactions `json:"transactions"`
	Items             []OrderItem  `json:"items"`
}

type Order struct {
	ID                string       `json:"id"`
	Type              string       `json:"type"`
	Status            string       `json:"status"`
	StatusDetail      string       `json:"status_detail"`
	ExternalReference string       `json:"external_reference"`
	Description       string       `json:"description,omitempty"`
	TotalAmount       string       `json:"total_amount"`
	ExpirationTime    string       `json:"expiration_time,omitempty"`
	Config            OrderConfig  `json:"config"`
	Transactions      Transactions `json:"transactions"`
	Items             []OrderItem  `json:"items,omitempty"`
	TypeResponse      TypeResponse `json:"type_response"`
}

func (o Order) PaymentID() string {
	if len(o.Transactions.Payments) == 0 {
		return ""
	}
	return o.Transactions.Payments[0].ID
}

func (o Order) RefundID() string {
	for _, r := range o.Transactions.Refunds {
		if r.ID != "" {
			return r.ID
		}
	}
	return o.ID
}

type QROrder struct {
	ExternalReference string
	Description       string
	ExternalPOSID     string
	Expiration        time.Duration
	Items             []QRItem
}

type QRItem struct {
	Title      string
	PriceCents int
}

func (q QROrder) Request() OrderRequest {
	total := 0
	items := make([]OrderItem, 0, len(q.Items))
	for _, it := range q.Items {
		total += it.PriceCents
		items = append(items, OrderItem{Title: it.Title, UnitPrice: Amount(it.PriceCents), Quantity: 1, UnitMeasure: "unit"})
	}
	return OrderRequest{
		Type:              OrderTypeQR,
		TotalAmount:       Amount(total),
		Description:       q.Description,
		ExternalReference: q.ExternalReference,
		ExpirationTime:    ISODuration(q.Expiration),
		Config:            OrderConfig{QR: QRConfig{ExternalPOSID: q.ExternalPOSID, Mode: QRModeDynamic}},
		Transactions:      Transactions{Payments: []OrderPayment{{Amount: Amount(total)}}},
		Items:             items,
	}
}

func (c *Client) CreateQROrder(ctx context.Context, order QROrder, idempotencyKey string) (Order, error) {
	var out Order
	err := c.do(ctx, http.MethodPost, "/v1/orders", order.Request(), idempotency(idempotencyKey), &out)
	return out, err
}

func (c *Client) GetOrder(ctx context.Context, id string) (Order, error) {
	var out Order
	err := c.do(ctx, http.MethodGet, "/v1/orders/"+escape(id), nil, nil, &out)
	return out, err
}

func (c *Client) CancelOrder(ctx context.Context, id, idempotencyKey string) error {
	err := c.do(ctx, http.MethodPost, "/v1/orders/"+escape(id)+"/cancel", nil, idempotency(idempotencyKey), nil)
	if statusOf(err) == http.StatusConflict {
		return nil
	}
	return err
}

func (c *Client) RefundOrder(ctx context.Context, id, idempotencyKey string) (Order, error) {
	var out Order
	base := "/v1/orders/" + escape(id)
	err := c.do(ctx, http.MethodPost, base+"/refund", nil, idempotency(idempotencyKey), &out)
	if status := statusOf(err); status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		err = c.do(ctx, http.MethodPost, base+"/refunds", nil, idempotency(idempotencyKey), &out)
	}
	if alreadyRefunded(err) {
		return Order{ID: id, Status: OrderRefunded}, nil
	}
	if err != nil {
		return Order{}, err
	}
	if out.ID == "" {
		out.ID = id
	}
	return out, nil
}

func alreadyRefunded(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict && strings.Contains(apiErr.Body, codeAlreadyRefunded)
}

func idempotency(key string) map[string]string {
	return map[string]string{"X-Idempotency-Key": key}
}

func Amount(cents int) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

func ISODuration(d time.Duration) string {
	if d < time.Second {
		d = time.Second
	}
	total := int64(d / time.Second)
	hours, minutes, seconds := total/3600, (total%3600)/60, total%60
	var b strings.Builder
	b.WriteString("PT")
	if hours > 0 {
		b.WriteString(strconv.FormatInt(hours, 10) + "H")
	}
	if minutes > 0 {
		b.WriteString(strconv.FormatInt(minutes, 10) + "M")
	}
	if seconds > 0 {
		b.WriteString(strconv.FormatInt(seconds, 10) + "S")
	}
	return b.String()
}

func ParseISODuration(s string) (time.Duration, error) {
	rest, ok := strings.CutPrefix(strings.ToUpper(s), "PT")
	if !ok || rest == "" {
		return 0, fmt.Errorf("mercadopago: invalid duration %q", s)
	}
	units := map[byte]time.Duration{'H': time.Hour, 'M': time.Minute, 'S': time.Second}
	var total time.Duration
	for rest != "" {
		i := strings.IndexAny(rest, "HMS")
		if i <= 0 {
			return 0, fmt.Errorf("mercadopago: invalid duration %q", s)
		}
		n, err := strconv.ParseInt(rest[:i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("mercadopago: invalid duration %q", s)
		}
		total += time.Duration(n) * units[rest[i]]
		rest = rest[i+1:]
	}
	return total, nil
}
