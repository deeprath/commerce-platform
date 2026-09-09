-- +goose Up

-- Cumulative amount refunded against a payment. status moves to REFUNDED only
-- when refunded_cents reaches amount_cents; partial refunds keep it AUTHORIZED.
ALTER TABLE payments ADD COLUMN refunded_cents BIGINT NOT NULL DEFAULT 0;

-- One row per refund, for idempotency and an audit trail.
CREATE TABLE refunds (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    payment_id      UUID NOT NULL REFERENCES payments (id),
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    idempotency_key TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX refunds_idem_idx ON refunds (payment_id, idempotency_key)
    WHERE idempotency_key <> '';
CREATE INDEX refunds_payment_idx ON refunds (payment_id);

-- +goose Down
DROP TABLE refunds;
ALTER TABLE payments DROP COLUMN refunded_cents;
