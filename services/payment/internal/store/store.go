// Package store persists payment intents and writes payment.* outbox rows in
// the same transaction as the status change.
package store

import (
	"context"
	"embed"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Payment struct {
	ID            string
	OrderID       string
	AmountCents   int64
	Currency      string
	Status        string
	MethodToken   string
	ClientSecret  string
	CreatedAt     time.Time
	RefundedCents int64
}

const cols = `id, order_id, amount_cents, currency, status, method_token, client_secret, created_at, refunded_cents`

func scanPayment(row pgx.Row, p *Payment) error {
	return row.Scan(&p.ID, &p.OrderID, &p.AmountCents, &p.Currency, &p.Status,
		&p.MethodToken, &p.ClientSecret, &p.CreatedAt, &p.RefundedCents)
}

func errNoPayment() error {
	return pkgerrs.New(pkgerrs.KindNotFound, "PAYMENT_NOT_FOUND", "no such payment")
}

// loadForUpdate row-locks and scans a payment, mapping "not found".
func loadForUpdate(ctx context.Context, tx pgx.Tx, id string) (Payment, error) {
	var p Payment
	err := scanPayment(tx.QueryRow(ctx, `SELECT `+cols+` FROM payments WHERE id = $1 FOR UPDATE`, id), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, errNoPayment()
	}
	return p, wrap(err)
}

// Create opens a new intent in REQUIRES_CONFIRMATION.
func (s *Store) Create(ctx context.Context, orderID string, amountCents int64, currency, methodToken string) (*Payment, error) {
	secret := "pi_" + uuid.NewString()
	var p Payment
	err := scanPayment(s.pool.QueryRow(ctx, `
		INSERT INTO payments (order_id, amount_cents, currency, method_token, client_secret)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING `+cols,
		orderID, amountCents, currency, methodToken, secret), &p)
	if err != nil {
		return nil, wrap(err)
	}
	return &p, nil
}

func (s *Store) Get(ctx context.Context, id string) (*Payment, error) {
	var p Payment
	err := scanPayment(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM payments WHERE id = $1`, id), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoPayment()
	}
	if err != nil {
		return nil, wrap(err)
	}
	return &p, nil
}

// Transition sets a new status and writes the matching outbox event, atomically.
// It is idempotent: if the payment is already in `to`, it returns without error.
func (s *Store) Transition(ctx context.Context, id, to, reason string) (*Payment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	p, err := loadForUpdate(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if p.Status == to {
		return &p, nil // idempotent
	}
	if !canTransition(p.Status, to) {
		return nil, pkgerrs.New(pkgerrs.KindFailedPrecondition, "BAD_TRANSITION",
			"cannot move payment from "+p.Status+" to "+to)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE payments SET status = $2, updated_at = now() WHERE id = $1`, id, to); err != nil {
		return nil, wrap(err)
	}
	p.Status = to

	if topic, payload, ok := eventFor(&p, to, reason); ok {
		if _, err := tx.Exec(ctx,
			`INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`,
			topic, []byte(p.OrderID), payload); err != nil {
			return nil, wrap(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return &p, nil
}

func canTransition(from, to string) bool {
	switch from {
	case "REQUIRES_CONFIRMATION":
		return to == "AUTHORIZED" || to == "FAILED" || to == "VOIDED"
	case "AUTHORIZED":
		return to == "VOIDED" // refunds go through Refund(), not Transition()
	default:
		return false
	}
}

// Refund records a refund of amountCents (0 => the full remaining balance)
// against an authorized payment. Partial refunds accumulate; the payment moves
// to REFUNDED only once fully refunded. Repeatable safely: a non-empty
// idemKey that has already been used returns the current payment unchanged.
// Each refund emits one commerce.payment.refunded event for its own amount.
func (s *Store) Refund(ctx context.Context, id string, amountCents int64, idemKey string) (*Payment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	p, err := loadForUpdate(ctx, tx, id)
	if err != nil {
		return nil, err
	}

	done, err := refundAlreadyApplied(ctx, tx, p.ID, idemKey)
	if err != nil {
		return nil, err
	}
	if done {
		return &p, nil
	}

	amountCents, settled, err := resolveRefundAmount(&p, amountCents)
	if err != nil {
		return nil, err
	}
	if settled {
		return &p, nil // nothing left to refund
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO refunds (payment_id, amount_cents, idempotency_key) VALUES ($1,$2,$3)`,
		p.ID, amountCents, idemKey); err != nil {
		return nil, wrap(err)
	}
	p.RefundedCents += amountCents
	newStatus := p.Status
	if p.RefundedCents >= p.AmountCents {
		newStatus = "REFUNDED"
	}
	if _, err := tx.Exec(ctx,
		`UPDATE payments SET refunded_cents = $2, status = $3, updated_at = now() WHERE id = $1`,
		p.ID, p.RefundedCents, newStatus); err != nil {
		return nil, wrap(err)
	}
	p.Status = newStatus

	b, _ := proto.Marshal(&paymentv1.PaymentRefunded{
		PaymentId: p.ID, OrderId: p.OrderID,
		Amount:     money(p.Currency, amountCents),
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if _, err := tx.Exec(ctx,
		`INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`,
		"commerce.payment.refunded", []byte(p.OrderID), b); err != nil {
		return nil, wrap(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return &p, nil
}

// refundAlreadyApplied reports whether a non-empty idemKey has been used for
// this payment.
func refundAlreadyApplied(ctx context.Context, tx pgx.Tx, paymentID, idemKey string) (bool, error) {
	if idemKey == "" {
		return false, nil
	}
	var seen bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM refunds WHERE payment_id = $1 AND idempotency_key = $2)`,
		paymentID, idemKey).Scan(&seen)
	return seen, wrap(err)
}

// resolveRefundAmount validates a refund against the payment and returns the
// effective amount. settled is true when there is nothing left to refund.
func resolveRefundAmount(p *Payment, requested int64) (amount int64, settled bool, err error) {
	if p.Status != "AUTHORIZED" && p.Status != "REFUNDED" {
		return 0, false, pkgerrs.New(pkgerrs.KindFailedPrecondition, "NOT_REFUNDABLE",
			"cannot refund a payment in "+p.Status)
	}
	remaining := p.AmountCents - p.RefundedCents
	if requested <= 0 {
		requested = remaining
	}
	if requested <= 0 {
		return 0, true, nil
	}
	if requested > remaining {
		return 0, false, pkgerrs.New(pkgerrs.KindFailedPrecondition, "REFUND_EXCEEDS_BALANCE",
			"refund exceeds the unrefunded balance")
	}
	return requested, false, nil
}

func money(currency string, cents int64) *commonv1.Money {
	return &commonv1.Money{
		CurrencyCode: currency,
		Units:        cents / 100,
		Nanos:        int32(cents%100) * 10_000_000,
	}
}

func eventFor(p *Payment, to, reason string) (string, []byte, bool) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	amount := &commonv1.Money{
		CurrencyCode: p.Currency,
		Units:        p.AmountCents / 100,
		Nanos:        int32(p.AmountCents%100) * 10_000_000,
	}
	switch to {
	case "AUTHORIZED":
		b, _ := proto.Marshal(&paymentv1.PaymentAuthorized{
			PaymentId: p.ID, OrderId: p.OrderID, Amount: amount, OccurredAt: now,
		})
		return "commerce.payment.authorized", b, true
	case "FAILED":
		b, _ := proto.Marshal(&paymentv1.PaymentFailed{
			PaymentId: p.ID, OrderId: p.OrderID, Reason: reason, OccurredAt: now,
		})
		return "commerce.payment.failed", b, true
	default:
		return "", nil, false
	}
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return pkgerrs.Wrap(err, pkgerrs.KindInternal, "DB_ERROR", "database error: "+err.Error())
}
