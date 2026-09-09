package kafka

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxRelay polls a service's `outbox` table and publishes rows to Kafka,
// then marks them sent. Services write domain rows + an outbox row in ONE
// Postgres transaction, so there is no dual-write and no lost events.
//
// Expected table (created by each service's migrations):
//
//	CREATE TABLE outbox (
//	  id           BIGGENERATED ALWAYS AS IDENTITY PRIMARY KEY,
//	  topic        TEXT NOT NULL,
//	  key          BYTEA,
//	  payload      BYTEA NOT NULL,
//	  headers      JSONB NOT NULL DEFAULT '{}',
//	  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
//	  published_at TIMESTAMPTZ
//	);
type OutboxRelay struct {
	pool     *pgxpool.Pool
	producer *Producer
	interval time.Duration
	batch    int
}

// NewOutboxRelay wires a relay. interval<=0 defaults to 1s; batch<=0 defaults to 100.
func NewOutboxRelay(pool *pgxpool.Pool, p *Producer, interval time.Duration, batch int) *OutboxRelay {
	if interval <= 0 {
		interval = time.Second
	}
	if batch <= 0 {
		batch = 100
	}
	return &OutboxRelay{pool: pool, producer: p, interval: interval, batch: batch}
}

// Run relays until ctx is cancelled.
func (r *OutboxRelay) Run(ctx context.Context) error {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n, err := r.drainOnce(ctx); err != nil {
				slog.ErrorContext(ctx, "outbox relay error", slog.Any("err", err))
			} else if n > 0 {
				slog.DebugContext(ctx, "outbox relayed", slog.Int("count", n))
			}
		}
	}
}

func (r *OutboxRelay) drainOnce(ctx context.Context) (int, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, topic, key, payload FROM outbox
		 WHERE published_at IS NULL
		 ORDER BY id
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`, r.batch)
	if err != nil {
		return 0, err
	}
	type row struct {
		id      int64
		topic   string
		key     []byte
		payload []byte
	}
	var pending []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.topic, &x.key, &x.payload); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var sent int
	for _, x := range pending {
		if err := r.producer.Publish(ctx, x.topic, x.key, x.payload, nil); err != nil {
			return sent, err // stop on first failure; next tick retries from here
		}
		if _, err := r.pool.Exec(ctx,
			`UPDATE outbox SET published_at = now() WHERE id = $1`, x.id); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}
