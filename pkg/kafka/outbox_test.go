package kafka

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// recordingPublisher captures published records and can be told to start
// failing partway through a batch.
type recordingPublisher struct {
	mu        sync.Mutex
	published []string
	failAfter int // fail once this many have succeeded; 0 never fails
}

func (p *recordingPublisher) Publish(_ context.Context, topic string, _, _ []byte, _ map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failAfter > 0 && len(p.published) >= p.failAfter {
		return errors.New("broker refused the write")
	}
	p.published = append(p.published, topic)
	return nil
}

func (p *recordingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}

// outboxRow is one pending row the fake db will hand back.
type outboxRow struct {
	id      int64
	topic   string
	key     []byte
	payload []byte
}

// fakeRows implements just the four pgx.Rows methods drainOnce calls. The
// embedded nil interface covers the rest: anything else panics, which is the
// point — it proves the drain path touches nothing more.
type fakeRows struct {
	pgx.Rows
	rows []outboxRow
	i    int
	err  error
}

func (f *fakeRows) Next() bool { f.i++; return f.i <= len(f.rows) }
func (f *fakeRows) Close()     {}
func (f *fakeRows) Err() error { return f.err }
func (f *fakeRows) Scan(dest ...any) error {
	r := f.rows[f.i-1]
	*dest[0].(*int64) = r.id
	*dest[1].(*string) = r.topic
	*dest[2].(*[]byte) = r.key
	*dest[3].(*[]byte) = r.payload
	return nil
}

// fakeDB serves pending rows in batches and records what got marked published.
type fakeDB struct {
	mu       sync.Mutex
	pending  []outboxRow
	marked   []int64
	queries  int
	queryErr error
	execErr  error
	rowErr   error
}

func (d *fakeDB) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries++
	if d.queryErr != nil {
		return nil, d.queryErr
	}
	limit := args[0].(int)
	n := min(limit, len(d.pending))
	batch := make([]outboxRow, n)
	copy(batch, d.pending[:n])
	return &fakeRows{rows: batch}, nil
}

func (d *fakeDB) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.execErr != nil {
		return pgconn.CommandTag{}, d.execErr
	}
	ids := args[0].([]int64)
	d.marked = append(d.marked, ids...)
	// Published rows stop being pending, which is what lets the relay make
	// progress across passes.
	keep := d.pending[:0]
	for _, r := range d.pending {
		if !contains(ids, r.id) {
			keep = append(keep, r)
		}
	}
	d.pending = keep
	return pgconn.CommandTag{}, nil
}

// fakeRow answers Pending's count/min query.
type fakeRow struct {
	count  int64
	oldest *time.Time
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*int64) = r.count
	*dest[1].(**time.Time) = r.oldest
	return nil
}

func (d *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rowErr != nil {
		return fakeRow{err: d.rowErr}
	}
	var oldest *time.Time
	if len(d.pending) > 0 {
		t := time.Now().Add(-90 * time.Second)
		oldest = &t
	}
	return fakeRow{count: int64(len(d.pending)), oldest: oldest}
}

func (d *fakeDB) markedIDs() []int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int64(nil), d.marked...)
}

func contains(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func rows(n int) []outboxRow {
	out := make([]outboxRow, n)
	for i := range out {
		out[i] = outboxRow{id: int64(i + 1), topic: "commerce.order.created", key: []byte("k"), payload: []byte("v")}
	}
	return out
}

// The ceiling this change removes: one drain per tick capped throughput at
// batch/interval. A tick must now clear the whole backlog.
func TestDrainUntilEmpty_ClearsABacklogSeveralBatchesDeep(t *testing.T) {
	d := &fakeDB{pending: rows(250)}
	pub := &recordingPublisher{}
	r := NewOutboxRelay(d, pub, time.Hour, 100) // interval irrelevant: one tick

	r.drainUntilEmpty(context.Background())

	if got := len(d.markedIDs()); got != 250 {
		t.Fatalf("relayed %d of 250 rows in one tick — still capped at one batch", got)
	}
	if pub.count() != 250 {
		t.Fatalf("published %d records, want 250", pub.count())
	}
	// 100 + 100 + 50: the short third pass is what tells it to stop.
	if d.queries != 3 {
		t.Fatalf("made %d drain passes, want 3 (stopping on the first short one)", d.queries)
	}
}

// A backlog that exactly fills the last batch still needs one more pass to
// discover it is empty — and must not loop forever once it does.
func TestDrainUntilEmpty_StopsWhenTheBacklogIsAnExactMultiple(t *testing.T) {
	d := &fakeDB{pending: rows(200)}
	r := NewOutboxRelay(d, &recordingPublisher{}, time.Hour, 100)

	done := make(chan struct{})
	go func() { r.drainUntilEmpty(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drainUntilEmpty did not terminate on an exact-multiple backlog")
	}
	if got := len(d.markedIDs()); got != 200 {
		t.Fatalf("marked %d of 200", got)
	}
}

// A publish failure stops the pass where it happened: the rows before it are
// marked, the failing row and everything after stay pending so the retry
// resumes in id order.
func TestDrainOnce_StopsAtFirstPublishFailureAndMarksOnlyThePrefix(t *testing.T) {
	d := &fakeDB{pending: rows(10)}
	pub := &recordingPublisher{failAfter: 3}
	r := NewOutboxRelay(d, pub, time.Hour, 100)

	n, err := r.drainOnce(context.Background())
	if err == nil {
		t.Fatal("want the publish error surfaced")
	}
	if n != 3 {
		t.Fatalf("relayed %d, want the 3 that published before the failure", n)
	}
	marked := d.markedIDs()
	if len(marked) != 3 || marked[0] != 1 || marked[2] != 3 {
		t.Fatalf("marked %v, want exactly ids 1-3", marked)
	}
	if len(d.pending) != 7 {
		t.Fatalf("%d rows still pending, want 7 (from the failure onward)", len(d.pending))
	}
}

// A failing pass must end the tick rather than spin against a broker that is
// currently refusing writes.
func TestDrainUntilEmpty_GivesUpTheTickOnError(t *testing.T) {
	d := &fakeDB{pending: rows(250), queryErr: errors.New("db down")}
	r := NewOutboxRelay(d, &recordingPublisher{}, time.Hour, 100)

	r.drainUntilEmpty(context.Background())
	if d.queries != 1 {
		t.Fatalf("made %d passes after an error, want 1", d.queries)
	}
}

// Cancellation stops the loop even with a backlog still waiting.
func TestDrainUntilEmpty_StopsOnCancelledContext(t *testing.T) {
	d := &fakeDB{pending: rows(250)}
	r := NewOutboxRelay(d, &recordingPublisher{}, time.Hour, 100)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.drainUntilEmpty(ctx)

	if d.queries != 0 {
		t.Fatalf("made %d passes on a cancelled context, want 0", d.queries)
	}
}

// Nothing to do must not mark anything or loop.
func TestDrainOnce_EmptyOutboxIsANoop(t *testing.T) {
	d := &fakeDB{}
	r := NewOutboxRelay(d, &recordingPublisher{}, time.Hour, 100)

	n, err := r.drainOnce(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("drainOnce on empty = (%d, %v), want (0, nil)", n, err)
	}
	if len(d.markedIDs()) != 0 {
		t.Fatal("marked rows from an empty outbox")
	}
}

// A failure marking the published prefix must surface, not be silently dropped
// — those rows would otherwise be republished forever.
func TestDrainOnce_SurfacesAMarkFailure(t *testing.T) {
	d := &fakeDB{pending: rows(5), execErr: errors.New("update failed")}
	r := NewOutboxRelay(d, &recordingPublisher{}, time.Hour, 100)

	if _, err := r.drainOnce(context.Background()); err == nil {
		t.Fatal("mark failure swallowed")
	}
}

func TestNewOutboxRelay_AppliesDefaults(t *testing.T) {
	r := NewOutboxRelay(&fakeDB{}, &recordingPublisher{}, 0, 0)
	if r.interval != time.Second || r.batch != 100 {
		t.Fatalf("defaults = %v / %d, want 1s / 100", r.interval, r.batch)
	}
}

// Pending is what an operator reaches for when a service has gone quiet: it has
// to report both how deep the backlog is and how long it has been stuck.
func TestPending_ReportsDepthAndAge(t *testing.T) {
	d := &fakeDB{pending: rows(12)}
	r := NewOutboxRelay(d, &recordingPublisher{}, time.Hour, 100)

	count, oldest, err := r.Pending(context.Background())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if count != 12 {
		t.Fatalf("count = %d, want 12", count)
	}
	if oldest < time.Minute {
		t.Fatalf("oldest = %v, want the age of the stuck row", oldest)
	}
}

// A drained outbox reports no backlog and no age, rather than a bogus duration
// measured from a zero timestamp.
func TestPending_EmptyOutboxHasNoAge(t *testing.T) {
	r := NewOutboxRelay(&fakeDB{}, &recordingPublisher{}, time.Hour, 100)

	count, oldest, err := r.Pending(context.Background())
	if err != nil || count != 0 || oldest != 0 {
		t.Fatalf("Pending() = (%d, %v, %v), want (0, 0, nil)", count, oldest, err)
	}
}

func TestPending_SurfacesQueryErrors(t *testing.T) {
	d := &fakeDB{rowErr: errors.New("db down")}
	r := NewOutboxRelay(d, &recordingPublisher{}, time.Hour, 100)

	if _, _, err := r.Pending(context.Background()); err == nil {
		t.Fatal("Pending swallowed a query error")
	}
}

// Run must actually drive the drain on each tick, and return when cancelled.
func TestRun_DrainsOnTickAndStopsOnCancel(t *testing.T) {
	d := &fakeDB{pending: rows(30)}
	r := NewOutboxRelay(d, &recordingPublisher{}, 10*time.Millisecond, 100)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for len(d.markedIDs()) < 30 {
		select {
		case <-deadline:
			t.Fatalf("Run relayed %d of 30 rows", len(d.markedIDs()))
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
