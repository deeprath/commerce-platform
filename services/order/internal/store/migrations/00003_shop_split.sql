-- +goose Up
ALTER TABLE order_lines ADD COLUMN shop_id TEXT NOT NULL DEFAULT '';

-- Tracks which of an order's shop groups (see domain.Order.ShopGroups) have
-- had their shipment delivered, so the order can wait for every group before
-- moving CONFIRMED -> FULFILLED. One row per (order, shop) once that shop's
-- shipment delivers; a redelivered fulfillment.delivered event for the same
-- shop is a no-op insert.
CREATE TABLE order_shipment_deliveries (
    order_id     UUID NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    shop_id      TEXT NOT NULL,
    delivered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (order_id, shop_id)
);

-- +goose Down
DROP TABLE order_shipment_deliveries;
ALTER TABLE order_lines DROP COLUMN shop_id;
