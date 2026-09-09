-- +goose Up
CREATE TABLE orders (
    id                UUID PRIMARY KEY,
    owner_id          TEXT NOT NULL,
    status            TEXT NOT NULL
                      CHECK (status IN ('PENDING_PAYMENT', 'CONFIRMED', 'CANCELLED', 'FULFILLED')),
    currency          CHAR(3) NOT NULL,
    subtotal_cents    BIGINT NOT NULL,
    discount_cents    BIGINT NOT NULL DEFAULT 0,
    tax_cents         BIGINT NOT NULL DEFAULT 0,
    total_cents       BIGINT NOT NULL,
    ship_to           JSONB NOT NULL DEFAULT '{}',
    cart_id           TEXT NOT NULL DEFAULT '',
    coupon_code       TEXT NOT NULL DEFAULT '',
    payment_id        TEXT NOT NULL DEFAULT '',
    reservation_id    TEXT NOT NULL DEFAULT '',
    pricing_signature TEXT NOT NULL DEFAULT '',
    cancel_reason     TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX orders_owner_created_idx ON orders (owner_id, created_at DESC);
CREATE INDEX orders_payment_id_idx ON orders (payment_id) WHERE payment_id <> '';
CREATE INDEX orders_reservation_id_idx ON orders (reservation_id) WHERE reservation_id <> '';

CREATE TABLE order_lines (
    order_id         UUID NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    product_id       TEXT NOT NULL,
    title            TEXT NOT NULL DEFAULT '',
    quantity         INTEGER NOT NULL,
    unit_price_cents BIGINT NOT NULL,
    line_total_cents BIGINT NOT NULL,
    PRIMARY KEY (order_id, product_id)
);

-- Consumer idempotency: an event id is inserted once; a duplicate delivery is
-- detected by the unique violation and skipped.
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
DROP TABLE order_lines;
DROP TABLE orders;
