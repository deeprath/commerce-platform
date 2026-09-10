-- +goose Up
-- Marketplace: a product may belong to a seller's shop. NULL => first-party
-- (platform-owned). Authorization for seller writes is enforced in the service
-- via OpenFGA `shop#staff` (ADR-040); this column is just the link.
ALTER TABLE products ADD COLUMN shop_id UUID;
CREATE INDEX products_shop_idx ON products (shop_id) WHERE shop_id IS NOT NULL;

-- +goose Down
DROP INDEX products_shop_idx;
ALTER TABLE products DROP COLUMN shop_id;
