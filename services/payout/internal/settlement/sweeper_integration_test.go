package settlement

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/payout/internal/domain"
	"github.com/deeprath/commerce-platform/services/payout/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("payout"),
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

func TestSweep_MarksDuePayoutsPaid(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	created, err := st.CreateFromOrder(ctx, "order-1",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: domain.Money{Currency: "USD", Cents: 1000}}}, "e:0:1")
	if err != nil || len(created) != 1 {
		t.Fatalf("seed: %v %+v", err, created)
	}
	id := created[0].ID

	// A long threshold leaves it untouched.
	slow := New(st, time.Minute, time.Hour)
	slow.sweep(ctx)
	got, err := st.Get(ctx, id, "")
	if err != nil || got.Status != domain.StatusPending {
		t.Fatalf("after slow sweep: %v %+v", err, got)
	}

	// A zero threshold settles it.
	fast := New(st, time.Minute, 0)
	fast.sweep(ctx)
	got, err = st.Get(ctx, id, "")
	if err != nil || got.Status != domain.StatusPaid {
		t.Fatalf("after fast sweep: %v %+v", err, got)
	}
}

// Run ticks on its interval and stops cleanly when the context is cancelled.
func TestRun_TicksThenStops(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	created, err := st.CreateFromOrder(ctx, "order-run",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: domain.Money{Currency: "USD", Cents: 1000}}}, "e:9:1")
	if err != nil || len(created) != 1 {
		t.Fatalf("seed: %v %+v", err, created)
	}
	id := created[0].ID

	runCtx, cancel := context.WithCancel(ctx)
	sw := New(st, 20*time.Millisecond, 0)
	done := make(chan error, 1)
	go func() { done <- sw.Run(runCtx) }()

	deadline := time.After(3 * time.Second)
	for {
		got, _ := st.Get(ctx, id, "")
		if got != nil && got.Status == domain.StatusPaid {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run did not settle the payout in time")
		case <-time.After(25 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("Run returned nil, want context.Canceled")
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
