-- +goose Up
CREATE TABLE stock (
    product_id TEXT PRIMARY KEY,
    on_hand    INTEGER NOT NULL DEFAULT 0 CHECK (on_hand >= 0),
    reserved   INTEGER NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE reservations (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_ref  TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'HELD' CHECK (status IN ('HELD', 'COMMITTED', 'RELEASED')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX reservations_sweep_idx ON reservations (expires_at) WHERE status = 'HELD';
CREATE INDEX reservations_order_ref_idx ON reservations (order_ref);

CREATE TABLE reservation_items (
    reservation_id UUID NOT NULL REFERENCES reservations (id) ON DELETE CASCADE,
    product_id     TEXT NOT NULL,
    quantity       INTEGER NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (reservation_id, product_id)
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
DROP TABLE reservation_items;
DROP TABLE reservations;
DROP TABLE stock;
