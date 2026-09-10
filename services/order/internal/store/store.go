// Package store persists the order aggregate. Saga state transitions and their
// order.* outbox rows are written in one transaction; Kafka consumers dedupe on
// processed_events.
package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Insert persists a new PENDING_PAYMENT order + lines + the order.created outbox
// row, atomically.
func (s *Store) Insert(ctx context.Context, o *domain.Order) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	shipTo, _ := json.Marshal(o.ShipTo)
	if _, err := tx.Exec(ctx, `
		INSERT INTO orders (id, owner_id, status, currency, subtotal_cents, discount_cents,
			tax_cents, total_cents, ship_to, cart_id, coupon_code, payment_id, reservation_id, pricing_signature)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		o.ID, o.OwnerID, string(o.Status), o.Total.Currency,
		o.Subtotal.Cents, o.Discount.Cents, o.Tax.Cents, o.Total.Cents,
		shipTo, o.CartID, o.CouponCode, o.PaymentID, o.ReservationID, o.PricingSignature); err != nil {
		return wrap(err)
	}
	for _, l := range o.Lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_lines (order_id, product_id, title, quantity, unit_price_cents, line_total_cents)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			o.ID, l.ProductID, l.Title, l.Quantity, l.UnitPrice.Cents, l.LineTotal.Cents); err != nil {
			return wrap(err)
		}
	}
	if err := outboxCreated(ctx, tx, o); err != nil {
		return err
	}
	return wrap(tx.Commit(ctx))
}

// Get loads one order (with lines). ownerID != "" restricts to that owner.
func (s *Store) Get(ctx context.Context, id, ownerID string) (*domain.Order, error) {
	return s.get(ctx, s.pool, id, ownerID)
}

// notFoundID rejects a syntactically invalid UUID before it reaches a `WHERE
// id = $1` on a uuid column (which would be a Postgres error -> 500). A
// malformed id definitionally matches no row, so NotFound is the honest answer
// and it does not disclose the id format.
func notFoundID(id, reason string) error {
	if _, err := uuid.Parse(id); err != nil {
		return pkgerrs.New(pkgerrs.KindNotFound, reason, "no such record")
	}
	return nil
}

func (s *Store) get(ctx context.Context, q querier, id, ownerID string) (*domain.Order, error) {
	if err := notFoundID(id, "ORDER_NOT_FOUND"); err != nil {
		return nil, err
	}
	var (
		o      domain.Order
		status string
		shipTo []byte
		cur    string
		subC   int64
		discC  int64
		taxC   int64
		totC   int64
	)
	row := q.QueryRow(ctx,
		`SELECT id, owner_id, status, currency, subtotal_cents, discount_cents, tax_cents, total_cents,
			ship_to, cart_id, coupon_code, payment_id, reservation_id, pricing_signature, cancel_reason,
			created_at, updated_at
		 FROM orders WHERE id = $1`, id)
	err := row.Scan(&o.ID, &o.OwnerID, &status, &cur, &subC, &discC, &taxC, &totC,
		&shipTo, &o.CartID, &o.CouponCode, &o.PaymentID, &o.ReservationID, &o.PricingSignature, &o.CancelReason,
		&o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "ORDER_NOT_FOUND", "no such order")
	}
	if err != nil {
		return nil, wrap(err)
	}
	if ownerID != "" && o.OwnerID != ownerID {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "ORDER_NOT_FOUND", "no such order")
	}
	o.Status = domain.Status(status)
	o.Subtotal = domain.Money{Currency: cur, Cents: subC}
	o.Discount = domain.Money{Currency: cur, Cents: discC}
	o.Tax = domain.Money{Currency: cur, Cents: taxC}
	o.Total = domain.Money{Currency: cur, Cents: totC}
	_ = json.Unmarshal(shipTo, &o.ShipTo)

	rows, err := q.Query(ctx,
		`SELECT product_id, title, quantity, unit_price_cents, line_total_cents FROM order_lines WHERE order_id = $1 ORDER BY product_id`, id)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.Line
		var up, lt int64
		if err := rows.Scan(&l.ProductID, &l.Title, &l.Quantity, &up, &lt); err != nil {
			return nil, wrap(err)
		}
		l.UnitPrice = domain.Money{Currency: cur, Cents: up}
		l.LineTotal = domain.Money{Currency: cur, Cents: lt}
		o.Lines = append(o.Lines, l)
	}
	return &o, wrap(rows.Err())
}

// FindByPaymentID / FindByReservationID resolve an order for an inbound event.
func (s *Store) FindByPaymentID(ctx context.Context, paymentID string) (*domain.Order, error) {
	return s.findBy(ctx, "payment_id", paymentID)
}
func (s *Store) FindByReservationID(ctx context.Context, resID string) (*domain.Order, error) {
	return s.findBy(ctx, "reservation_id", resID)
}
func (s *Store) findBy(ctx context.Context, col, val string) (*domain.Order, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id FROM orders WHERE `+col+` = $1`, val).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "ORDER_NOT_FOUND", "no order for "+col)
	}
	if err != nil {
		return nil, wrap(err)
	}
	return s.Get(ctx, id, "")
}

// List returns the owner's orders newest-first. before (RFC3339Nano) is the
// keyset cursor; the returned string is the next cursor, or "" when exhausted.
// List returns orders newest-first. ownerID "" lists every customer's orders
// (caller-gated by role in grpcsvc); status "" means any status.
func (s *Store) List(ctx context.Context, ownerID, status string, limit int, before string) ([]*domain.Order, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor := time.Now().Add(time.Hour)
	if before != "" {
		t, err := time.Parse(time.RFC3339Nano, before)
		if err != nil {
			return nil, "", pkgerrs.New(pkgerrs.KindInvalidArgument, "BAD_CURSOR", "page_token is invalid")
		}
		cursor = t
	}
	q := `SELECT id FROM orders
		WHERE ($1 = '' OR owner_id = $1) AND ($2 = '' OR status = $2) AND created_at < $3
		ORDER BY created_at DESC LIMIT $4`
	rows, err := s.pool.Query(ctx, q, ownerID, status, cursor, limit+1)
	if err != nil {
		return nil, "", wrap(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, "", wrap(err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	out := make([]*domain.Order, 0, len(ids))
	for _, id := range ids {
		o, err := s.Get(ctx, id, ownerID)
		if err != nil {
			return nil, "", err
		}
		out = append(out, o)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out, next, nil
}

// Apply runs fn against the order inside a row-locked transaction and, if fn
// changed the status, writes the matching order.* outbox row. It also records
// eventID in processed_events; a duplicate delivery short-circuits with (nil,nil).
func (s *Store) Apply(ctx context.Context, orderID, eventID string, fn func(*domain.Order) error) (*domain.Order, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if eventID != "" {
		_, err := tx.Exec(ctx, `INSERT INTO processed_events (event_id) VALUES ($1)`, eventID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, nil // already processed
		}
		if err != nil {
			return nil, wrap(err)
		}
	}

	o, err := s.get(ctx, tx, orderID, "")
	if err != nil {
		return nil, err
	}
	before := o.Status
	if err := fn(o); err != nil {
		return nil, err
	}
	if o.Status != before {
		if _, err := tx.Exec(ctx, `
			UPDATE orders SET status = $2, cancel_reason = $3, payment_id = $4, reservation_id = $5, updated_at = now()
			WHERE id = $1`,
			o.ID, string(o.Status), o.CancelReason, o.PaymentID, o.ReservationID); err != nil {
			return nil, wrap(err)
		}
		if err := outboxTransition(ctx, tx, o); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return o, nil
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func outboxCreated(ctx context.Context, tx pgx.Tx, o *domain.Order) error {
	u, n := o.Total.UnitsNanos()
	b, err := proto.Marshal(&orderv1.OrderCreated{
		OrderId: o.ID, OwnerId: o.OwnerID, CartId: o.CartID,
		Total:      &commonv1.Money{CurrencyCode: o.Total.Currency, Units: u, Nanos: n},
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "marshal OrderCreated")
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`,
		"commerce.order.created", []byte(o.ID), b)
	return wrap(err)
}

func outboxTransition(ctx context.Context, tx pgx.Tx, o *domain.Order) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var topic string
	var b []byte
	var err error
	switch o.Status {
	case domain.StatusConfirmed:
		topic = "commerce.order.confirmed"
		b, err = proto.Marshal(&orderv1.OrderConfirmed{
			OrderId: o.ID, OwnerId: o.OwnerID, PaymentId: o.PaymentID, OccurredAt: now,
			ShipTo: shipToProto(o.ShipTo), Lines: linesProto(o.Lines),
		})
	case domain.StatusCancelled:
		topic = "commerce.order.cancelled"
		b, err = proto.Marshal(&orderv1.OrderCancelled{OrderId: o.ID, OwnerId: o.OwnerID, Reason: o.CancelReason, OccurredAt: now})
	case domain.StatusFulfilled:
		topic = "commerce.order.fulfilled"
		b, err = proto.Marshal(&orderv1.OrderFulfilled{OrderId: o.ID, OwnerId: o.OwnerID, OccurredAt: now})
	default:
		return nil
	}
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "marshal order event")
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`, topic, []byte(o.ID), b)
	return wrap(err)
}

func shipToProto(a domain.Address) *commonv1.Address {
	return &commonv1.Address{
		FullName: a.FullName, Line1: a.Line1, Line2: a.Line2, City: a.City,
		Region: a.Region, PostalCode: a.PostalCode, CountryCode: a.CountryCode, Phone: a.Phone,
	}
}

func linesProto(lines []domain.Line) []*orderv1.OrderLine {
	out := make([]*orderv1.OrderLine, 0, len(lines))
	for _, l := range lines {
		uu, un := l.UnitPrice.UnitsNanos()
		lu, ln := l.LineTotal.UnitsNanos()
		out = append(out, &orderv1.OrderLine{
			ProductId: l.ProductID, Title: l.Title, Quantity: l.Quantity,
			UnitPrice: &commonv1.Money{CurrencyCode: l.UnitPrice.Currency, Units: uu, Nanos: un},
			LineTotal: &commonv1.Money{CurrencyCode: l.LineTotal.Currency, Units: lu, Nanos: ln},
		})
	}
	return out
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return pkgerrs.Wrap(err, pkgerrs.KindInternal, "DB_ERROR", "database error: "+err.Error())
}
