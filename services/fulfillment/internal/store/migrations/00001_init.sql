-- +goose Up
CREATE TABLE shipments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id        TEXT NOT NULL UNIQUE, -- v1: exactly one shipment per order
    owner_id        TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'PENDING'
                    CHECK (status IN ('PENDING', 'SHIPPED', 'DELIVERED', 'CANCELLED')),
    carrier         TEXT NOT NULL DEFAULT '',
    tracking_number TEXT NOT NULL DEFAULT '',
    ship_to         JSONB NOT NULL DEFAULT '{}',
    items           JSONB NOT NULL DEFAULT '[]',
    cancel_reason   TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    shipped_at      TIMESTAMPTZ,
    delivered_at    TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX shipments_owner_created_idx ON shipments (owner_id, created_at DESC);
-- The sandbox carrier advancer sweeps by (status, timestamp).
CREATE INDEX shipments_pending_idx ON shipments (created_at) WHERE status = 'PENDING';
CREATE INDEX shipments_shipped_idx ON shipments (shipped_at) WHERE status = 'SHIPPED';

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
DROP TABLE shipments;
