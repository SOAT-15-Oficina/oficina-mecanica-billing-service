-- +goose Up
CREATE TABLE quotes (
    id                   UUID PRIMARY KEY,
    work_order_id        UUID        NOT NULL UNIQUE,
    saga_id              UUID        NOT NULL,
    work_order_code      TEXT        NOT NULL,
    customer_name        TEXT        NOT NULL,
    customer_email       TEXT        NOT NULL,
    status               TEXT        NOT NULL CHECK (status IN ('ENVIADO', 'APROVADO', 'REPROVADO', 'CANCELADO')),
    total_cents          INTEGER     NOT NULL CHECK (total_cents >= 0),
    approved_total_cents INTEGER     NOT NULL DEFAULT 0 CHECK (approved_total_cents >= 0),
    has_supply_shortage  BOOLEAN     NOT NULL DEFAULT FALSE,
    sent_at              TIMESTAMPTZ NOT NULL,
    decided_at           TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE quote_items (
    id                     UUID PRIMARY KEY,
    quote_id               UUID    NOT NULL REFERENCES quotes (id) ON DELETE CASCADE,
    work_order_service_id  UUID    NOT NULL,
    title                  TEXT    NOT NULL,
    price_cents            INTEGER NOT NULL CHECK (price_cents >= 0),
    estimated_time_minutes INTEGER NOT NULL CHECK (estimated_time_minutes >= 0),
    decision               TEXT    NOT NULL DEFAULT 'PENDENTE' CHECK (decision IN ('PENDENTE', 'APROVADO', 'REPROVADO')),
    decided_at             TIMESTAMPTZ,
    UNIQUE (quote_id, work_order_service_id)
);

CREATE TABLE payments (
    id                     UUID PRIMARY KEY,
    quote_id               UUID        NOT NULL REFERENCES quotes (id),
    status                 TEXT        NOT NULL CHECK (status IN ('PENDENTE', 'APROVADO', 'RECUSADO', 'EXPIRADO', 'ESTORNADO')),
    amount_cents           INTEGER     NOT NULL CHECK (amount_cents > 0),
    preference_id          TEXT        NOT NULL UNIQUE,
    checkout_url           TEXT        NOT NULL,
    provider_payment_id    TEXT UNIQUE,
    provider_refund_id     TEXT,
    failure_reason         TEXT,
    expires_at             TIMESTAMPTZ NOT NULL,
    confirmed_at           TIMESTAMPTZ,
    refunded_at            TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX payments_one_approved_per_quote
    ON payments (quote_id) WHERE status IN ('APROVADO', 'ESTORNADO');

CREATE TABLE outbox (
    id             UUID PRIMARY KEY,
    message_type   TEXT        NOT NULL,
    group_id       TEXT        NOT NULL,
    body           JSONB       NOT NULL,
    trace_carrier  JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at   TIMESTAMPTZ
);

CREATE INDEX outbox_pending ON outbox (created_at) WHERE published_at IS NULL;

CREATE TABLE processed_messages (
    message_id   UUID PRIMARY KEY,
    message_type TEXT        NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE processed_messages;
DROP TABLE outbox;
DROP TABLE payments;
DROP TABLE quote_items;
DROP TABLE quotes;
