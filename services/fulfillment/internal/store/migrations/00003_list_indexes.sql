-- Indexes for the shipment list paths that had none.
--
-- List() takes owner_id, order_id and status as optional filters. Only the
-- owner-scoped shape was indexed; the rest fell back to a sequential scan:
--
--   * order_id had no index at all, despite being a filter on the RPC
--     (ListShipments.order_id) — "the shipments for this order" scanned the
--     whole table.
--   * the operator view with no owner filter had nothing to order by.
--   * the two existing partial indexes cover only PENDING and SHIPPED, so
--     filtering by DELIVERED or CANCELLED was unindexed.

-- +goose Up
CREATE INDEX shipments_order_idx ON shipments (order_id);
CREATE INDEX shipments_created_idx ON shipments (created_at DESC);
CREATE INDEX shipments_status_created_idx ON shipments (status, created_at DESC);

-- +goose Down
DROP INDEX shipments_status_created_idx;
DROP INDEX shipments_created_idx;
DROP INDEX shipments_order_idx;
