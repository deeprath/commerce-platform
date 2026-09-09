-- +goose Up
CREATE TABLE notifications (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id   TEXT NOT NULL,
    kind       TEXT NOT NULL,
    channel    TEXT NOT NULL DEFAULT 'EMAIL'
               CHECK (channel IN ('EMAIL', 'SMS', 'PUSH')),
    status     TEXT NOT NULL DEFAULT 'SENT'
               CHECK (status IN ('SENT', 'FAILED')),
    subject    TEXT NOT NULL DEFAULT '',
    body       TEXT NOT NULL DEFAULT '',
    ref_id     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX notifications_owner_created_idx ON notifications (owner_id, created_at DESC);

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
DROP TABLE notifications;
