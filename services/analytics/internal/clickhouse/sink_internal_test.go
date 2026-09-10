package clickhouse

import (
	"context"
	"testing"
)

// Buffer behaviour that doesn't touch the connection. The full-batch flush and
// the schema/insert paths are covered by the testcontainers integration test.

func TestSink_AddBuffersUntilBatchSize(t *testing.T) {
	s := &Sink{batchSize: 3}
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := s.Add(ctx, Event{Type: "order_created"}); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if len(s.buf) != 2 {
		t.Fatalf("buffered %d, want 2 (no flush before batchSize)", len(s.buf))
	}
}

func TestSink_FlushEmptyIsNoop(t *testing.T) {
	s := &Sink{batchSize: 10} // nil conn — must not be dereferenced
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("empty flush: %v", err)
	}
}

func TestSink_RequeuePrependsForRetry(t *testing.T) {
	s := &Sink{batchSize: 100}
	s.buf = []Event{{Type: "new"}}
	s.requeue([]Event{{Type: "failed-1"}, {Type: "failed-2"}})

	if len(s.buf) != 3 {
		t.Fatalf("len = %d, want 3", len(s.buf))
	}
	if s.buf[0].Type != "failed-1" || s.buf[1].Type != "failed-2" || s.buf[2].Type != "new" {
		t.Fatalf("requeue order wrong: %+v", s.buf)
	}
}
