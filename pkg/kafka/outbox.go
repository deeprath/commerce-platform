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
	producer publisher
	interval time.Duration
	batch    int
}

// publisher is the slice of *Producer the relay actually uses. Narrowing it to
// an interface lets the relay be driven against a real outbox table without a
// broker — the same reasoning as media's objectStore and search's searcher.
// *Producer satisfies it structurally, so call sites are unchanged.
type publisher interface {
	Publish(ctx context.Context, topic string, key, value []byte, headers map[string]string) error
}

// NewOutboxRelay wires a relay. interval<=0 defaults to 1s; batch<=0 defaults to 100.
func NewOutboxRelay(pool *pgxpool.Pool, p publisher, interval time.Duration, batch int) *OutboxRelay {
	if interval <= 0 {
		interval = time.Second
	}
	if batch <= 0 {
		batch = 100
	}
	return &OutboxRelay{pool: pool, producer: p, interval: interval, batch: batch}
}

// Run relays until ctx is cancelled.
//
// Each tick drains repeatedly rather than once. A single drain per tick capped
// throughput at batch/interval — 100 rows per second on the defaults — so any
// burst above that built a backlog the relay could never catch up on, however
// idle the service went afterwards. Draining until the table is empty means the
// tick only sets how soon an *idle* relay notices new work; it no longer caps
// how fast a busy one gets through it.
func (r *OutboxRelay) Run(ctx context.Context) error {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			r.drainUntilEmpty(ctx)
		}
	}
}

// drainUntilEmpty keeps draining while each pass comes back full, which means
// rows were still waiting. A short pass means the table is drained and the
// relay can go back to sleep.
func (r *OutboxRelay) drainUntilEmpty(ctx context.Context) {
	for passes := 0; ; passes++ {
		if ctx.Err() != nil {
			return
		}
		n, err := r.drainOnce(ctx)
		if err != nil {
			// Publishing stops at the first failure and the rows stay
			// unpublished, so the next pass or tick retries from the same
			// point. Give up on this tick rather than spinning against a
			// broker that is currently refusing writes.
			slog.ErrorContext(ctx, "outbox relay error",
				slog.Any("err", err), slog.Int("relayed_before_error", n))
			return
		}
		if n > 0 {
			slog.DebugContext(ctx, "outbox relayed", slog.Int("count", n), slog.Int("pass", passes))
		}
		if n < r.batch {
			return // short pass — nothing left waiting
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

	// Publish in id order, stopping at the first failure: rows are keyed per
	// aggregate, so skipping past a failure to publish a later row could
	// reorder two events for the same key. Everything from the failure on stays
	// unpublished and is retried from the same point.
	//
	// Publishing stays one synchronous record at a time, which is now the
	// throughput bound (an ack round trip each). Sending the batch in one
	// ProduceSync would be faster but needs the ordering guarantees analysing
	// against partial failure before it would be safe here.
	published := make([]int64, 0, len(pending))
	var pubErr error
	for _, x := range pending {
		if err := r.producer.Publish(ctx, x.topic, x.key, x.payload, nil); err != nil {
			pubErr = err
			break
		}
		published = append(published, x.id)
	}

	// Mark the published prefix in one statement rather than one per row. The
	// window between publishing and marking is wider as a result: a crash in it
	// republishes this batch instead of a single row. That is within the
	// at-least-once contract every consumer here already dedupes against, and
	// it removes a round trip per event.
	if len(published) > 0 {
		if _, err := r.pool.Exec(ctx,
			`UPDATE outbox SET published_at = now() WHERE id = ANY($1)`, published); err != nil {
			return len(published), err
		}
	}
	return len(published), pubErr
}

// Pending reports how many rows are waiting and how old the oldest is — the
// relay's backlog, for a health endpoint or an operator poking at a service
// that seems to have stopped emitting events.
func (r *OutboxRelay) Pending(ctx context.Context) (count int64, oldest time.Duration, err error) {
	var oldestAt *time.Time
	err = r.pool.QueryRow(ctx, `
		SELECT count(*), min(created_at) FROM outbox WHERE published_at IS NULL`,
	).Scan(&count, &oldestAt)
	if err != nil {
		return 0, 0, err
	}
	if oldestAt != nil {
		oldest = time.Since(*oldestAt)
	}
	return count, oldest, nil
}
