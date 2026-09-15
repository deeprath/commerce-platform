-- Indexes for the payout list paths that had none.
--
-- List() takes shop_id and status as optional filters. The shop-scoped shape
-- was indexed and PENDING has a partial index, but the operator views — every
-- payout, and every payout in a status other than PENDING (PAID, FAILED) — had
-- no usable index and fell back to a sequential scan plus a sort.

-- +goose Up
CREATE INDEX payouts_created_idx ON payouts (created_at DESC);
CREATE INDEX payouts_status_created_idx ON payouts (status, created_at DESC);

-- +goose Down
DROP INDEX payouts_status_created_idx;
DROP INDEX payouts_created_idx;
