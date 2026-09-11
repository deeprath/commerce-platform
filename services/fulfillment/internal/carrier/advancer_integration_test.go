package carrier

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/domain"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("fulfillment"),
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

func TestAdvancerSweep_DrivesPendingToDelivered(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	created, err := st.CreateFromOrder(ctx, "order-1", "owner-1",
		domain.Address{FullName: "Buyer"},
		[]store.ShopItems{{Items: []domain.Item{{ProductID: "p1", Quantity: 1}}}}, "e:0:1")
	if err != nil || len(created) != 1 {
		t.Fatalf("seed: %v %+v", err, created)
	}
	sh := created[0]

	// Zero delays => every due shipment advances one step per sweep.
	a := New(st, time.Minute, 0, 0)

	a.sweep(ctx) // PENDING -> SHIPPED
	got, _ := st.Get(ctx, sh.ID, "")
	if got.Status != domain.StatusShipped {
		t.Fatalf("after first sweep: %s, want SHIPPED", got.Status)
	}
	if got.Carrier != "SANDBOX" || got.TrackingNumber == "" {
		t.Fatalf("sandbox carrier should set carrier + tracking: %+v", got)
	}

	a.sweep(ctx) // SHIPPED -> DELIVERED
	got, _ = st.Get(ctx, sh.ID, "")
	if got.Status != domain.StatusDelivered {
		t.Fatalf("after second sweep: %s, want DELIVERED", got.Status)
	}

	a.sweep(ctx) // nothing left to do
	got, _ = st.Get(ctx, sh.ID, "")
	if got.Status != domain.StatusDelivered {
		t.Fatalf("delivered shipment must stay put: %s", got.Status)
	}

	// A long threshold leaves a fresh PENDING shipment untouched.
	fresh, err := st.CreateFromOrder(ctx, "order-2", "owner-2",
		domain.Address{}, []store.ShopItems{{}}, "e:0:2")
	if err != nil || len(fresh) != 1 {
		t.Fatalf("seed fresh: %v %+v", err, fresh)
	}
	slow := New(st, time.Minute, time.Hour, time.Hour)
	slow.sweep(ctx)
	got, _ = st.Get(ctx, fresh[0].ID, "")
	if got.Status != domain.StatusPending {
		t.Fatalf("fresh shipment advanced despite 1h threshold: %s", got.Status)
	}
}

// Run ticks on its interval and stops cleanly when the context is cancelled.
func TestAdvancerRun_TicksThenStops(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	created, err := st.CreateFromOrder(ctx, "order-run", "owner-run",
		domain.Address{}, []store.ShopItems{{Items: []domain.Item{{ProductID: "p1", Quantity: 1}}}}, "e:9:1")
	if err != nil || len(created) != 1 {
		t.Fatalf("seed: %v %+v", err, created)
	}
	sh := created[0]

	runCtx, cancel := context.WithCancel(ctx)
	a := New(st, 20*time.Millisecond, 0, 0)
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()

	deadline := time.After(3 * time.Second)
	for {
		got, _ := st.Get(ctx, sh.ID, "")
		if got.Status == domain.StatusDelivered {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("Run did not advance the shipment to DELIVERED in time (at %s)", got.Status)
		case <-time.After(25 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
