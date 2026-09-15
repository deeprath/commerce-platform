-- Index for the unfiltered shop listing.
--
-- ListShops takes status as an optional filter. shops_status_created_idx serves
-- the filtered shape, but a composite on (status, created_at) cannot order by
-- created_at alone — so browsing every shop, which is a public path via
-- GET /seller/shops, had no usable index.

-- +goose Up
CREATE INDEX shops_created_idx ON shops (created_at DESC);

-- +goose Down
DROP INDEX shops_created_idx;
