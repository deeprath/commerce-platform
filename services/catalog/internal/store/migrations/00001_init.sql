-- +goose Up
CREATE TABLE products (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug         TEXT NOT NULL UNIQUE,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    category_id  TEXT NOT NULL DEFAULT '',
    price_currency CHAR(3) NOT NULL,
    price_units  BIGINT NOT NULL DEFAULT 0,
    price_nanos  INTEGER NOT NULL DEFAULT 0,
    media_keys   TEXT[] NOT NULL DEFAULT '{}',
    status       TEXT NOT NULL DEFAULT 'DRAFT'
                 CHECK (status IN ('DRAFT', 'ACTIVE', 'ARCHIVED')),
    attributes   JSONB NOT NULL DEFAULT '{}',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX products_status_created_at_idx ON products (status, created_at DESC);
CREATE INDEX products_category_idx ON products (category_id) WHERE status = 'ACTIVE';
CREATE INDEX products_attributes_gin ON products USING GIN (attributes);

-- Transactional outbox: written in the same tx as a product change; the
-- pkg/kafka relay publishes rows to Kafka and stamps published_at.
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
DROP TABLE products;
