package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/proto"

	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/payment/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("payment"),
		tcpostgres.WithUsername("t"), tcpostgres.WithPassword("t"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.Migrate(ctx, dsn, store.Migrations, "migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgx.NewPool(ctx, pgx.PoolConfig{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// authorized creates a payment and drives it to AUTHORIZED (amountCents total).
func authorized(t *testing.T, st *store.Store, amountCents int64) *store.Payment {
	t.Helper()
	ctx := context.Background()
	p, err := st.Create(ctx, "order-1", amountCents, "USD", "pm_card_ok")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p, err = st.Transition(ctx, p.ID, "AUTHORIZED", "")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	return p
}

func TestRefund_PartialAccumulatesThenRefunded(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	p := authorized(t, st, 10000) // $100.00

	r1, err := st.Refund(ctx, p.ID, 3000, "ret-a")
	if err != nil {
		t.Fatalf("first partial refund: %v", err)
	}
	if r1.RefundedCents != 3000 || r1.Status != "AUTHORIZED" {
		t.Fatalf("after partial: %+v", r1)
	}

	// Second partial takes it to the full amount -> REFUNDED.
	r2, err := st.Refund(ctx, p.ID, 7000, "ret-b")
	if err != nil {
		t.Fatalf("second partial refund: %v", err)
	}
	if r2.RefundedCents != 10000 || r2.Status != "REFUNDED" {
		t.Fatalf("after full: %+v", r2)
	}

	// Over-refund is refused.
	if _, err := st.Refund(ctx, p.ID, 100, "ret-c"); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("over-refund: want FailedPrecondition, got %v", err)
	}

	// One payment.refunded event per successful refund, each for its own amount.
	rows, err := pool.Query(ctx, `SELECT payload FROM outbox WHERE topic='commerce.payment.refunded' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var amounts []int64
	for rows.Next() {
		var b []byte
		_ = rows.Scan(&b)
		var e paymentv1.PaymentRefunded
		if err := proto.Unmarshal(b, &e); err != nil {
			t.Fatalf("payload: %v", err)
		}
		amounts = append(amounts, e.GetAmount().GetUnits()*100+int64(e.GetAmount().GetNanos())/10_000_000)
	}
	if len(amounts) != 2 || amounts[0] != 3000 || amounts[1] != 7000 {
		t.Fatalf("refund event amounts = %v, want [3000 7000]", amounts)
	}
}

func TestRefund_IdempotencyKeyAndFullBalanceDefault(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	p := authorized(t, st, 5000)

	// Same key twice -> applied once.
	if _, err := st.Refund(ctx, p.ID, 2000, "key-1"); err != nil {
		t.Fatalf("refund: %v", err)
	}
	again, err := st.Refund(ctx, p.ID, 2000, "key-1")
	if err != nil {
		t.Fatalf("retry same key: %v", err)
	}
	if again.RefundedCents != 2000 {
		t.Fatalf("idempotency key not honoured: refunded=%d", again.RefundedCents)
	}

	// amount 0 => refund the remaining balance.
	done, err := st.Refund(ctx, p.ID, 0, "key-2")
	if err != nil {
		t.Fatalf("refund remainder: %v", err)
	}
	if done.RefundedCents != 5000 || done.Status != "REFUNDED" {
		t.Fatalf("after remainder: %+v", done)
	}

	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE payment_id = $1`, p.ID).Scan(&n)
	if n != 2 {
		t.Fatalf("refunds rows = %d, want 2", n)
	}
}

func TestRefund_NotRefundableAndMissing(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	// A REQUIRES_CONFIRMATION payment can't be refunded.
	p, _ := st.Create(ctx, "o", 1000, "USD", "pm_card_ok")
	if _, err := st.Refund(ctx, p.ID, 100, ""); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("refund unauthorized payment: want FailedPrecondition, got %v", err)
	}
	if _, err := st.Refund(ctx, "00000000-0000-0000-0000-000000000000", 100, ""); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("refund missing: want NotFound, got %v", err)
	}
}
