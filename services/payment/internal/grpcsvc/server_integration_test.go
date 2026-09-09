package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/payment/internal/grpcsvc"
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

func usd(units int64, nanos int32) *commonv1.Money {
	return &commonv1.Money{CurrencyCode: "USD", Units: units, Nanos: nanos}
}

func TestRefundRPC_PartialThenFull(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	s := grpcsvc.New(st)

	created, err := s.CreatePayment(ctx, &paymentv1.CreatePaymentRequest{
		OrderId: "o1", Amount: usd(100, 0), MethodToken: "pm_card_ok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.ConfirmPayment(ctx, &paymentv1.ConfirmPaymentRequest{
		PaymentId: created.GetPaymentId(), Outcome: paymentv1.ConfirmPaymentRequest_OUTCOME_AUTHORIZE,
	}); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	// Partial refund: status stays AUTHORIZED, `refunded` is populated.
	p1, err := s.Refund(ctx, &paymentv1.RefundRequest{PaymentId: created.GetPaymentId(), Amount: usd(40, 0), IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("partial refund: %v", err)
	}
	if p1.GetStatus() != paymentv1.PaymentStatus_PAYMENT_STATUS_AUTHORIZED ||
		p1.GetRefunded().GetUnits() != 40 {
		t.Fatalf("after partial: status=%v refunded=%v", p1.GetStatus(), p1.GetRefunded())
	}
	// Retried idempotency key: no double refund.
	again, _ := s.Refund(ctx, &paymentv1.RefundRequest{PaymentId: created.GetPaymentId(), Amount: usd(40, 0), IdempotencyKey: "k1"})
	if again.GetRefunded().GetUnits() != 40 {
		t.Fatalf("idempotent retry changed refunded to %v", again.GetRefunded())
	}

	// Amount 0 => refund the remainder; status -> REFUNDED.
	p2, err := s.Refund(ctx, &paymentv1.RefundRequest{PaymentId: created.GetPaymentId(), IdempotencyKey: "k2"})
	if err != nil {
		t.Fatalf("remainder refund: %v", err)
	}
	if p2.GetStatus() != paymentv1.PaymentStatus_PAYMENT_STATUS_REFUNDED || p2.GetRefunded().GetUnits() != 100 {
		t.Fatalf("after remainder: status=%v refunded=%v", p2.GetStatus(), p2.GetRefunded())
	}

	// Negative amount is rejected.
	if _, err := s.Refund(ctx, &paymentv1.RefundRequest{PaymentId: created.GetPaymentId(), Amount: usd(-1, 0)}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("negative amount: want InvalidArgument, got %v", err)
	}
}
