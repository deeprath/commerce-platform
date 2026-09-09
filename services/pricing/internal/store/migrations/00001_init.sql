-- +goose Up
CREATE TABLE coupons (
    code         TEXT PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('PERCENT', 'AMOUNT')),
    percent_off  INTEGER NOT NULL DEFAULT 0,
    amount_cents BIGINT NOT NULL DEFAULT 0,
    currency     CHAR(3) NOT NULL DEFAULT 'USD',
    expires_at   TIMESTAMPTZ,
    max_uses     BIGINT NOT NULL DEFAULT 0,   -- 0 = unlimited
    used_count   BIGINT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Local dev seed.
INSERT INTO coupons (code, kind, percent_off, expires_at) VALUES
  ('SAVE10', 'PERCENT', 10, now() + interval '365 days');
INSERT INTO coupons (code, kind, amount_cents, expires_at) VALUES
  ('FIVEOFF', 'AMOUNT', 500, now() + interval '365 days');
INSERT INTO coupons (code, kind, percent_off, expires_at) VALUES
  ('EXPIRED', 'PERCENT', 50, now() - interval '1 day');

-- +goose Down
DROP TABLE coupons;
