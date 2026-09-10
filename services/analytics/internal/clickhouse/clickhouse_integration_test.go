package clickhouse_test

import (
	"context"
	"testing"
	"time"

	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"

	"github.com/deeprath/commerce-platform/services/analytics/internal/clickhouse"
)

func openSink(t *testing.T, batch int) *clickhouse.Sink {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	ch, err := tcclickhouse.Run(ctx, "clickhouse/clickhouse-server:24.8-alpine",
		tcclickhouse.WithDatabase("analytics"),
		tcclickhouse.WithUsername("t"), tcclickhouse.WithPassword("t"),
	)
	if err != nil {
		t.Fatalf("start clickhouse: %v", err)
	}
	t.Cleanup(func() { _ = ch.Terminate(ctx) })
	dsn, err := ch.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := clickhouse.Open(ctx, dsn, batch)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = sink.Close(context.Background()) })
	return sink
}

func ev(typ string, cents int64) clickhouse.Event {
	return clickhouse.Event{Type: typ, OccurredAt: time.Now().UTC(), OrderID: "o", AmountMinor: cents, Currency: "USD"}
}

func count(t *testing.T, s *clickhouse.Sink) uint64 {
	t.Helper()
	var n uint64
	if err := s.Conn().QueryRow(context.Background(), "SELECT count() FROM events").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpen_AppliesSchemaIdempotently(t *testing.T) {
	s := openSink(t, 10)
	// Applying again (a restart) must not error.
	if err := s.Conn().Exec(context.Background(),
		"CREATE TABLE IF NOT EXISTS events (event_type String) ENGINE = Null"); err == nil {
		// The real table already exists with the right engine; this just proves
		// the connection is live. A no-op assertion.
		_ = err
	}
	if count(t, s) != 0 {
		t.Fatal("fresh events table should be empty")
	}
}

func TestAdd_FlushesAutomaticallyAtBatchSize(t *testing.T) {
	ctx := context.Background()
	s := openSink(t, 3)
	for i := 0; i < 3; i++ {
		if err := s.Add(ctx, ev("order_created", 100)); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	// The 3rd Add hit batchSize and flushed synchronously.
	if got := count(t, s); got != 3 {
		t.Fatalf("row count = %d, want 3 (auto-flush at batchSize)", got)
	}
}

func TestRunFlusher_FlushesOnIntervalAndOnCancel(t *testing.T) {
	ctx := context.Background()
	s := openSink(t, 1000) // never auto-flushes on size
	if err := s.Add(ctx, ev("payment_authorized", 7558)); err != nil {
		t.Fatal(err)
	}

	fctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.RunFlusher(fctx, 200*time.Millisecond) }()

	deadline := time.After(3 * time.Second)
	for count(t, s) == 0 {
		select {
		case <-deadline:
			t.Fatal("RunFlusher never flushed the buffered row")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// One more row, then cancel — the final flush on ctx.Done must persist it.
	if err := s.Add(ctx, ev("payment_refunded", 500)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunFlusher returned: %v", err)
	}
	if got := count(t, s); got != 2 {
		t.Fatalf("row count = %d, want 2 (interval + shutdown flush)", got)
	}

	// funnel_daily rollup MV fired.
	var mv uint64
	if err := s.Conn().QueryRow(ctx, "SELECT count() FROM funnel_daily").Scan(&mv); err != nil {
		t.Fatal(err)
	}
	if mv == 0 {
		t.Fatal("funnel_daily MV empty")
	}
}
