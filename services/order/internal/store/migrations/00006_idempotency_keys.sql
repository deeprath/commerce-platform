-- Makes checkout safe to retry.
--
-- CreateOrder had no idempotency at all. Its only protection was accidental:
-- a successful checkout clears the cart, so a *sequential* retry hits
-- CART_EMPTY. That breaks down twice over — the clear is best-effort and logged
-- on failure, and two concurrent submits both read a full cart before either
-- clears. Either way the customer gets two orders, stock is reserved twice, and
-- their card is authorised twice.
--
-- The key is claimed before any downstream work, so the claim itself is what
-- serialises concurrent attempts; see store.ClaimIdempotencyKey.

-- +goose Up
CREATE TABLE idempotency_keys (
    key         TEXT PRIMARY KEY,
    owner_id    TEXT NOT NULL,
    -- Hash of the request the key was first used for. A second request with the
    -- same key but a different body is a client bug, not a retry, and must be
    -- rejected rather than silently handed someone else's order.
    fingerprint TEXT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('IN_FLIGHT', 'COMPLETED')),
    -- Set when the order commits, in the same transaction, so a crash can never
    -- leave a completed order behind an unclaimed key.
    order_id    UUID REFERENCES orders (id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Drives the reaper, and the recovery of keys abandoned mid-flight by a crash
-- between claiming and committing.
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);
CREATE INDEX idempotency_keys_in_flight_idx
    ON idempotency_keys (created_at) WHERE status = 'IN_FLIGHT';

-- +goose Down
DROP TABLE idempotency_keys;
