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
