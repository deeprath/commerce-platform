// Package clickhouse is the analytics service's OLAP sink: a thin wrapper over
// clickhouse-go that applies the embedded schema on startup and batches event
// rows into the `events` table.
package clickhouse

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
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
}

// Open connects, pings, and applies the schema.
func Open(ctx context.Context, dsn string, batchSize int) (*Sink, error) {
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
	return &Sink{conn: conn, batchSize: batchSize}, nil
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
// reached.
func (s *Sink) Add(ctx context.Context, ev Event) error {
	s.mu.Lock()
	s.buf = append(s.buf, ev)
	full := len(s.buf) >= s.batchSize
	s.mu.Unlock()
	if full {
		return s.Flush(ctx)
	}
	return nil
}

// AddClick buffers a clickstream row; it flushes automatically once batchSize is
// reached.
func (s *Sink) AddClick(ctx context.Context, cl Click) error {
	s.mu.Lock()
	s.clickBuf = append(s.clickBuf, cl)
	full := len(s.clickBuf) >= s.batchSize
	s.mu.Unlock()
	if full {
		return s.Flush(ctx)
	}
	return nil
}

// Flush drains both buffers. Each is independent: a failure on one requeues its
// own rows and is returned, but does not hold back the other.
func (s *Sink) Flush(ctx context.Context) error {
	evErr := s.flushEvents(ctx)
	clErr := s.flushClicks(ctx)
	if evErr != nil {
		return evErr
	}
	return clErr
}

// flushEvents writes and clears the funnel buffer. A no-op when empty.
func (s *Sink) flushEvents(ctx context.Context) error {
	s.mu.Lock()
	if len(s.buf) == 0 {
		s.mu.Unlock()
		return nil
	}
	rows := s.buf
	s.buf = nil
	s.mu.Unlock()

	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO events
		(event_type, occurred_at, order_id, owner_id, payment_id, amount_minor, currency, reason)`)
	if err != nil {
		s.requeue(rows)
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Type, r.OccurredAt, r.OrderID, r.OwnerID, r.PaymentID, r.AmountMinor, r.Currency, r.Reason,
		); err != nil {
			s.requeue(rows)
			return fmt.Errorf("append row: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		s.requeue(rows)
		return fmt.Errorf("send batch: %w", err)
	}
	return nil
}

// flushClicks writes and clears the clickstream buffer. A no-op when empty.
func (s *Sink) flushClicks(ctx context.Context) error {
	s.mu.Lock()
	if len(s.clickBuf) == 0 {
		s.mu.Unlock()
		return nil
	}
	rows := s.clickBuf
	s.clickBuf = nil
	s.mu.Unlock()

	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO clickstream
		(event_type, occurred_at, client_time, anonymous_id, session_id, owner_id,
		 path, referrer, product_id, query, value_minor, currency, user_agent)`)
	if err != nil {
		s.requeueClicks(rows)
		return fmt.Errorf("prepare clickstream batch: %w", err)
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Type, r.OccurredAt, r.ClientTime, r.AnonymousID, r.SessionID, r.OwnerID,
			r.Path, r.Referrer, r.ProductID, r.Query, r.ValueMinor, r.Currency, r.UserAgent,
		); err != nil {
			s.requeueClicks(rows)
			return fmt.Errorf("append clickstream row: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		s.requeueClicks(rows)
		return fmt.Errorf("send clickstream batch: %w", err)
	}
	return nil
}

// requeue puts funnel rows back at the front so a transient ClickHouse error
// retries them on the next flush rather than dropping analytics data.
func (s *Sink) requeue(rows []Event) {
	s.mu.Lock()
	s.buf = append(rows, s.buf...)
	s.mu.Unlock()
}

// requeueClicks is requeue for the clickstream buffer.
func (s *Sink) requeueClicks(rows []Click) {
	s.mu.Lock()
	s.clickBuf = append(rows, s.clickBuf...)
	s.mu.Unlock()
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
			if err := s.Flush(ctx); err != nil {
				return err
			}
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
