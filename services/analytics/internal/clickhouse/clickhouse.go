// Package clickhouse is the analytics service's OLAP sink: a thin wrapper over
// clickhouse-go that applies the embedded schema on startup and batches event
// rows into the `events` table.
package clickhouse

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// Event is one flattened analytics fact — the shape of a row in `events`.
type Event struct {
	Type        string
	OccurredAt  time.Time
	OrderID     string
	OwnerID     string
	PaymentID   string
	AmountMinor int64 // money in minor units (cents); 0 when not applicable
	Currency    string
	Reason      string
}

// Click is one flattened browser interaction — the shape of a row in
// `clickstream`. It is a distinct fact from Event (funnel/revenue) with its own
// table, buffer and consumer group.
type Click struct {
	Type        string
	OccurredAt  time.Time // BFF receive time — authoritative for ordering
	ClientTime  time.Time // browser clock, may be skewed
	AnonymousID string
	SessionID   string
	OwnerID     string
	Path        string
	Referrer    string
	ProductID   string
	Query       string
	ValueMinor  int64
	Currency    string
	UserAgent   string
}

// Sink writes events to ClickHouse. Safe for concurrent Add/AddClick; Flush and
// the background flusher serialise on the mutex. It holds two independent
// buffers — funnel facts (`events`) and clickstream rows (`clickstream`) — that
// flush together but fail independently.
type Sink struct {
	conn      driver.Conn
	mu        sync.Mutex
	buf       []Event
	clickBuf  []Click
	batchSize int
	maxBuffer int

	// Degradation state, guarded by mu. Counters rather than OTEL metrics
	// because this repo has no meter helper yet; Stats() exposes them so a
	// future metrics wiring — and the tests — can read them.
	refused       uint64
	flushFailures uint64
	degraded      bool
}

// ErrBufferFull is returned by Add/AddClick once maxBuffer rows are already
// waiting on a ClickHouse that isn't draining them. It is backpressure, not an
// error to retry in place: the caller should stop advancing its source offsets
// and let Kafka — which is already a durable, replayable buffer — hold the
// backlog, rather than have this process grow a second, non-durable copy of it
// in memory until the pod is OOM-killed.
var ErrBufferFull = errors.New("analytics buffer full")

// Stats reports degradation counters: rows refused by backpressure, flush
// failures so far, and whether the sink is currently refusing writes.
func (s *Sink) Stats() (refused, flushFailures uint64, degraded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused, s.flushFailures, s.degraded
}

// Open connects, pings, and applies the schema. batchSize is the row count that
// triggers an eager flush; maxBuffer caps how many rows may wait in memory when
// ClickHouse is unreachable, after which Add/AddClick apply backpressure.
func Open(ctx context.Context, dsn string, batchSize, maxBuffer int) (*Sink, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse clickhouse dsn: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping clickhouse: %w", err)
	}
	if err := applySchema(ctx, conn); err != nil {
		return nil, err
	}
	if maxBuffer < batchSize {
		maxBuffer = batchSize
	}
	return &Sink{conn: conn, batchSize: batchSize, maxBuffer: maxBuffer}, nil
}

func applySchema(ctx context.Context, conn driver.Conn) error {
	names, err := fs.Glob(schemaFS, "schema/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := schemaFS.ReadFile(n)
		if err != nil {
			return err
		}
		// Each file may hold several ';'-terminated statements.
		for _, stmt := range strings.Split(string(b), ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if err := conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("apply %s: %w", n, err)
			}
		}
	}
	return nil
}

// Add buffers a funnel event; it flushes automatically once batchSize is
// reached. Returns ErrBufferFull, and buffers nothing, when the sink is already
// holding maxBuffer rows.
//
// A failed flush is deliberately not reported to the caller: the row is safely
// buffered either way and the next flush retries it, so the caller should
// commit its source offset rather than reprocess a record this sink already
// holds. Only backpressure — where nothing was buffered — is an error here.
func (s *Sink) Add(ctx context.Context, ev Event) error {
	s.mu.Lock()
	if len(s.buf) >= s.maxBuffer {
		s.refuseLocked(ctx, len(s.buf))
		s.mu.Unlock()
		return ErrBufferFull
	}
	s.buf = append(s.buf, ev)
	full := len(s.buf) >= s.batchSize
	s.mu.Unlock()
	if full {
		s.flushLogging(ctx)
	}
	return nil
}

// AddClick is Add for the clickstream buffer, with the same semantics.
func (s *Sink) AddClick(ctx context.Context, cl Click) error {
	s.mu.Lock()
	if len(s.clickBuf) >= s.maxBuffer {
		s.refuseLocked(ctx, len(s.clickBuf))
		s.mu.Unlock()
		return ErrBufferFull
	}
	s.clickBuf = append(s.clickBuf, cl)
	full := len(s.clickBuf) >= s.batchSize
	s.mu.Unlock()
	if full {
		s.flushLogging(ctx)
	}
	return nil
}

// refuseLocked counts a backpressured row and logs the transition into the
// degraded state once, not once per refused row. Caller holds mu.
func (s *Sink) refuseLocked(ctx context.Context, held int) {
	s.refused++
	if s.degraded {
		return
	}
	s.degraded = true
	slog.WarnContext(ctx, "analytics sink is full; applying backpressure so Kafka holds the backlog",
		slog.Int("buffered_rows", held), slog.Int("max_buffer", s.maxBuffer))
}

// flushLogging runs a flush whose failure is not the caller's problem — the
// rows stay buffered for the next attempt, so this logs and counts instead of
// propagating.
func (s *Sink) flushLogging(ctx context.Context) {
	if err := s.Flush(ctx); err != nil {
		s.mu.Lock()
		s.flushFailures++
		n := s.flushFailures
		s.mu.Unlock()
		slog.WarnContext(ctx, "analytics flush failed; rows stay buffered for the next attempt",
			slog.Any("err", err), slog.Uint64("flush_failures", n))
	}
}

// Flush drains both buffers. Each is independent: a failure on one requeues its
// own rows and is returned, but does not hold back the other.
func (s *Sink) Flush(ctx context.Context) error {
	evErr := s.flushEvents(ctx)
	clErr := s.flushClicks(ctx)
	if evErr != nil {
		return evErr
	}
	if clErr != nil {
		return clErr
	}
	s.clearDegraded(ctx)
	return nil
}

// flushEvents writes the oldest batchSize funnel rows. A no-op when empty.
//
// Rows are *reserved*, not removed, until ClickHouse confirms the write — they
// are only dropped from the buffer on success. The buffer therefore never
// exceeds maxBuffer, and there is no requeue-on-failure path to grow it.
// (Clearing the buffer up front and putting rows back on failure is what let it
// grow without bound: the window between the two accepted new rows freely.)
func (s *Sink) flushEvents(ctx context.Context) error {
	s.mu.Lock()
	n := min(len(s.buf), s.batchSize)
	if n == 0 {
		s.mu.Unlock()
		return nil
	}
	rows := make([]Event, n)
	copy(rows, s.buf[:n])
	s.mu.Unlock()

	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO events
		(event_type, occurred_at, order_id, owner_id, payment_id, amount_minor, currency, reason)`)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Type, r.OccurredAt, r.OrderID, r.OwnerID, r.PaymentID, r.AmountMinor, r.Currency, r.Reason,
		); err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send batch: %w", err)
	}

	s.mu.Lock()
	s.buf = slices.Delete(s.buf, 0, n) // keep anything appended mid-send
	s.mu.Unlock()
	return nil
}

// flushClicks is flushEvents for the clickstream buffer, with the same
// reserve-until-confirmed semantics.
func (s *Sink) flushClicks(ctx context.Context) error {
	s.mu.Lock()
	n := min(len(s.clickBuf), s.batchSize)
	if n == 0 {
		s.mu.Unlock()
		return nil
	}
	rows := make([]Click, n)
	copy(rows, s.clickBuf[:n])
	s.mu.Unlock()

	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO clickstream
		(event_type, occurred_at, client_time, anonymous_id, session_id, owner_id,
		 path, referrer, product_id, query, value_minor, currency, user_agent)`)
	if err != nil {
		return fmt.Errorf("prepare clickstream batch: %w", err)
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Type, r.OccurredAt, r.ClientTime, r.AnonymousID, r.SessionID, r.OwnerID,
			r.Path, r.Referrer, r.ProductID, r.Query, r.ValueMinor, r.Currency, r.UserAgent,
		); err != nil {
			return fmt.Errorf("append clickstream row: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send clickstream batch: %w", err)
	}

	s.mu.Lock()
	s.clickBuf = slices.Delete(s.clickBuf, 0, n)
	s.mu.Unlock()
	return nil
}

// clearDegraded lifts the backpressure flag after a clean flush, logging the
// recovery once with how many rows were refused while it lasted.
func (s *Sink) clearDegraded(ctx context.Context) {
	s.mu.Lock()
	if !s.degraded {
		s.mu.Unlock()
		return
	}
	s.degraded = false
	refused := s.refused
	s.mu.Unlock()
	slog.InfoContext(ctx, "analytics sink draining again; backpressure lifted",
		slog.Uint64("rows_refused_while_full", refused))
}

// RunFlusher flushes on an interval until ctx is done, then flushes once more.
func (s *Sink) RunFlusher(ctx context.Context, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.Flush(context.WithoutCancel(ctx))
		case <-t.C:
			// Detached from ctx's cancellation deliberately, not just for
			// symmetry with the shutdown flush above: ctx here only signals
			// "stop looping," it's not a deadline for any one flush. Passing
			// it straight through would race the caller's cancel against
			// whichever periodic flush happens to be in flight at the time —
			// select can land on this case in the same instant ctx.Done()
			// becomes ready, or cancellation can simply arrive mid-Send, and
			// either way the batch fails with "context canceled" instead of
			// completing. Using ctx only to decide whether to keep looping,
			// never to bound the flush itself, closes that race entirely.
			// A flush failure must not end the loop. This runs in the service's
			// errgroup, so returning here cancelled every other goroutine and
			// exited the process — one transient ClickHouse blip took the whole
			// analytics service down and then crash-looped it, since each
			// restart re-consumed and hit the same wall. Log, count, keep
			// ticking: the rows are still buffered and the next tick retries
			// them, and Add applies backpressure if the buffer fills meanwhile.
			s.flushLogging(context.WithoutCancel(ctx))
		}
	}
}

// Close flushes and closes the connection.
func (s *Sink) Close(ctx context.Context) error {
	_ = s.Flush(ctx)
	return s.conn.Close()
}

// Conn exposes the underlying connection for tests / ad-hoc queries.
func (s *Sink) Conn() driver.Conn { return s.conn }
