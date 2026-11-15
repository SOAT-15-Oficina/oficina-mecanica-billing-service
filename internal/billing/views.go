package billing

import (
	"context"

	"github.com/google/uuid"
)

func (s *Service) QuoteByWorkOrder(ctx context.Context, workOrderID uuid.UUID) (Quote, error) {
	return quoteByWorkOrder(ctx, s.db, workOrderID, false)
}

func (s *Service) PublicQuote(ctx context.Context, quoteID uuid.UUID) (PublicQuote, error) {
	q, err := quoteByID(ctx, s.db, quoteID, false)
	if err != nil {
		return PublicQuote{}, err
	}
	return q.Public(), nil
}

func (s *Service) PaymentPage(ctx context.Context, paymentID uuid.UUID) (PaymentPage, error) {
	quoteID, err := paymentQuoteID(ctx, s.db, "id", paymentID.String())
	if err != nil {
		return PaymentPage{}, err
	}
	q, err := quoteByID(ctx, s.db, quoteID, false)
	if err != nil {
		return PaymentPage{}, err
	}
	for _, p := range q.Payments {
		if p.ID == paymentID {
			return PaymentPage{Payment: p, WorkOrderCode: q.WorkOrderCode, CustomerName: q.CustomerName}, nil
		}
	}
	return PaymentPage{}, ErrNotFound
}
