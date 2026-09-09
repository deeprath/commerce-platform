-- +goose Up
CREATE TABLE returns (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id       UUID NOT NULL REFERENCES orders (id),
    owner_id       TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'REQUESTED'
                   CHECK (status IN ('REQUESTED', 'APPROVED', 'REJECTED')),
    reason         TEXT NOT NULL DEFAULT '',
    currency       CHAR(3) NOT NULL,
    refund_total_cents BIGINT NOT NULL DEFAULT 0,
    decided_by     TEXT NOT NULL DEFAULT '',
    decision_note  TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX returns_owner_created_idx ON returns (owner_id, created_at DESC);
CREATE INDEX returns_order_idx ON returns (order_id);

CREATE TABLE return_lines (
    return_id          UUID NOT NULL REFERENCES returns (id) ON DELETE CASCADE,
    product_id         TEXT NOT NULL,
    quantity           INTEGER NOT NULL CHECK (quantity > 0),
    refund_amount_cents BIGINT NOT NULL,
    -- set once the units have been added back to inventory, so a retried
    -- approval does not restock twice.
    restocked          BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (return_id, product_id)
);

-- +goose Down
DROP TABLE return_lines;
DROP TABLE returns;
