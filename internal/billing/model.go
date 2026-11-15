package billing

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	QuoteSent     = "ENVIADO"
	QuoteApproved = "APROVADO"
	QuoteRejected = "REPROVADO"
	QuoteCanceled = "CANCELADO"

	ItemPending  = "PENDENTE"
	ItemApproved = "APROVADO"
	ItemRejected = "REPROVADO"

	PaymentPending  = "PENDENTE"
	PaymentApproved = "APROVADO"
	PaymentDeclined = "RECUSADO"
	PaymentExpired  = "EXPIRADO"
	PaymentRefunded = "ESTORNADO"
)

var (
	ErrNotFound         = errors.New("billing: not found")
	ErrConflict         = errors.New("billing: conflict")
	ErrInvalidSignature = errors.New("billing: invalid webhook signature")
	ErrInvalidDecision  = errors.New("billing: invalid decision")
)

type Item struct {
	ID                   uuid.UUID  `json:"id"`
	WorkOrderServiceID   uuid.UUID  `json:"work_order_service_id"`
	Title                string     `json:"title"`
	PriceCents           int        `json:"price_cents"`
	EstimatedTimeMinutes int        `json:"estimated_time_minutes"`
	Decision             string     `json:"decision"`
	DecidedAt            *time.Time `json:"decided_at,omitempty"`
}

type Payment struct {
	ID                uuid.UUID  `json:"id"`
	Status            string     `json:"status"`
	AmountCents       int        `json:"amount_cents"`
	ProviderOrderID   string     `json:"provider_order_id"`
	QRCodeURL         string     `json:"qr_code_url"`
	QRData            string     `json:"qr_data"`
	ProviderPaymentID *string    `json:"provider_payment_id,omitempty"`
	ProviderRefundID  *string    `json:"provider_refund_id,omitempty"`
	FailureReason     *string    `json:"failure_reason,omitempty"`
	ExpiresAt         time.Time  `json:"expires_at"`
	ConfirmedAt       *time.Time `json:"confirmed_at,omitempty"`
	RefundedAt        *time.Time `json:"refunded_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

type Quote struct {
	ID                 uuid.UUID  `json:"id"`
	WorkOrderID        uuid.UUID  `json:"work_order_id"`
	SagaID             uuid.UUID  `json:"saga_id"`
	WorkOrderCode      string     `json:"work_order_code"`
	CustomerName       string     `json:"customer_name"`
	CustomerEmail      string     `json:"customer_email"`
	Status             string     `json:"status"`
	TotalCents         int        `json:"total_cents"`
	ApprovedTotalCents int        `json:"approved_total_cents"`
	SupplyShortage     bool       `json:"supply_shortage"`
	SentAt             time.Time  `json:"sent_at"`
	DecidedAt          *time.Time `json:"decided_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	Items              []Item     `json:"items"`
	Payments           []Payment  `json:"payments"`
}

type PublicQuote struct {
	ID                 uuid.UUID       `json:"id"`
	WorkOrderCode      string          `json:"work_order_code"`
	CustomerName       string          `json:"customer_name"`
	Status             string          `json:"status"`
	TotalCents         int             `json:"total_cents"`
	ApprovedTotalCents int             `json:"approved_total_cents"`
	SupplyShortage     bool            `json:"supply_shortage"`
	SentAt             time.Time       `json:"sent_at"`
	DecidedAt          *time.Time      `json:"decided_at,omitempty"`
	Items              []Item          `json:"items"`
	PaymentURL         string          `json:"payment_url,omitempty"`
	Payments           []PublicPayment `json:"payments"`
}

type PublicPayment struct {
	ID              uuid.UUID `json:"id"`
	Status          string    `json:"status"`
	AmountCents     int       `json:"amount_cents"`
	ProviderOrderID string    `json:"provider_order_id"`
	QRCodeURL       string    `json:"qr_code_url"`
	QRData          string    `json:"qr_data"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type PaymentPage struct {
	Payment       Payment
	WorkOrderCode string
	CustomerName  string
}

func (q Quote) Public() PublicQuote {
	out := PublicQuote{
		ID:                 q.ID,
		WorkOrderCode:      q.WorkOrderCode,
		CustomerName:       q.CustomerName,
		Status:             q.Status,
		TotalCents:         q.TotalCents,
		ApprovedTotalCents: q.ApprovedTotalCents,
		SupplyShortage:     q.SupplyShortage,
		SentAt:             q.SentAt,
		DecidedAt:          q.DecidedAt,
		Items:              q.Items,
		Payments:           make([]PublicPayment, 0, len(q.Payments)),
	}
	for _, p := range q.Payments {
		out.Payments = append(out.Payments, PublicPayment{
			ID: p.ID, Status: p.Status, AmountCents: p.AmountCents, ProviderOrderID: p.ProviderOrderID,
			QRCodeURL: p.QRCodeURL, QRData: p.QRData, ExpiresAt: p.ExpiresAt,
		})
	}
	if p := q.payment(PaymentPending); p != nil {
		out.PaymentURL = p.QRCodeURL
	}
	return out
}

func (q Quote) payment(statuses ...string) *Payment {
	for i := len(q.Payments) - 1; i >= 0; i-- {
		for _, s := range statuses {
			if q.Payments[i].Status == s {
				return &q.Payments[i]
			}
		}
	}
	return nil
}

func (q Quote) paymentByOrder(orderID string) *Payment {
	for i := range q.Payments {
		if q.Payments[i].ProviderOrderID == orderID {
			return &q.Payments[i]
		}
	}
	return nil
}

func (q Quote) pendingItems() []uuid.UUID {
	var out []uuid.UUID
	for _, it := range q.Items {
		if it.Decision == ItemPending {
			out = append(out, it.WorkOrderServiceID)
		}
	}
	return out
}

func (q Quote) approvedItems() []Item {
	var out []Item
	for _, it := range q.Items {
		if it.Decision == ItemApproved {
			out = append(out, it)
		}
	}
	return out
}
