-- +goose Up
CREATE TABLE payments (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id     TEXT NOT NULL,
    amount_cents BIGINT NOT NULL,
    currency     CHAR(3) NOT NULL,
    status       TEXT NOT NULL DEFAULT 'REQUIRES_CONFIRMATION'
                 CHECK (status IN ('REQUIRES_CONFIRMATION', 'AUTHORIZED', 'FAILED', 'REFUNDED', 'VOIDED')),
    method_token TEXT NOT NULL DEFAULT '',
    client_secret TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX payments_order_id_idx ON payments (order_id);

CREATE TABLE outbox (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic        TEXT NOT NULL,
    key          BYTEA,
    payload      BYTEA NOT NULL,
    headers      JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);
CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;

-- +goose Down
DROP TABLE outbox;
DROP TABLE payments;
