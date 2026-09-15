-- Indexes for the operator-facing list paths.
--
-- List()/ListReturns() take owner_id and status as *optional* filters, so four
-- query shapes reach Postgres. Until now only the owner-scoped one was indexed
-- (orders_owner_created_idx), which a composite on (owner_id, created_at) can
-- only serve when owner_id is actually constrained. The admin views — "every
-- order", and "every order in this status" — had no usable index at all and
-- fell back to a sequential scan plus a sort, on the fastest-growing tables in
-- the system.
--
-- Paired with the switch away from `($1 = '' OR col = $1)` in the same change:
-- that idiom made one plan serve every shape, so these indexes would have been
-- unusable regardless of whether they existed.

-- +goose Up
CREATE INDEX orders_created_idx ON orders (created_at DESC);
CREATE INDEX orders_status_created_idx ON orders (status, created_at DESC);
CREATE INDEX returns_created_idx ON returns (created_at DESC);
CREATE INDEX returns_status_created_idx ON returns (status, created_at DESC);

-- +goose Down
DROP INDEX returns_status_created_idx;
DROP INDEX returns_created_idx;
DROP INDEX orders_status_created_idx;
DROP INDEX orders_created_idx;
