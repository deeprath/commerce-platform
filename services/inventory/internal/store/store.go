// Package store is the inventory service's PostgreSQL persistence. Every stock
// mutation writes a commerce.inventory.stock_changed outbox row in the same
// transaction; Reserve/Commit/Release are atomic and row-locked.
package store

import (
	"context"
	"embed"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Level struct{ OnHand, Reserved int }

func (l Level) Available() int { return l.OnHand - l.Reserved }

// Line is a product/quantity pair.
type Line struct {
	ProductID string
	Quantity  int
}

// Levels returns current stock for the given products (missing => zeroes).
func (s *Store) Levels(ctx context.Context, ids []string) (map[string]Level, error) {
	out := map[string]Level{}
	for _, id := range ids {
		out[id] = Level{}
	}
	rows, err := s.pool.Query(ctx, `SELECT product_id, on_hand, reserved FROM stock WHERE product_id = ANY($1)`, ids)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var l Level
		if err := rows.Scan(&id, &l.OnHand, &l.Reserved); err != nil {
			return nil, wrap(err)
		}
		out[id] = l
	}
	return out, wrap(rows.Err())
}

// EnsureStockRow inserts a zero row for a product if none exists (called from
// the catalog.product_changed consumer). defaultOnHand seeds dev environments.
func (s *Store) EnsureStockRow(ctx context.Context, productID string, defaultOnHand int) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO stock (product_id, on_hand) VALUES ($1, $2)
		ON CONFLICT (product_id) DO NOTHING`, productID, defaultOnHand)
	return wrap(err)
}

// Reserve holds stock for order_ref. Returns FAILED_PRECONDITION / INSUFFICIENT_STOCK
// if any line can't be met.
func (s *Store) Reserve(ctx context.Context, orderRef string, lines []Line, ttl time.Duration) (string, time.Time, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", time.Time{}, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	expires := time.Now().Add(ttl).UTC()
	var resID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO reservations (order_ref, expires_at) VALUES ($1, $2) RETURNING id`,
		orderRef, expires).Scan(&resID); err != nil {
		return "", time.Time{}, wrap(err)
	}

	for _, ln := range lines {
		var l Level
		err := tx.QueryRow(ctx,
			`SELECT on_hand, reserved FROM stock WHERE product_id = $1 FOR UPDATE`, ln.ProductID).
			Scan(&l.OnHand, &l.Reserved)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", time.Time{}, pkgerrs.New(pkgerrs.KindFailedPrecondition, "INSUFFICIENT_STOCK",
				"no stock record for "+ln.ProductID).WithMeta("product_id", ln.ProductID)
		}
		if err != nil {
			return "", time.Time{}, wrap(err)
		}
		if l.Available() < ln.Quantity {
			return "", time.Time{}, pkgerrs.New(pkgerrs.KindFailedPrecondition, "INSUFFICIENT_STOCK",
				"not enough stock for "+ln.ProductID).
				WithMeta("product_id", ln.ProductID)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE stock SET reserved = reserved + $2, updated_at = now() WHERE product_id = $1`,
			ln.ProductID, ln.Quantity); err != nil {
			return "", time.Time{}, wrap(err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO reservation_items (reservation_id, product_id, quantity) VALUES ($1, $2, $3)`,
			resID, ln.ProductID, ln.Quantity); err != nil {
			return "", time.Time{}, wrap(err)
		}
		if err := outboxStock(ctx, tx, ln.ProductID, l.OnHand, l.Reserved+ln.Quantity); err != nil {
			return "", time.Time{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", time.Time{}, wrap(err)
	}
	return resID, expires, nil
}

// Commit turns a HELD reservation into a permanent decrement. Idempotent.
func (s *Store) Commit(ctx context.Context, resID string) error {
	return s.finish(ctx, resID, "COMMITTED", func(onHand, reserved, qty int) (int, int) {
		return onHand - qty, reserved - qty
	})
}

// Release returns a HELD reservation's stock. Idempotent; unknown id is a no-op.
func (s *Store) Release(ctx context.Context, resID string) error {
	return s.finish(ctx, resID, "RELEASED", func(onHand, reserved, qty int) (int, int) {
		return onHand, reserved - qty
	})
}

// reservationItem is one product/quantity line of a reservation.
type reservationItem struct {
	id  string
	qty int
}

func (s *Store) finish(ctx context.Context, resID, target string, apply func(onHand, reserved, qty int) (int, int)) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	held, err := reservationIsHeld(ctx, tx, resID)
	if err != nil || !held {
		return err // NotFound/not-HELD both return (nil, err) — nothing more to do
	}

	items, err := loadReservationItems(ctx, tx, resID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := applyStockDelta(ctx, tx, it, apply); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE reservations SET status = $2, updated_at = now() WHERE id = $1`, resID, target); err != nil {
		return wrap(err)
	}
	return wrap(tx.Commit(ctx))
}

// reservationIsHeld reports whether resID exists and is still HELD, row-locking
// it for the rest of the caller's transaction. A missing id is not an error —
// callers treat "not held" (missing or already resolved) as an idempotent no-op.
func reservationIsHeld(ctx context.Context, tx pgx.Tx, resID string) (bool, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM reservations WHERE id = $1 FOR UPDATE`, resID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // unknown id: nothing to do
	}
	if err != nil {
		return false, wrap(err)
	}
	return status == "HELD", nil
}

func loadReservationItems(ctx context.Context, tx pgx.Tx, resID string) ([]reservationItem, error) {
	rows, err := tx.Query(ctx, `SELECT product_id, quantity FROM reservation_items WHERE reservation_id = $1`, resID)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()

	var items []reservationItem
	for rows.Next() {
		var it reservationItem
		if err := rows.Scan(&it.id, &it.qty); err != nil {
			return nil, wrap(err)
		}
		items = append(items, it)
	}
	return items, wrap(rows.Err())
}

// applyStockDelta row-locks one product's stock, applies apply to it (clamped
// at zero), persists the result, and emits its outbox row.
func applyStockDelta(ctx context.Context, tx pgx.Tx, it reservationItem, apply func(onHand, reserved, qty int) (int, int)) error {
	var l Level
	if err := tx.QueryRow(ctx,
		`SELECT on_hand, reserved FROM stock WHERE product_id = $1 FOR UPDATE`, it.id).
		Scan(&l.OnHand, &l.Reserved); err != nil {
		return wrap(err)
	}
	nh, nr := apply(l.OnHand, l.Reserved, it.qty)
	if nr < 0 {
		nr = 0
	}
	if nh < 0 {
		nh = 0
	}
	if _, err := tx.Exec(ctx,
		`UPDATE stock SET on_hand = $2, reserved = $3, updated_at = now() WHERE product_id = $1`,
		it.id, nh, nr); err != nil {
		return wrap(err)
	}
	return outboxStock(ctx, tx, it.id, nh, nr)
}

// AdjustStock changes on_hand by delta.
func (s *Store) AdjustStock(ctx context.Context, productID string, delta int) (Level, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Level{}, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var l Level
	err = tx.QueryRow(ctx,
		`INSERT INTO stock (product_id, on_hand) VALUES ($1, GREATEST($2, 0))
		 ON CONFLICT (product_id) DO UPDATE SET on_hand = GREATEST(stock.on_hand + $2, 0), updated_at = now()
		 RETURNING on_hand, reserved`, productID, delta).Scan(&l.OnHand, &l.Reserved)
	if err != nil {
		return Level{}, wrap(err)
	}
	if err := outboxStock(ctx, tx, productID, l.OnHand, l.Reserved); err != nil {
		return Level{}, err
	}
	return l, wrap(tx.Commit(ctx))
}

// ExpireDue releases HELD reservations past their expiry and emits
// ReservationExpired for each. Returns how many it swept.
func (s *Store) ExpireDue(ctx context.Context, limit int) (int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, order_ref FROM reservations
		 WHERE status = 'HELD' AND expires_at < now()
		 ORDER BY expires_at LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, wrap(err)
	}
	type r struct{ id, orderRef string }
	var due []r
	for rows.Next() {
		var x r
		if err := rows.Scan(&x.id, &x.orderRef); err != nil {
			rows.Close()
			return 0, wrap(err)
		}
		due = append(due, x)
	}
	rows.Close()

	for _, x := range due {
		items, err := s.reservationItems(ctx, x.id)
		if err != nil {
			return 0, err
		}
		if err := s.Release(ctx, x.id); err != nil {
			return 0, err
		}
		if err := s.emitReservationExpired(ctx, x.id, x.orderRef, items); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}

func (s *Store) reservationItems(ctx context.Context, resID string) ([]Line, error) {
	rows, err := s.pool.Query(ctx, `SELECT product_id, quantity FROM reservation_items WHERE reservation_id = $1`, resID)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	var out []Line
	for rows.Next() {
		var ln Line
		if err := rows.Scan(&ln.ProductID, &ln.Quantity); err != nil {
			return nil, wrap(err)
		}
		out = append(out, ln)
	}
	return out, wrap(rows.Err())
}

func (s *Store) emitReservationExpired(ctx context.Context, resID, orderRef string, items []Line) error {
	evt := &inventoryv1.ReservationExpired{
		ReservationId: resID, OrderRef: orderRef,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	for _, it := range items {
		evt.Items = append(evt.Items, &inventoryv1.LineItem{ProductId: it.ProductID, Quantity: int32(it.Quantity)})
	}
	payload, err := proto.Marshal(evt)
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "cannot marshal ReservationExpired")
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO outbox (topic, key, payload) VALUES ($1, $2, $3)`,
		"commerce.inventory.reservation_expired", []byte(orderRef), payload)
	return wrap(err)
}

func outboxStock(ctx context.Context, tx pgx.Tx, productID string, onHand, reserved int) error {
	evt := &inventoryv1.StockChanged{
		ProductId: productID, OnHand: int32(onHand), Reserved: int32(reserved),
		Available: int32(onHand - reserved), OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	payload, err := proto.Marshal(evt)
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "cannot marshal StockChanged")
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO outbox (topic, key, payload) VALUES ($1, $2, $3)`,
		"commerce.inventory.stock_changed", []byte(productID), payload)
	return wrap(err)
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return pkgerrs.Wrap(err, pkgerrs.KindInternal, "DB_ERROR", "database error: "+err.Error())
}
