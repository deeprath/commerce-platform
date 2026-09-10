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

func TestSink_AddClickBuffersUntilBatchSize(t *testing.T) {
	s := &Sink{batchSize: 3}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.AddClick(ctx, Click{Type: "page_view"}); err != nil {
			t.Fatalf("addclick %d: %v", i, err)
		}
	}
	if len(s.clickBuf) != 2 {
		t.Fatalf("clickBuf = %d, want 2 (no flush before batchSize)", len(s.clickBuf))
	}
	if len(s.buf) != 0 {
		t.Fatalf("funnel buffer touched by AddClick: %d", len(s.buf))
	}
}

func TestSink_FlushClicksEmptyIsNoop(t *testing.T) {
	s := &Sink{batchSize: 10} // nil conn — must not be dereferenced
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("empty flush over both buffers: %v", err)
	}
}

func TestSink_RequeueClicksPrependsForRetry(t *testing.T) {
	s := &Sink{batchSize: 100}
	s.clickBuf = []Click{{Type: "new"}}
	s.requeueClicks([]Click{{Type: "failed-1"}, {Type: "failed-2"}})

	if got := []string{s.clickBuf[0].Type, s.clickBuf[1].Type, s.clickBuf[2].Type}; got[0] != "failed-1" || got[1] != "failed-2" || got[2] != "new" {
		t.Fatalf("requeueClicks order wrong: %+v", s.clickBuf)
	}
}
