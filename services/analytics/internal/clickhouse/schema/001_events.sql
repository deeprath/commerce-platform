-- One wide append-only fact table for the checkout funnel + revenue analytics,
-- fed from the order.* / payment.* Kafka events. MergeTree, month-partitioned,
-- 90-day TTL. Analytical queries live in Grafana against this table and the
-- rollup below.
CREATE TABLE IF NOT EXISTS events
(
    event_type   LowCardinality(String),                 -- order_created | order_confirmed | order_cancelled | order_fulfilled | payment_authorized | payment_failed | payment_refunded
    occurred_at  DateTime64(3, 'UTC'),
    order_id     String,
    owner_id     String,
    payment_id   String DEFAULT '',
    amount_minor Int64  DEFAULT 0,                        -- money in minor units (cents)
    currency     LowCardinality(String) DEFAULT '',
    reason       String DEFAULT '',                       -- failure / cancellation reason
    ingested_at  DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(occurred_at)
ORDER BY (event_type, occurred_at, order_id)
TTL toDateTime(occurred_at) + INTERVAL 90 DAY;

-- Daily rollup: event counts + revenue per type. SummingMergeTree so the MV can
-- just insert partial aggregates and reads merge them.
CREATE TABLE IF NOT EXISTS funnel_daily
(
    day           Date,
    event_type    LowCardinality(String),
    events        UInt64,
    revenue_minor Int64
)
ENGINE = SummingMergeTree
ORDER BY (day, event_type);

CREATE MATERIALIZED VIEW IF NOT EXISTS funnel_daily_mv TO funnel_daily AS
SELECT
    toDate(occurred_at)                       AS day,
    event_type,
    count()                                   AS events,
    sum(amount_minor)                         AS revenue_minor
FROM events
GROUP BY day, event_type;
