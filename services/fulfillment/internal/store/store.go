// Package store persists the shipment aggregate. Status changes and their
// fulfillment.* outbox rows are written in one transaction; the Kafka consumer
// dedupes on processed_events.
package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/domain"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const cols = `id, order_id, owner_id, status, carrier, tracking_number, ship_to, items,
	cancel_reason, created_at, shipped_at, delivered_at, shop_id`

// ShopItems is one shop's slice of a confirmed order's lines — the unit
// CreateFromOrder turns into a single shipment.
type ShopItems struct {
	ShopID string
	Items  []domain.Item
}

// CreateFromOrder inserts one shipment per shop group (see ShopItems) for a
// confirmed order, plus each shipment's fulfillment.shipment_created outbox
// row, and records eventID in processed_events — all atomically. It is
// idempotent: a duplicate (order, shop) pair (a re-delivered event) is
// skipped rather than re-created, and does not emit a second created event.
// A duplicate eventID short-circuits with (nil, nil).
func (s *Store) CreateFromOrder(
	ctx context.Context, orderID, ownerID string, shipTo domain.Address, groups []ShopItems, eventID string,
) ([]*domain.Shipment, error) {
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

	shipToJSON, _ := json.Marshal(shipTo)

	var created []*domain.Shipment
	for _, g := range groups {
		itemsJSON, _ := json.Marshal(g.Items)

		// ON CONFLICT DO NOTHING (not a bare INSERT) so a duplicate (order,
		// shop) pair does not abort the transaction — we still need to
		// commit the processed_events row and create the other groups.
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO shipments (order_id, shop_id, owner_id, ship_to, items)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (order_id, shop_id) DO NOTHING
			RETURNING id`,
			orderID, g.ShopID, ownerID, shipToJSON, itemsJSON).Scan(&id)

		if errors.Is(err, pgx.ErrNoRows) {
			continue // this shop's shipment already exists (redelivered event)
		}
		if err != nil {
			return nil, wrap(err)
		}

		sh, err := scanOne(ctx, tx, `SELECT `+cols+` FROM shipments WHERE id = $1`, id)
		if err != nil {
			return nil, err
		}
		if err := emitOutbox(ctx, tx, "commerce.fulfillment.shipment_created",
			sh.OrderID, &fulfillmentv1.ShipmentCreated{
				ShipmentId: sh.ID, OrderId: sh.OrderID, OwnerId: sh.OwnerID,
				ShopId: sh.ShopID, OccurredAt: nowRFC3339(),
			}); err != nil {
			return nil, err
		}
		created = append(created, sh)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return created, nil
}

// Get returns one shipment. ownerID "" skips the owner-scope check.
func (s *Store) Get(ctx context.Context, id, ownerID string) (*domain.Shipment, error) {
	sh, err := scanOne(ctx, s.pool, `SELECT `+cols+` FROM shipments WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if ownerID != "" && sh.OwnerID != ownerID {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "SHIPMENT_NOT_FOUND", "no such shipment")
	}
	return sh, nil
}

// List returns shipments newest-first, keyset-paginated by created_at.
// ownerID "" lists every customer's shipments (caller-gated by role in
// grpcsvc); orderID / status "" => no filter.
func (s *Store) List(
	ctx context.Context, ownerID, orderID, status string, limit int, before string,
) ([]*domain.Shipment, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	cursor := time.Now().Add(time.Hour)
	if before != "" {
		if t, err := time.Parse(time.RFC3339Nano, before); err == nil {
			cursor = t
		}
	}
	q := `SELECT ` + cols + ` FROM shipments
		WHERE ($1 = '' OR owner_id = $1) AND created_at < $2
		  AND ($3 = '' OR order_id = $3) AND ($4 = '' OR status = $4)
		ORDER BY created_at DESC LIMIT $5`
	rows, err := s.pool.Query(ctx, q, ownerID, cursor, orderID, status, limit+1)
	if err != nil {
		return nil, "", wrap(err)
	}
	defer rows.Close()

	var out []*domain.Shipment
	for rows.Next() {
		sh, err := scanRow(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, sh)
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

// AdvanceTarget names the next state the sandbox carrier should drive a
// shipment to.
type AdvanceTarget struct {
	ID string
	To domain.Status
}

// DueForAdvance returns shipments the sandbox carrier should move on: PENDING
// ones older than pendingAfter (=> SHIPPED) and SHIPPED ones whose shipped_at
// is older than shippedAfter (=> DELIVERED).
func (s *Store) DueForAdvance(
	ctx context.Context, pendingAfter, shippedAfter time.Duration, limit int,
) ([]AdvanceTarget, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,
		       CASE WHEN status = 'PENDING' THEN 'SHIPPED' ELSE 'DELIVERED' END AS next
		FROM shipments
		WHERE (status = 'PENDING' AND created_at < now() - $1::interval)
		   OR (status = 'SHIPPED' AND shipped_at < now() - $2::interval)
		ORDER BY created_at
		LIMIT $3`,
		pendingAfter.String(), shippedAfter.String(), limit)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()

	var out []AdvanceTarget
	for rows.Next() {
		var t AdvanceTarget
		var to string
		if err := rows.Scan(&t.ID, &to); err != nil {
			return nil, wrap(err)
		}
		t.To = domain.Status(to)
		out = append(out, t)
	}
	return out, wrap(rows.Err())
}

// Transition applies a status change inside a row-locked transaction and writes
// the matching fulfillment.* outbox event. Idempotent: a shipment already in
// `to` returns unchanged.
func (s *Store) Transition(
	ctx context.Context, id string, to domain.Status, carrier, tracking, reason string,
) (*domain.Shipment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	sh, err := scanOne(ctx, tx, `SELECT `+cols+` FROM shipments WHERE id = $1 FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	if sh.Status == to {
		return sh, nil // idempotent
	}

	var applyErr error
	switch to {
	case domain.StatusShipped:
		applyErr = sh.MarkShipped(carrier, tracking)
	case domain.StatusDelivered:
		applyErr = sh.MarkDelivered()
	case domain.StatusCancelled:
		applyErr = sh.Cancel(reason)
	default:
		applyErr = pkgerrs.New(pkgerrs.KindInvalidArgument, "BAD_TARGET", "unknown target status "+string(to))
	}
	if applyErr != nil {
		return nil, applyErr
	}

	if _, err := tx.Exec(ctx, `
		UPDATE shipments SET
			status = $2, carrier = $3, tracking_number = $4, cancel_reason = $5,
			shipped_at = CASE WHEN $2 = 'SHIPPED' AND shipped_at IS NULL THEN now() ELSE shipped_at END,
			delivered_at = CASE WHEN $2 = 'DELIVERED' AND delivered_at IS NULL THEN now() ELSE delivered_at END,
			updated_at = now()
		WHERE id = $1`,
		sh.ID, string(sh.Status), sh.Carrier, sh.TrackingNumber, sh.CancelReason); err != nil {
		return nil, wrap(err)
	}

	if topic, evt := eventFor(sh); topic != "" {
		if err := emitOutbox(ctx, tx, topic, sh.OrderID, evt); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	// Reload so shipped_at/delivered_at reflect the DB defaults just written.
	return s.Get(ctx, sh.ID, "")
}

func eventFor(sh *domain.Shipment) (string, proto.Message) {
	now := nowRFC3339()
	switch sh.Status {
	case domain.StatusShipped:
		return "commerce.fulfillment.shipped", &fulfillmentv1.ShipmentShipped{
			ShipmentId: sh.ID, OrderId: sh.OrderID, OwnerId: sh.OwnerID,
			Carrier: sh.Carrier, TrackingNumber: sh.TrackingNumber, ShopId: sh.ShopID, OccurredAt: now,
		}
	case domain.StatusDelivered:
		return "commerce.fulfillment.delivered", &fulfillmentv1.ShipmentDelivered{
			ShipmentId: sh.ID, OrderId: sh.OrderID, OwnerId: sh.OwnerID, ShopId: sh.ShopID, OccurredAt: now,
		}
	case domain.StatusCancelled:
		return "commerce.fulfillment.cancelled", &fulfillmentv1.ShipmentCancelled{
			ShipmentId: sh.ID, OrderId: sh.OrderID, OwnerId: sh.OwnerID,
			Reason: sh.CancelReason, ShopId: sh.ShopID, OccurredAt: now,
		}
	default:
		return "", nil
	}
}

// --- row scanning -----------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanOne(ctx context.Context, q querier, sql string, args ...any) (*domain.Shipment, error) {
	sh, err := scanRow(q.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "SHIPMENT_NOT_FOUND", "no such shipment")
	}
	return sh, err
}

func scanRow(r rowScanner) (*domain.Shipment, error) {
	var (
		sh                   domain.Shipment
		shipToJSON, itemJSON []byte
		shippedAt, deliverAt *time.Time
	)
	if err := r.Scan(
		&sh.ID, &sh.OrderID, &sh.OwnerID, &sh.Status, &sh.Carrier, &sh.TrackingNumber,
		&shipToJSON, &itemJSON, &sh.CancelReason, &sh.CreatedAt, &shippedAt, &deliverAt, &sh.ShopID,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, wrap(err)
	}
	_ = json.Unmarshal(shipToJSON, &sh.ShipTo)
	_ = json.Unmarshal(itemJSON, &sh.Items)
	sh.ShippedAt, sh.DeliveredAt = shippedAt, deliverAt
	return &sh, nil
}

// --- helpers --------------------------------------------------------------

func emitOutbox(ctx context.Context, tx pgx.Tx, topic, key string, m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "marshal fulfillment event")
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`, topic, []byte(key), b)
	return wrap(err)
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
