// Package store persists the payout aggregate. Status changes and their
// payout.* outbox rows are written in one transaction; the Kafka consumer
// dedupes on processed_events.
package store

import (
	"context"
	"embed"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	payoutv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payout/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/payout/internal/domain"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const cols = `id, order_id, shop_id, currency, amount_cents, status, created_at, paid_at`

// ShopAmount is one shop's share of a confirmed order — the unit
// CreateFromOrder turns into a single payout.
type ShopAmount struct {
	ShopID string
	Amount domain.Money
}

// CreateFromOrder inserts one payout per shop group for a confirmed order,
// plus each payout's payout.created outbox row, and records eventID in
// processed_events — all atomically. It is idempotent: a duplicate (order,
// shop) pair (a re-delivered event) is skipped rather than re-created. A
// duplicate eventID short-circuits with (nil, nil).
func (s *Store) CreateFromOrder(ctx context.Context, orderID string, groups []ShopAmount, eventID string) ([]*domain.Payout, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if eventID != "" {
		_, err := tx.Exec(ctx, `INSERT INTO processed_events (event_id) VALUES ($1)`, eventID)
		if isUniqueViolation(err) {
			return nil, nil // already processed
		}
		if err != nil {
			return nil, wrap(err)
		}
	}

	var created []*domain.Payout
	for _, g := range groups {
		if g.ShopID == "" {
			continue // first-party revenue stays with the platform, no payout
		}

		// ON CONFLICT DO NOTHING (not a bare INSERT) so a duplicate (order,
		// shop) pair does not abort the transaction.
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO payouts (order_id, shop_id, currency, amount_cents)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (order_id, shop_id) DO NOTHING
			RETURNING id`,
			orderID, g.ShopID, g.Amount.Currency, g.Amount.Cents).Scan(&id)

		if errors.Is(err, pgx.ErrNoRows) {
			continue // this shop's payout already exists (redelivered event)
		}
		if err != nil {
			return nil, wrap(err)
		}

		p, err := scanOne(ctx, tx, `SELECT `+cols+` FROM payouts WHERE id = $1`, id)
		if err != nil {
			return nil, err
		}
		u, n := p.Amount.UnitsNanos()
		if err := emitOutbox(ctx, tx, "commerce.payout.created", p.OrderID, &payoutv1.PayoutCreated{
			PayoutId: p.ID, OrderId: p.OrderID, ShopId: p.ShopID,
			Amount:     money(p.Amount.Currency, u, n),
			OccurredAt: nowRFC3339(),
		}); err != nil {
			return nil, err
		}
		created = append(created, p)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return created, nil
}

// Get returns one payout. shopID "" skips the shop-scope check (finance/admin).
func (s *Store) Get(ctx context.Context, id, shopID string) (*domain.Payout, error) {
	p, err := scanOne(ctx, s.pool, `SELECT `+cols+` FROM payouts WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if shopID != "" && p.ShopID != shopID {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "PAYOUT_NOT_FOUND", "no such payout")
	}
	return p, nil
}

// List returns payouts newest-first, keyset-paginated by created_at. shopID ""
// lists every shop's payouts (caller-gated by role in grpcsvc); status ""
// means any status.
func (s *Store) List(ctx context.Context, shopID, status string, limit int, before string) ([]*domain.Payout, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	cursor := time.Now().Add(time.Hour)
	if before != "" {
		if t, err := time.Parse(time.RFC3339Nano, before); err == nil {
			cursor = t
		}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+cols+` FROM payouts
		WHERE ($1 = '' OR shop_id = $1) AND created_at < $2 AND ($3 = '' OR status = $3)
		ORDER BY created_at DESC LIMIT $4`,
		shopID, cursor, status, limit+1)
	if err != nil {
		return nil, "", wrap(err)
	}
	defer rows.Close()

	var out []*domain.Payout
	for rows.Next() {
		p, err := scanRow(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, "", wrap(err)
	}

	next := ""
	if len(out) > limit {
		next = out[limit-1].CreatedAt.Format(time.RFC3339Nano)
		out = out[:limit]
	}
	return out, next, nil
}

// DuePayoutIDs returns PENDING payout ids older than pendingAfter — what the
// sandbox settlement sweep should mark PAID next.
func (s *Store) DuePayoutIDs(ctx context.Context, pendingAfter time.Duration, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM payouts
		WHERE status = 'PENDING' AND created_at < now() - $1::interval
		ORDER BY created_at LIMIT $2`,
		pendingAfter.String(), limit)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrap(err)
		}
		ids = append(ids, id)
	}
	return ids, wrap(rows.Err())
}

// MarkPaid applies the PENDING -> PAID transition inside a row-locked
// transaction and writes the matching payout.paid outbox row. Idempotent: a
// payout already PAID returns unchanged.
func (s *Store) MarkPaid(ctx context.Context, id string) (*domain.Payout, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	p, err := scanOne(ctx, tx, `SELECT `+cols+` FROM payouts WHERE id = $1 FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	if p.Status == domain.StatusPaid {
		return p, nil // idempotent
	}
	if err := p.MarkPaid(); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE payouts SET status = $2, paid_at = now() WHERE id = $1`,
		p.ID, string(p.Status)); err != nil {
		return nil, wrap(err)
	}
	if err := emitOutbox(ctx, tx, "commerce.payout.paid", p.OrderID, &payoutv1.PayoutPaid{
		PayoutId: p.ID, OrderId: p.OrderID, ShopId: p.ShopID, OccurredAt: nowRFC3339(),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	// Reload so paid_at reflects the DB default just written.
	return s.Get(ctx, p.ID, "")
}

// --- row scanning -----------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanOne(ctx context.Context, q querier, sql string, args ...any) (*domain.Payout, error) {
	p, err := scanRow(q.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "PAYOUT_NOT_FOUND", "no such payout")
	}
	return p, err
}

func scanRow(r rowScanner) (*domain.Payout, error) {
	var (
		p        domain.Payout
		currency string
		amount   int64
		status   string
		paidAt   *time.Time
	)
	if err := r.Scan(&p.ID, &p.OrderID, &p.ShopID, &currency, &amount, &status, &p.CreatedAt, &paidAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, wrap(err)
	}
	p.Amount = domain.Money{Currency: currency, Cents: amount}
	p.Status = domain.Status(status)
	p.PaidAt = paidAt
	return &p, nil
}

// --- helpers --------------------------------------------------------------

func emitOutbox(ctx context.Context, tx pgx.Tx, topic, key string, m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "marshal payout event")
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`, topic, []byte(key), b)
	return wrap(err)
}

func money(currency string, units int64, nanos int32) *commonv1.Money {
	return &commonv1.Money{CurrencyCode: currency, Units: units, Nanos: nanos}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return pkgerrs.Wrap(err, pkgerrs.KindInternal, "DB_ERROR", "database error: "+err.Error())
}
