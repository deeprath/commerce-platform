CREATE TABLE IF NOT EXISTS clickstream (
  event_type   LowCardinality(String),
  occurred_at  DateTime64(3, 'UTC'),
  client_time  DateTime64(3, 'UTC'),
  anonymous_id String,
  session_id   String,
  owner_id     String DEFAULT '',
  path         String DEFAULT '',
  referrer     String DEFAULT '',
  product_id   String DEFAULT '',
  query        String DEFAULT '',
  value_minor  Int64  DEFAULT 0,
  currency     LowCardinality(String) DEFAULT '',
  user_agent   String DEFAULT '',
  ingested_at  DateTime64(3, 'UTC') DEFAULT now64(3)
) ENGINE = MergeTree
PARTITION BY toYYYYMM(occurred_at)
ORDER BY (event_type, occurred_at, anonymous_id)
TTL toDateTime(occurred_at) + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS clickstream_daily (
  day         Date,
  event_type  LowCardinality(String),
  events      UInt64,
  value_minor Int64
) ENGINE = SummingMergeTree
ORDER BY (day, event_type);

CREATE MATERIALIZED VIEW IF NOT EXISTS clickstream_daily_mv TO clickstream_daily AS
SELECT
  toDate(occurred_at) AS day,
  event_type,
  count()          AS events,
  sum(value_minor) AS value_minor
FROM clickstream
GROUP BY day, event_type;
