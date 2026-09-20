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
	"github.com/deeprath/commerce-platform/pkg/sqlfilter"
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

// returnCols is the returns projection scanReturn expects, shared by the
// single-row and list paths so they cannot drift apart.
const returnCols = `id, order_id, owner_id, status, reason, currency, refund_total_cents,
	decided_by, decision_note, created_at, updated_at`

// scanReturn reads one returns row. pgx.Rows satisfies pgx.Row, so this serves
// both QueryRow and a row-at-a-time loop over a result set.
func scanReturn(row pgx.Row) (*domain.Return, error) {
	var (
		r       domain.Return
		st, cur string
		tot     int64
	)
	if err := row.Scan(&r.ID, &r.OrderID, &r.OwnerID, &st, &r.Reason, &cur, &tot,
		&r.DecidedBy, &r.DecisionNote, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Status = domain.ReturnStatus(st)
	r.RefundTotal = domain.Money{Currency: cur, Cents: tot}
	return &r, nil
}

// attachReturnLines loads the lines for every given return in one query, rather
// than a round trip each. Line currency comes from the return's own total.
func attachReturnLines(ctx context.Context, q querier, returns []*domain.Return) error {
	if len(returns) == 0 {
		return nil
	}
	ids := make([]string, len(returns))
	byID := make(map[string]*domain.Return, len(returns))
	for i, r := range returns {
		ids[i] = r.ID
		byID[r.ID] = r
	}
	rows, err := q.Query(ctx, `
		SELECT return_id, product_id, quantity, refund_amount_cents
		FROM return_lines WHERE return_id = ANY($1) ORDER BY return_id, product_id`, ids)
	if err != nil {
		return wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			returnID string
			l        domain.ReturnLine
			amt      int64
		)
		if err := rows.Scan(&returnID, &l.ProductID, &l.Quantity, &amt); err != nil {
			return wrap(err)
		}
		r, ok := byID[returnID]
		if !ok {
			continue
		}
		l.RefundAmount = domain.Money{Currency: r.RefundTotal.Currency, Cents: amt}
		r.Lines = append(r.Lines, l)
	}
	return wrap(rows.Err())
}

func (s *Store) getReturn(ctx context.Context, q querier, id, ownerID string) (*domain.Return, error) {
	if err := notFoundID(id, "RETURN_NOT_FOUND"); err != nil {
		return nil, err
	}
	r, err := scanReturn(q.QueryRow(ctx, `SELECT `+returnCols+` FROM returns WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "RETURN_NOT_FOUND", "no such return")
	}
	if err != nil {
		return nil, wrap(err)
	}
	if ownerID != "" && r.OwnerID != ownerID {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "RETURN_NOT_FOUND", "no such return")
	}
	if err := attachReturnLines(ctx, q, []*domain.Return{r}); err != nil {
		return nil, err
	}
	return r, nil
}

// ListReturns returns returns newest-first, keyset-paginated by created_at.
// ownerID "" lists every customer's returns (caller-gated by role in grpcsvc);
// status "" means any status.
func (s *Store) ListReturns(ctx context.Context, ownerID, status string, limit int, before string) ([]*domain.Return, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor := time.Now().Add(time.Hour)
	if before != "" {
		if t, err := time.Parse(time.RFC3339Nano, before); err == nil {
			cursor = t
		}
	}
	var f sqlfilter.Filters
	f.Add("created_at < $%d", cursor)
	f.AddNonEmpty("owner_id = $%d", ownerID)
	f.AddNonEmpty("status = $%d", status)
	// Rows, not ids to re-fetch: 2 queries per page instead of 1 + 2N. The
	// per-row owner check getReturn applied is already in the WHERE clause
	// above when ownerID is set.
	rows, err := s.pool.Query(ctx, "SELECT "+returnCols+" FROM returns"+f.Where()+
		" ORDER BY created_at DESC LIMIT "+f.Placeholder(limit+1), f.Args()...)
	if err != nil {
		return nil, "", wrap(err)
	}
	out := make([]*domain.Return, 0, limit+1)
	for rows.Next() {
		r, err := scanReturn(rows)
		if err != nil {
			rows.Close()
			return nil, "", wrap(err)
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", wrap(err)
	}

	// Trim before loading lines, so the look-ahead row doesn't pull lines that
	// are never returned.
	next := ""
	if len(out) > limit {
		next = out[limit-1].CreatedAt.Format(time.RFC3339Nano)
		out = out[:limit]
	}
	if err := attachReturnLines(ctx, s.pool, out); err != nil {
		return nil, "", err
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
	if err := notFoundID(id, "RETURN_NOT_FOUND"); err != nil {
		return nil, err
	}
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
		shopRefunds, sErr := shopRefundsFor(ctx, tx, r)
		if sErr != nil {
			return sErr
		}
		topic = "commerce.order.return_approved"
		b, err = proto.Marshal(&orderv1.ReturnApproved{
			ReturnId: r.ID, OrderId: r.OrderID, OwnerId: r.OwnerID,
			RefundTotal: &commonv1.Money{CurrencyCode: r.RefundTotal.Currency, Units: u, Nanos: n},
			OccurredAt:  now,
			ShopRefunds: shopRefunds,
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

// shopRefundsFor splits an approved return's refund across the shops that own
// the returned lines. The order service is the only place that holds the line
// -> shop mapping, so the breakdown is computed here and carried on the event
// rather than left for a consumer to re-derive — commerce.payout.v1 reverses a
// shop's payout against exactly these amounts.
//
// Lines whose product is no longer on the order are impossible (return_lines
// are created from the order's own lines), but the join is written as an inner
// join anyway: a refund that cannot be attributed to a shop must not silently
// become a first-party one.
func shopRefundsFor(ctx context.Context, tx pgx.Tx, r *domain.Return) ([]*orderv1.ShopRefund, error) {
	rows, err := tx.Query(ctx, `
		SELECT ol.shop_id, SUM(rl.refund_amount_cents)::BIGINT
		FROM return_lines rl
		JOIN order_lines ol
		  ON ol.order_id = $2 AND ol.product_id = rl.product_id
		WHERE rl.return_id = $1
		GROUP BY ol.shop_id
		ORDER BY ol.shop_id`, r.ID, r.OrderID)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()

	var out []*orderv1.ShopRefund
	for rows.Next() {
		var shopID string
		var cents int64
		if err := rows.Scan(&shopID, &cents); err != nil {
			return nil, wrap(err)
		}
		m := domain.Money{Currency: r.RefundTotal.Currency, Cents: cents}
		u, n := m.UnitsNanos()
		out = append(out, &orderv1.ShopRefund{
			ShopId: shopID,
			Amount: &commonv1.Money{CurrencyCode: m.Currency, Units: u, Nanos: n},
		})
	}
	return out, wrap(rows.Err())
}
