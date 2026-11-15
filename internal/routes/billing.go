package routes

import (
	"context"
	"errors"
	"log/slog"

	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/billing"
	"github.com/SOAT-15-Oficina/oficina-mecanica-billing-service/internal/observability"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type Billing interface {
	QuoteByWorkOrder(ctx context.Context, workOrderID uuid.UUID) (billing.Quote, error)
	PublicQuote(ctx context.Context, quoteID uuid.UUID) (billing.PublicQuote, error)
	DecideItem(ctx context.Context, quoteID, workOrderServiceID uuid.UUID, decision, correlationID string) (billing.PublicQuote, error)
	DecideAll(ctx context.Context, quoteID uuid.UUID, decision, correlationID string) (billing.PublicQuote, error)
	HandleWebhook(ctx context.Context, n billing.Notification) error
	PaymentPage(ctx context.Context, paymentID uuid.UUID) (billing.PaymentPage, error)
}

type billingHandler struct {
	svc Billing
}

func registerBilling(group fiber.Router, svc Billing, authenticate, authorize fiber.Handler) {
	h := billingHandler{svc: svc}

	group.Get("/quotes/by-work-order/:workOrderId", authenticate, authorize, h.quoteByWorkOrder)

	public := group.Group("/public/quotes")
	public.Get("/:quoteId", h.publicQuote)
	public.Get("/:quoteId/items/:workOrderServiceId/approve", h.decideItem(billing.ItemApproved))
	public.Get("/:quoteId/items/:workOrderServiceId/reject", h.decideItem(billing.ItemRejected))
	public.Get("/:quoteId/approve-all", h.decideAll(billing.ItemApproved))
	public.Get("/:quoteId/reject-all", h.decideAll(billing.ItemRejected))

	payments := group.Group("/public/payments")
	payments.Get("/:paymentId/qr", h.qrHTML)
	payments.Get("/:paymentId/qr.png", h.qrPNG)

	group.Post("/webhooks/mercadopago", h.webhook)
}

func (h billingHandler) quoteByWorkOrder(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("workOrderId"))
	if err != nil {
		return badRequest(c, "invalid work order id")
	}
	q, err := h.svc.QuoteByWorkOrder(c.Context(), id)
	if err != nil {
		return failure(c, err, "quote not found")
	}
	return c.JSON(q)
}

func (h billingHandler) publicQuote(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("quoteId"))
	if err != nil {
		return badRequest(c, "invalid quote id")
	}
	q, err := h.svc.PublicQuote(c.Context(), id)
	if err != nil {
		return failure(c, err, "quote not found")
	}
	return c.JSON(q)
}

func (h billingHandler) decideItem(decision string) fiber.Handler {
	return func(c fiber.Ctx) error {
		quoteID, err := uuid.Parse(c.Params("quoteId"))
		if err != nil {
			return badRequest(c, "invalid quote id")
		}
		itemID, err := uuid.Parse(c.Params("workOrderServiceId"))
		if err != nil {
			return badRequest(c, "invalid work order service id")
		}
		q, err := h.svc.DecideItem(c.Context(), quoteID, itemID, decision, c.Get("X-Request-Id"))
		if err != nil {
			return failure(c, err, "quote item not found")
		}
		return c.JSON(fiber.Map{"message": decisionMessage(decision, false), "quote": q})
	}
}

func (h billingHandler) decideAll(decision string) fiber.Handler {
	return func(c fiber.Ctx) error {
		quoteID, err := uuid.Parse(c.Params("quoteId"))
		if err != nil {
			return badRequest(c, "invalid quote id")
		}
		q, err := h.svc.DecideAll(c.Context(), quoteID, decision, c.Get("X-Request-Id"))
		if err != nil {
			return failure(c, err, "quote not found")
		}
		return c.JSON(fiber.Map{"message": decisionMessage(decision, true), "quote": q})
	}
}

func (h billingHandler) webhook(c fiber.Ctx) error {
	err := h.svc.HandleWebhook(c.Context(), billing.Notification{
		DataID:    c.Query("data.id"),
		Type:      c.Query("type"),
		Signature: c.Get("x-signature"),
		RequestID: c.Get("x-request-id"),
	})
	if errors.Is(err, billing.ErrInvalidSignature) {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid signature"})
	}
	if err != nil {
		return internalError(c, err)
	}
	return c.JSON(fiber.Map{"status": "ok"})
}

func decisionMessage(decision string, all bool) string {
	switch {
	case all && decision == billing.ItemApproved:
		return "Todos os serviços pendentes foram aprovados"
	case all:
		return "Todos os serviços pendentes foram reprovados"
	case decision == billing.ItemApproved:
		return "Serviço aprovado com sucesso"
	default:
		return "Serviço reprovado com sucesso"
	}
}

func failure(c fiber.Ctx, err error, notFound string) error {
	switch {
	case errors.Is(err, billing.ErrNotFound):
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": notFound})
	case errors.Is(err, billing.ErrConflict):
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "quote or item already decided or canceled"})
	case errors.Is(err, billing.ErrInvalidDecision):
		return badRequest(c, "invalid decision")
	}
	return internalError(c, err)
}

func badRequest(c fiber.Ctx, message string) error {
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": message})
}

func internalError(c fiber.Ctx, err error) error {
	slog.Default().ErrorContext(c.Context(), "http: request failed",
		slog.String(observability.KeyRoute, c.Route().Path), observability.Err(err))
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "internal server error"})
}
