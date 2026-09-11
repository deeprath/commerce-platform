-- +goose Up
ALTER TABLE shipments ADD COLUMN shop_id TEXT NOT NULL DEFAULT '';

-- One shipment per order was a v1 simplification; an order's lines are now
-- grouped by shop into one shipment per distinct shop_id (see
-- CreateFromOrder), so the uniqueness moves to the (order, shop) pair.
ALTER TABLE shipments DROP CONSTRAINT shipments_order_id_key;
ALTER TABLE shipments ADD CONSTRAINT shipments_order_shop_key UNIQUE (order_id, shop_id);

-- +goose Down
ALTER TABLE shipments DROP CONSTRAINT shipments_order_shop_key;
ALTER TABLE shipments ADD CONSTRAINT shipments_order_id_key UNIQUE (order_id);
ALTER TABLE shipments DROP COLUMN shop_id;
