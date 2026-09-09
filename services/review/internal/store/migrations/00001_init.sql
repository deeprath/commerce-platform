-- +goose Up

-- Which (customer, product) pairs may be reviewed. Populated from
-- commerce.order.confirmed.
CREATE TABLE purchases (
    owner_id     TEXT NOT NULL,
    product_id   TEXT NOT NULL,
    first_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, product_id)
);

CREATE TABLE reviews (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id        TEXT NOT NULL,
    author_id         TEXT NOT NULL,
    author_name       TEXT NOT NULL DEFAULT 'Customer',
    rating            SMALLINT NOT NULL CHECK (rating BETWEEN 1 AND 5),
    title             TEXT NOT NULL DEFAULT '',
    body              TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'PUBLISHED'
                      CHECK (status IN ('PUBLISHED', 'HIDDEN')),
    verified_purchase BOOLEAN NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (product_id, author_id) -- one review per customer per product
);
CREATE INDEX reviews_product_published_idx
    ON reviews (product_id, created_at DESC) WHERE status = 'PUBLISHED';

-- Kafka consumer idempotency.
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
DROP TABLE reviews;
DROP TABLE purchases;
