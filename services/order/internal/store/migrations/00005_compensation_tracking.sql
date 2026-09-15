-- Track whether a cancelled order's compensations actually completed.
--
-- The saga compensates by calling Inventory.Release and Payment.Void, logging
-- and moving on if either fails. A failed Release self-heals — inventory expires
-- the reservation on its own TTL sweep. A failed Void does not: the shopper's
-- authorisation stays live on an order that no longer exists, with nothing
-- retrying it and no way to find it afterwards, because the attempt left no
-- trace beyond a log line.
--
-- Recording when each compensation succeeded turns that into a queryable set,
-- which the reconciler sweeps and retries. Both RPCs are idempotent, so retrying
-- one that actually did succeed is safe.

-- +goose Up
ALTER TABLE orders
    ADD COLUMN reservation_released_at TIMESTAMPTZ,
    ADD COLUMN payment_voided_at       TIMESTAMPTZ;

-- Existing cancelled orders are backfilled as handled. We have no evidence
-- either way for them, and re-voiding every historical cancellation the moment
-- this deploys is a worse risk than the unknown — their reservations have long
-- since expired regardless. Tracking is accurate from here forward.
UPDATE orders
   SET reservation_released_at = updated_at,
       payment_voided_at       = updated_at
 WHERE status = 'CANCELLED';

-- Drives the reconciler's sweep: cancelled orders still missing a compensation
-- they actually owe. An order cancelled before it ever reserved or paid owes
-- nothing, so the id checks keep it out of the index entirely rather than
-- leaving it to match on every sweep forever.
CREATE INDEX orders_pending_compensation_idx
    ON orders (updated_at)
 WHERE status = 'CANCELLED'
   AND ((reservation_released_at IS NULL AND reservation_id <> '')
     OR (payment_voided_at       IS NULL AND payment_id     <> ''));

-- +goose Down
DROP INDEX orders_pending_compensation_idx;
ALTER TABLE orders
    DROP COLUMN payment_voided_at,
    DROP COLUMN reservation_released_at;
