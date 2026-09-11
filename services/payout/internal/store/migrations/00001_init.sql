-- +goose Up
CREATE TABLE payouts (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id     TEXT NOT NULL,
    shop_id      TEXT NOT NULL, -- always set; first-party lines create no payout
    currency     CHAR(3) NOT NULL,
    amount_cents BIGINT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'PENDING'
                 CHECK (status IN ('PENDING', 'PAID')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at      TIMESTAMPTZ,
    UNIQUE (order_id, shop_id)
);
CREATE INDEX payouts_shop_created_idx ON payouts (shop_id, created_at DESC);
-- The sandbox settlement sweep scans by (status, created_at).
CREATE INDEX payouts_pending_idx ON payouts (created_at) WHERE status = 'PENDING';

-- Kafka consumer idempotency: one row per handled (topic:partition:offset).
CREATE TABLE processed_events (
    event_id     TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

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
DROP TABLE processed_events;
DROP TABLE payouts;
