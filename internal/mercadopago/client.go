package mercadopago

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
	StatusRefunded  = "refunded"

	DateLayout = "2006-01-02T15:04:05.000-07:00"
)

var ErrNotFound = errors.New("mercadopago: not found")

type Item struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Quantity   int     `json:"quantity"`
	CurrencyID string  `json:"currency_id"`
	UnitPrice  float64 `json:"unit_price"`
}

type BackURLs struct {
	Success string `json:"success,omitempty"`
	Failure string `json:"failure,omitempty"`
	Pending string `json:"pending,omitempty"`
}

type PreferenceRequest struct {
	Items             []Item    `json:"items"`
	ExternalReference string    `json:"external_reference"`
	NotificationURL   string    `json:"notification_url"`
	Expires           bool      `json:"expires"`
	ExpirationDateTo  string    `json:"expiration_date_to"`
	BackURLs          *BackURLs `json:"back_urls,omitempty"`
}

type Preference struct {
	ID               string `json:"id"`
	InitPoint        string `json:"init_point"`
	SandboxInitPoint string `json:"sandbox_init_point"`
}

type Payment struct {
	ID                json.Number `json:"id"`
	Status            string      `json:"status"`
	StatusDetail      string      `json:"status_detail"`
	ExternalReference string      `json:"external_reference"`
	TransactionAmount float64     `json:"transaction_amount"`
}

type Refund struct {
	ID     json.Number `json:"id"`
	Status string      `json:"status"`
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

func NewItem(id, title string, priceCents int) Item {
	return Item{ID: id, Title: title, Quantity: 1, CurrencyID: "BRL", UnitPrice: float64(priceCents) / 100}
}

func ExpirationDate(t time.Time) string { return t.Format(DateLayout) }

func (c *Client) CreatePreference(ctx context.Context, req PreferenceRequest) (Preference, error) {
	var out Preference
	if err := c.do(ctx, http.MethodPost, "/checkout/preferences", req, nil, &out); err != nil {
		return Preference{}, err
	}
	if out.InitPoint == "" {
		out.InitPoint = out.SandboxInitPoint
	}
	return out, nil
}

func (c *Client) GetPayment(ctx context.Context, id string) (Payment, error) {
	var out Payment
	if err := c.do(ctx, http.MethodGet, "/v1/payments/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return Payment{}, err
	}
	return out, nil
}

func (c *Client) Refund(ctx context.Context, paymentID, idempotencyKey string) (Refund, error) {
	var out Refund
	headers := map[string]string{"X-Idempotency-Key": idempotencyKey}
	if err := c.do(ctx, http.MethodPost, "/v1/payments/"+url.PathEscape(paymentID)+"/refunds", struct{}{}, headers, &out); err != nil {
		return Refund{}, err
	}
	return out, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, headers map[string]string, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("mercadopago: marshal request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("mercadopago: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mercadopago: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("mercadopago: read response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s %s", ErrNotFound, method, path)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("mercadopago: %s %s returned %d: %s", method, path, resp.StatusCode, truncate(string(payload), 300))
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("mercadopago: decode response: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
