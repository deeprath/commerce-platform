package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/inventory/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/inventory/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("inventory"),
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

func newSrv(t *testing.T) (*grpcsvc.Server, *store.Store) {
	st := store.New(spinUp(t))
	return grpcsvc.New(st), st
}

func TestCheckAvailability_ReturnsLevelsPerProduct(t *testing.T) {
	ctx := context.Background()
	s, st := newSrv(t)
	if err := st.EnsureStockRow(ctx, "p1", 10); err != nil {
		t.Fatal(err)
	}

	resp, err := s.CheckAvailability(ctx, &inventoryv1.CheckAvailabilityRequest{ProductIds: []string{"p1", "p2"}})
	if err != nil {
		t.Fatalf("CheckAvailability: %v", err)
	}
	byID := map[string]*inventoryv1.StockLevel{}
	for _, l := range resp.GetLevels() {
		byID[l.GetProductId()] = l
	}
	if byID["p1"].GetOnHand() != 10 || byID["p1"].GetAvailable() != 10 {
		t.Fatalf("p1 = %+v", byID["p1"])
	}
	if byID["p2"].GetOnHand() != 0 || byID["p2"].GetAvailable() != 0 {
		t.Fatalf("p2 (never seeded) = %+v, want all zero", byID["p2"])
	}
}

func TestReserve_ValidationErrors(t *testing.T) {
	ctx := context.Background()
	s, _ := newSrv(t)

	cases := []*inventoryv1.ReserveRequest{
		{OrderRef: "", Items: []*inventoryv1.LineItem{{ProductId: "p1", Quantity: 1}}},
		{OrderRef: "order-1", Items: nil},
		{OrderRef: "order-1", Items: []*inventoryv1.LineItem{{ProductId: "p1", Quantity: 0}}},
	}
	for _, req := range cases {
		if _, err := s.Reserve(ctx, req); !errs.Is(err, errs.KindInvalidArgument) {
			t.Fatalf("Reserve(%+v): err = %v, want KindInvalidArgument", req, err)
		}
	}
}

func TestReserve_InsufficientStockIsFailedPrecondition(t *testing.T) {
	ctx := context.Background()
	s, st := newSrv(t)
	if err := st.EnsureStockRow(ctx, "p1", 2); err != nil {
		t.Fatal(err)
	}
	_, err := s.Reserve(ctx, &inventoryv1.ReserveRequest{
		OrderRef: "order-1", Items: []*inventoryv1.LineItem{{ProductId: "p1", Quantity: 5}},
	})
	if !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("err = %v, want KindFailedPrecondition", err)
	}
}

func TestReserve_TTLDefaultedAndClamped(t *testing.T) {
	ctx := context.Background()
	s, st := newSrv(t)
	if err := st.EnsureStockRow(ctx, "p1", 100); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		ttlSecs int32
		want    time.Duration
	}{
		{"zero defaults to 15m", 0, 15 * time.Minute},
		{"too small clamps to 60s", 1, 60 * time.Second},
		{"too large clamps to 60m", 24 * 60 * 60, 60 * time.Minute},
		{"within range kept as-is", 300, 300 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now()
			res, err := s.Reserve(ctx, &inventoryv1.ReserveRequest{
				OrderRef: "order-" + tc.name, TtlSeconds: tc.ttlSecs,
				Items: []*inventoryv1.LineItem{{ProductId: "p1", Quantity: 1}},
			})
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			expiresAt, err := time.Parse(time.RFC3339, res.GetExpiresAt())
			if err != nil {
				t.Fatalf("bad expires_at %q: %v", res.GetExpiresAt(), err)
			}
			got := expiresAt.Sub(before)
			// Generous tolerance — this only needs to prove the right bucket,
			// not race the clock.
			if got < tc.want-5*time.Second || got > tc.want+5*time.Second {
				t.Fatalf("expires_at - now = %v, want ~%v", got, tc.want)
			}
		})
	}
}

func TestReserveCommitRelease_MoveStockAsExpected(t *testing.T) {
	ctx := context.Background()
	s, st := newSrv(t)
	if err := st.EnsureStockRow(ctx, "p1", 10); err != nil {
		t.Fatal(err)
	}

	res, err := s.Reserve(ctx, &inventoryv1.ReserveRequest{
		OrderRef: "order-1", Items: []*inventoryv1.LineItem{{ProductId: "p1", Quantity: 3}},
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	afterReserve := level(t, s, ctx, "p1")
	if afterReserve.GetOnHand() != 10 || afterReserve.GetReserved() != 3 || afterReserve.GetAvailable() != 7 {
		t.Fatalf("after reserve: %+v", afterReserve)
	}

	if _, err := s.Commit(ctx, &inventoryv1.CommitRequest{ReservationId: res.GetReservationId()}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	afterCommit := level(t, s, ctx, "p1")
	if afterCommit.GetOnHand() != 7 || afterCommit.GetReserved() != 0 {
		t.Fatalf("after commit: %+v, want on_hand=7 reserved=0", afterCommit)
	}
}

func TestReserveThenRelease_ReturnsTheHold(t *testing.T) {
	ctx := context.Background()
	s, st := newSrv(t)
	if err := st.EnsureStockRow(ctx, "p1", 10); err != nil {
		t.Fatal(err)
	}

	res, err := s.Reserve(ctx, &inventoryv1.ReserveRequest{
		OrderRef: "order-1", Items: []*inventoryv1.LineItem{{ProductId: "p1", Quantity: 4}},
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	if _, err := s.Release(ctx, &inventoryv1.ReleaseRequest{ReservationId: res.GetReservationId()}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	after := level(t, s, ctx, "p1")
	if after.GetOnHand() != 10 || after.GetReserved() != 0 {
		t.Fatalf("after release: %+v, want on_hand=10 reserved=0 (fully returned)", after)
	}
}

func TestCommitAndRelease_UnknownReservationIsANoOp(t *testing.T) {
	ctx := context.Background()
	s, _ := newSrv(t)
	if _, err := s.Commit(ctx, &inventoryv1.CommitRequest{ReservationId: "does-not-exist"}); err != nil {
		t.Fatalf("Commit on an unknown reservation should be a no-op, not an error: %v", err)
	}
	if _, err := s.Release(ctx, &inventoryv1.ReleaseRequest{ReservationId: "does-not-exist"}); err != nil {
		t.Fatalf("Release on an unknown reservation should be a no-op, not an error: %v", err)
	}
}

func TestAdjustStock_RequiresTheInventoryManagerRole(t *testing.T) {
	s, _ := newSrv(t)
	req := &inventoryv1.AdjustStockRequest{ProductId: "p1", Delta: 5}

	if _, err := s.AdjustStock(context.Background(), req); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("no principal: err = %v, want KindUnauthenticated", err)
	}

	noRole := auth.WithPrincipal(context.Background(), &auth.Principal{Subject: "u1", Roles: []string{"customer"}})
	if _, err := s.AdjustStock(noRole, req); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("wrong role: err = %v, want KindPermissionDenied", err)
	}

	withRole := auth.WithPrincipal(context.Background(), &auth.Principal{Subject: "u1", Roles: []string{"inventory_manager"}})
	l, err := s.AdjustStock(withRole, req)
	if err != nil {
		t.Fatalf("with role: %v", err)
	}
	if l.GetOnHand() != 5 {
		t.Fatalf("on_hand = %d, want 5", l.GetOnHand())
	}
}

func TestAdjustStock_RequiresAProductID(t *testing.T) {
	withRole := auth.WithPrincipal(context.Background(), &auth.Principal{Subject: "u1", Roles: []string{"inventory_manager"}})
	s, _ := newSrv(t)
	_, err := s.AdjustStock(withRole, &inventoryv1.AdjustStockRequest{Delta: 1})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("err = %v, want KindInvalidArgument", err)
	}
}

func TestAdjustStock_NeverGoesBelowZero(t *testing.T) {
	withRole := auth.WithPrincipal(context.Background(), &auth.Principal{Subject: "u1", Roles: []string{"inventory_manager"}})
	s, st := newSrv(t)
	if err := st.EnsureStockRow(context.Background(), "p1", 2); err != nil {
		t.Fatal(err)
	}

	l, err := s.AdjustStock(withRole, &inventoryv1.AdjustStockRequest{ProductId: "p1", Delta: -10})
	if err != nil {
		t.Fatalf("AdjustStock: %v", err)
	}
	if l.GetOnHand() != 0 {
		t.Fatalf("on_hand = %d, want clamped to 0", l.GetOnHand())
	}
}

// --- helpers -------------------------------------------------------------

func level(t *testing.T, s *grpcsvc.Server, ctx context.Context, productID string) *inventoryv1.StockLevel {
	t.Helper()
	resp, err := s.CheckAvailability(ctx, &inventoryv1.CheckAvailabilityRequest{ProductIds: []string{productID}})
	if err != nil {
		t.Fatalf("CheckAvailability: %v", err)
	}
	for _, l := range resp.GetLevels() {
		if l.GetProductId() == productID {
			return l
		}
	}
	t.Fatalf("no level returned for %s", productID)
	return nil
}
