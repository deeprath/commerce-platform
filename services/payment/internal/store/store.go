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
	ID           string
	OrderID      string
	AmountCents  int64
	Currency     string
	Status       string
	MethodToken  string
	ClientSecret string
	CreatedAt    time.Time
}

const cols = `id, order_id, amount_cents, currency, status, method_token, client_secret, created_at`

// Create opens a new intent in REQUIRES_CONFIRMATION.
func (s *Store) Create(ctx context.Context, orderID string, amountCents int64, currency, methodToken string) (*Payment, error) {
	secret := "pi_" + uuid.NewString()
	var p Payment
	err := s.pool.QueryRow(ctx, `
		INSERT INTO payments (order_id, amount_cents, currency, method_token, client_secret)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING `+cols,
		orderID, amountCents, currency, methodToken, secret).
		Scan(&p.ID, &p.OrderID, &p.AmountCents, &p.Currency, &p.Status, &p.MethodToken, &p.ClientSecret, &p.CreatedAt)
	if err != nil {
		return nil, wrap(err)
	}
	return &p, nil
}

func (s *Store) Get(ctx context.Context, id string) (*Payment, error) {
	var p Payment
	err := s.pool.QueryRow(ctx, `SELECT `+cols+` FROM payments WHERE id = $1`, id).
		Scan(&p.ID, &p.OrderID, &p.AmountCents, &p.Currency, &p.Status, &p.MethodToken, &p.ClientSecret, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "PAYMENT_NOT_FOUND", "no such payment")
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

	var p Payment
	err = tx.QueryRow(ctx, `SELECT `+cols+` FROM payments WHERE id = $1 FOR UPDATE`, id).
		Scan(&p.ID, &p.OrderID, &p.AmountCents, &p.Currency, &p.Status, &p.MethodToken, &p.ClientSecret, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "PAYMENT_NOT_FOUND", "no such payment")
	}
	if err != nil {
		return nil, wrap(err)
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
		return to == "REFUNDED" || to == "VOIDED"
	default:
		return false
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
	case "REFUNDED":
		b, _ := proto.Marshal(&paymentv1.PaymentRefunded{
			PaymentId: p.ID, OrderId: p.OrderID, Amount: amount, OccurredAt: now,
		})
		return "commerce.payment.refunded", b, true
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
