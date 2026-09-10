-- +goose Up

CREATE TABLE shops (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id          TEXT NOT NULL UNIQUE,          -- one shop per user (v1)
    name              TEXT NOT NULL,
    slug              TEXT NOT NULL UNIQUE,
    description       TEXT NOT NULL DEFAULT '',
    contact_email     TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'PENDING_REVIEW'
                      CHECK (status IN ('PENDING_REVIEW', 'ACTIVE', 'SUSPENDED')),
    suspension_reason TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX shops_status_created_idx ON shops (status, created_at DESC);

-- Transactional outbox for commerce.shop.* events.
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
DROP TABLE shops;
