package clickhouse

import (
	"context"
	"errors"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Buffer behaviour that doesn't touch the connection. The full-batch flush and
// the schema/insert paths are covered by the testcontainers integration test.
//
// Sinks here are built directly rather than through Open, so maxBuffer must be
// set explicitly — Open clamps it to at least batchSize, but the zero value
// would refuse every write.

func TestSink_AddBuffersUntilBatchSize(t *testing.T) {
	s := &Sink{batchSize: 3, maxBuffer: 100}
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
	s := &Sink{batchSize: 10, maxBuffer: 100} // nil conn — must not be dereferenced
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("empty flush: %v", err)
	}
}

func TestSink_AddClickBuffersUntilBatchSize(t *testing.T) {
	s := &Sink{batchSize: 3, maxBuffer: 100}
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
	s := &Sink{batchSize: 10, maxBuffer: 100} // nil conn — must not be dereferenced
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("empty flush over both buffers: %v", err)
	}
}

// The buffer is a bounded staging area, not a second copy of the Kafka log: at
// maxBuffer, Add refuses rather than growing the heap until the pod is killed.
func TestSink_AddAppliesBackpressureAtMaxBuffer(t *testing.T) {
	// batchSize above maxBuffer so the eager flush (which would touch the nil
	// conn) never triggers — this test is only about the admission check.
	s := &Sink{batchSize: 1000, maxBuffer: 2}
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := s.Add(ctx, Event{Type: "order_created"}); err != nil {
			t.Fatalf("add %d should fit: %v", i, err)
		}
	}
	err := s.Add(ctx, Event{Type: "one_too_many"})
	if !errors.Is(err, ErrBufferFull) {
		t.Fatalf("err = %v, want ErrBufferFull", err)
	}
	if len(s.buf) != 2 {
		t.Fatalf("buffer grew past maxBuffer: %d, want 2", len(s.buf))
	}

	refused, _, degraded := s.Stats()
	if refused != 1 || !degraded {
		t.Fatalf("Stats() = refused %d degraded %v, want 1 / true", refused, degraded)
	}
}

func TestSink_AddClickAppliesBackpressureAtMaxBuffer(t *testing.T) {
	s := &Sink{batchSize: 1000, maxBuffer: 1}
	ctx := context.Background()

	if err := s.AddClick(ctx, Click{Type: "page_view"}); err != nil {
		t.Fatalf("first click should fit: %v", err)
	}
	if err := s.AddClick(ctx, Click{Type: "page_view"}); !errors.Is(err, ErrBufferFull) {
		t.Fatalf("err = %v, want ErrBufferFull", err)
	}
	if len(s.clickBuf) != 1 {
		t.Fatalf("clickBuf grew past maxBuffer: %d, want 1", len(s.clickBuf))
	}
}

// Backpressure is a state, not a permanent verdict: a clean flush lifts it so
// the consumers resume committing offsets.
func TestSink_CleanFlushLiftsBackpressure(t *testing.T) {
	s := &Sink{batchSize: 10, maxBuffer: 100, degraded: true, refused: 7}

	if err := s.Flush(context.Background()); err != nil { // both buffers empty
		t.Fatalf("empty flush: %v", err)
	}
	if _, _, degraded := s.Stats(); degraded {
		t.Fatal("still degraded after a clean flush")
	}
}

// failingConn stubs the one method the flush path calls. The embedded nil
// interface satisfies the rest of driver.Conn — anything else would panic, which
// is the point: it proves the flush path touches nothing else.
type failingConn struct {
	driver.Conn
	err error
}

func (c failingConn) PrepareBatch(context.Context, string, ...driver.PrepareBatchOption) (driver.Batch, error) {
	return nil, c.err
}

// The regression this guards: the old path cleared the buffer up front and only
// put rows back if the send failed. A crash in that window lost them outright,
// and the window itself re-opened admission — which is how the buffer grew
// without bound. Rows must now survive a failed flush in place.
func TestSink_FailedFlushKeepsRowsBuffered(t *testing.T) {
	s := &Sink{
		batchSize: 10, maxBuffer: 100,
		conn: failingConn{err: errors.New("clickhouse unreachable")},
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.Add(ctx, Event{Type: "order_created"}); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}

	if err := s.Flush(ctx); err == nil {
		t.Fatal("Flush succeeded against a failing connection")
	}
	if len(s.buf) != 3 {
		t.Fatalf("buffered %d rows after a failed flush, want all 3 kept", len(s.buf))
	}
}

// The core fix: however long ClickHouse stays down, memory stays bounded.
func TestSink_SustainedOutageCannotGrowTheBufferPastMax(t *testing.T) {
	const maxBuffer = 4
	s := &Sink{
		batchSize: 2, maxBuffer: maxBuffer, // eager flush fires often, always failing
		conn: failingConn{err: errors.New("clickhouse unreachable")},
	}
	ctx := context.Background()

	var refusedAdds int
	for i := 0; i < 500; i++ {
		if err := s.Add(ctx, Event{Type: "order_created"}); errors.Is(err, ErrBufferFull) {
			refusedAdds++
		}
	}

	if len(s.buf) > maxBuffer {
		t.Fatalf("buffer grew to %d during a sustained outage, want <= %d", len(s.buf), maxBuffer)
	}
	if refusedAdds == 0 {
		t.Fatal("no backpressure applied — the consumer would have kept committing offsets")
	}
	refused, failures, degraded := s.Stats()
	if refused == 0 || failures == 0 || !degraded {
		t.Fatalf("Stats() = %d refused / %d failures / degraded %v, want all set", refused, failures, degraded)
	}
}
