-- +goose Up
-- A payout can be reduced by an approved return. Reversals accumulate, so the
-- amount is a running total rather than a flag: a payout is only REVERSED once
-- reversed_cents has reached amount_cents.
ALTER TABLE payouts ADD COLUMN reversed_cents BIGINT NOT NULL DEFAULT 0
      CHECK (reversed_cents >= 0);
ALTER TABLE payouts ADD COLUMN reversed_at TIMESTAMPTZ;

-- Never reverse more than was owed. The domain clamps at the outstanding
-- amount; this is the backstop for anything that writes the column directly.
ALTER TABLE payouts ADD CONSTRAINT payouts_reversed_within_amount
      CHECK (reversed_cents <= amount_cents);

ALTER TABLE payouts DROP CONSTRAINT payouts_status_check;
ALTER TABLE payouts ADD CONSTRAINT payouts_status_check
      CHECK (status IN ('PENDING', 'PAID', 'REVERSED'));

-- +goose Down
ALTER TABLE payouts DROP CONSTRAINT payouts_status_check;
ALTER TABLE payouts ADD CONSTRAINT payouts_status_check
      CHECK (status IN ('PENDING', 'PAID'));
ALTER TABLE payouts DROP CONSTRAINT payouts_reversed_within_amount;
ALTER TABLE payouts DROP COLUMN reversed_at;
ALTER TABLE payouts DROP COLUMN reversed_cents;
