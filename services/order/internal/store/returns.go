package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
)

// InsertReturn persists a REQUESTED return + its lines + the
// order.return_requested outbox row, atomically.
func (s *Store) InsertReturn(ctx context.Context, r *domain.Return) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := tx.QueryRow(ctx, `
		INSERT INTO returns (id, order_id, owner_id, status, reason, currency, refund_total_cents)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING created_at, updated_at`,
		r.ID, r.OrderID, r.OwnerID, string(r.Status), r.Reason, r.RefundTotal.Currency, r.RefundTotal.Cents,
	).Scan(&r.CreatedAt, &r.UpdatedAt); err != nil {
		return wrap(err)
	}
	for _, l := range r.Lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO return_lines (return_id, product_id, quantity, refund_amount_cents)
			VALUES ($1,$2,$3,$4)`,
			r.ID, l.ProductID, l.Quantity, l.RefundAmount.Cents); err != nil {
			return wrap(err)
		}
	}
	if err := emitReturnEvent(ctx, tx, r); err != nil {
		return err
	}
	return wrap(tx.Commit(ctx))
}

// GetReturn loads one return with its lines. ownerID "" skips the owner check.
func (s *Store) GetReturn(ctx context.Context, id, ownerID string) (*domain.Return, error) {
	return s.getReturn(ctx, s.pool, id, ownerID)
}

func (s *Store) getReturn(ctx context.Context, q querier, id, ownerID string) (*domain.Return, error) {
	var (
		r   domain.Return
		st  string
		cur string
		tot int64
	)
	err := q.QueryRow(ctx, `
		SELECT id, order_id, owner_id, status, reason, currency, refund_total_cents,
		       decided_by, decision_note, created_at, updated_at
		FROM returns WHERE id = $1`, id).
		Scan(&r.ID, &r.OrderID, &r.OwnerID, &st, &r.Reason, &cur, &tot,
			&r.DecidedBy, &r.DecisionNote, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "RETURN_NOT_FOUND", "no such return")
	}
	if err != nil {
		return nil, wrap(err)
	}
	if ownerID != "" && r.OwnerID != ownerID {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "RETURN_NOT_FOUND", "no such return")
	}
	r.Status = domain.ReturnStatus(st)
	r.RefundTotal = domain.Money{Currency: cur, Cents: tot}

	rows, err := q.Query(ctx,
		`SELECT product_id, quantity, refund_amount_cents FROM return_lines WHERE return_id = $1 ORDER BY product_id`, id)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.ReturnLine
		var amt int64
		if err := rows.Scan(&l.ProductID, &l.Quantity, &amt); err != nil {
			return nil, wrap(err)
		}
		l.RefundAmount = domain.Money{Currency: cur, Cents: amt}
		r.Lines = append(r.Lines, l)
	}
	return &r, wrap(rows.Err())
}

// ListReturns returns the caller's returns, newest first, keyset-paginated by
// created_at (cursor is an RFC3339Nano timestamp).
func (s *Store) ListReturns(ctx context.Context, ownerID string, limit int, before string) ([]*domain.Return, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor := time.Now().Add(time.Hour)
	if before != "" {
		if t, err := time.Parse(time.RFC3339Nano, before); err == nil {
			cursor = t
		}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM returns WHERE owner_id = $1 AND created_at < $2
		ORDER BY created_at DESC LIMIT $3`, ownerID, cursor, limit+1)
	if err != nil {
		return nil, "", wrap(err)
	}
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, "", wrap(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", wrap(err)
	}

	out := make([]*domain.Return, 0, len(ids))
	for _, id := range ids {
		r, err := s.getReturn(ctx, s.pool, id, ownerID)
		if err != nil {
			return nil, "", err
		}
		out = append(out, r)
	}
	next := ""
	if len(out) > limit {
		next = out[limit-1].CreatedAt.Format(time.RFC3339Nano)
		out = out[:limit]
	}
	return out, next, nil
}

// ReturnedQty is how many units of productID on orderID are covered by returns
// that are not REJECTED (i.e. still count against the returnable quantity).
func (s *Store) ReturnedQty(ctx context.Context, orderID, productID string) (int32, error) {
	var n int32
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(rl.quantity), 0)
		FROM return_lines rl
		JOIN returns r ON r.id = rl.return_id
		WHERE r.order_id = $1 AND rl.product_id = $2 AND r.status <> 'REJECTED'`,
		orderID, productID).Scan(&n)
	return n, wrap(err)
}

// DecideReturn applies an operator's decision inside a row-locked transaction
// and emits order.return_approved / order.return_rejected on a status change.
// Idempotent: deciding a return the same way twice is a no-op.
func (s *Store) DecideReturn(ctx context.Context, id, decidedBy string, approve bool, note string) (*domain.Return, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the row for the duration of the decision.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM returns WHERE id = $1 FOR UPDATE`, id); err != nil {
		return nil, wrap(err)
	}
	r, err := s.getReturn(ctx, tx, id, "")
	if err != nil {
		return nil, err
	}
	before := r.Status
	if err := r.Decide(approve, decidedBy, note); err != nil {
		return nil, err
	}
	if r.Status != before {
		if _, err := tx.Exec(ctx, `
			UPDATE returns SET status = $2, decided_by = $3, decision_note = $4, updated_at = now()
			WHERE id = $1`, r.ID, string(r.Status), r.DecidedBy, r.DecisionNote); err != nil {
			return nil, wrap(err)
		}
		if err := emitReturnEvent(ctx, tx, r); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return r, nil
}

// UnrestockedLines returns the return's lines that have not yet been added back
// to inventory.
func (s *Store) UnrestockedLines(ctx context.Context, returnID string) ([]domain.ReturnLine, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT product_id, quantity FROM return_lines WHERE return_id = $1 AND NOT restocked ORDER BY product_id`, returnID)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	var out []domain.ReturnLine
	for rows.Next() {
		var l domain.ReturnLine
		if err := rows.Scan(&l.ProductID, &l.Quantity); err != nil {
			return nil, wrap(err)
		}
		out = append(out, l)
	}
	return out, wrap(rows.Err())
}

// MarkRestocked records that a line's units are back in inventory.
func (s *Store) MarkRestocked(ctx context.Context, returnID, productID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE return_lines SET restocked = TRUE WHERE return_id = $1 AND product_id = $2`, returnID, productID)
	return wrap(err)
}

func emitReturnEvent(ctx context.Context, tx pgx.Tx, r *domain.Return) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var topic string
	var b []byte
	var err error
	switch r.Status {
	case domain.ReturnRequested:
		topic = "commerce.order.return_requested"
		b, err = proto.Marshal(&orderv1.ReturnRequested{
			ReturnId: r.ID, OrderId: r.OrderID, OwnerId: r.OwnerID, OccurredAt: now,
		})
	case domain.ReturnApproved:
		u, n := r.RefundTotal.UnitsNanos()
		topic = "commerce.order.return_approved"
		b, err = proto.Marshal(&orderv1.ReturnApproved{
			ReturnId: r.ID, OrderId: r.OrderID, OwnerId: r.OwnerID,
			RefundTotal: &commonv1.Money{CurrencyCode: r.RefundTotal.Currency, Units: u, Nanos: n},
			OccurredAt:  now,
		})
	case domain.ReturnRejected:
		topic = "commerce.order.return_rejected"
		b, err = proto.Marshal(&orderv1.ReturnRejected{
			ReturnId: r.ID, OrderId: r.OrderID, OwnerId: r.OwnerID, OccurredAt: now,
		})
	default:
		return nil
	}
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "marshal return event")
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`, topic, []byte(r.OrderID), b)
	return wrap(err)
}
