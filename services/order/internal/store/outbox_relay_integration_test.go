package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/deeprath/commerce-platform/pkg/kafka"
)

// countingPublisher stands in for a broker: the relay only needs Publish, so
// this exercises the real SQL against a real outbox table without one.
type countingPublisher struct {
	mu     sync.Mutex
	topics []string
	failAt int // 1-based index to fail on; 0 never fails
	err    error
}

func (p *countingPublisher) Publish(_ context.Context, topic string, _, _ []byte, _ map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failAt > 0 && len(p.topics)+1 == p.failAt {
		return p.err
	}
	p.topics = append(p.topics, topic)
	return nil
}

func (p *countingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.topics)
}

// A tick must drain the whole backlog, not one batch of it. With one drain per
// tick the relay was capped at batch/interval — 4 ticks to clear this backlog —
// so a deadline shorter than that only passes if a single tick drained it all.
func TestOutboxRelay_OneTickDrainsTheWholeBacklog(t *testing.T) {
	const (
		rows     = 200
		batch    = 50 // 4 batches
		interval = 500 * time.Millisecond
	)
	pool := spinUp(t)
	ctx := context.Background()
	for i := 0; i < rows; i++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`,
			"commerce.order.created", []byte("k"), []byte("payload")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	pub := &countingPublisher{}
	relay := kafka.NewOutboxRelay(pool, pub, interval, batch)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = relay.Run(runCtx) }()

	// One tick lands at ~500ms. Four ticks — what the old one-batch-per-tick
	// relay needed — would not finish until ~2s.
	deadline := time.After(1200 * time.Millisecond)
	for pub.count() < rows {
		select {
		case <-deadline:
			t.Fatalf("relayed %d/%d rows before the deadline — the relay is still capped at one batch per tick",
				pub.count(), rows)
		case <-time.After(20 * time.Millisecond):
		}
	}

	cancel()
	pending, _, err := relay.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("Pending() = %d, want 0 — published rows were not marked", pending)
	}
}

// Publishing stops at the first failure and everything from there stays
// unpublished, so retries resume in id order and two events for the same key
// cannot swap places.
func TestOutboxRelay_StopsAtFirstFailureAndKeepsOrder(t *testing.T) {
	pool := spinUp(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`,
			"commerce.order.created", []byte("same-key"), []byte("payload")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	pub := &countingPublisher{failAt: 4, err: context.DeadlineExceeded}
	relay := kafka.NewOutboxRelay(pool, pub, 50*time.Millisecond, 100)

	runCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	_ = relay.Run(runCtx)

	pending, oldest, err := relay.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	// The three before the failure are published and marked; the failing row
	// and everything after it are still waiting.
	if pending != 7 {
		t.Fatalf("Pending() = %d, want 7 (rows from the failure onward stay unpublished)", pending)
	}
	if oldest <= 0 {
		t.Fatalf("Pending() oldest = %v, want the age of the stuck backlog", oldest)
	}
}

// An empty outbox must not report a backlog, and must not wedge the relay.
func TestOutboxRelay_PendingIsZeroWhenDrained(t *testing.T) {
	pool := spinUp(t)
	relay := kafka.NewOutboxRelay(pool, &countingPublisher{}, time.Second, 100)

	count, oldest, err := relay.Pending(context.Background())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if count != 0 || oldest != 0 {
		t.Fatalf("Pending() = (%d, %v), want (0, 0) on an empty outbox", count, oldest)
	}
}
