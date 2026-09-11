package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	payoutv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payout/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/fga"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/payout/internal/domain"
	"github.com/deeprath/commerce-platform/services/payout/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/payout/internal/store"
)

// fakeFGA — exact-tuple only; enough for the payout service's Check path.
type fakeFGA struct{ t map[string]bool }

func newFakeFGA() *fakeFGA { return &fakeFGA{t: map[string]bool{}} }

func k(u, r, o string) string { return u + "|" + r + "|" + o }

func (f *fakeFGA) Check(_ context.Context, u, r, o string) (bool, error) { return f.t[k(u, r, o)], nil }
func (f *fakeFGA) Write(context.Context, string, string, string) error   { return nil }
func (f *fakeFGA) Delete(context.Context, string, string, string) error  { return nil }
func (f *fakeFGA) Read(context.Context, string) ([]fga.Tuple, error)     { return nil, nil }

func (f *fakeFGA) grantStaff(sub, shop string) {
	f.t[k(fga.UserObject(sub), fga.RelationStaff, fga.ShopObject(shop))] = true
}

func newSrv(t *testing.T, f fga.API) (*grpcsvc.Server, *store.Store) {
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
	st := store.New(pool)
	return grpcsvc.New(st, f), st
}

func usd(cents int64) domain.Money { return domain.Money{Currency: "USD", Cents: cents} }

func staffOf(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer"}})
}
func financeUser(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer", "finance"}})
}

func TestListPayouts_ShopStaffSeesOwnShopOnly(t *testing.T) {
	f := newFakeFGA()
	s, st := newSrv(t, f)
	f.grantStaff("seller-a", "shop-a")

	if _, err := st.CreateFromOrder(context.Background(), "order-1",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: usd(1500)}}, ""); err != nil {
		t.Fatalf("seed shop-a: %v", err)
	}
	if _, err := st.CreateFromOrder(context.Background(), "order-2",
		[]store.ShopAmount{{ShopID: "shop-b", Amount: usd(1500)}}, ""); err != nil {
		t.Fatalf("seed shop-b: %v", err)
	}

	res, err := s.ListPayouts(staffOf("seller-a"), &payoutv1.ListPayoutsRequest{ShopId: "shop-a"})
	if err != nil {
		t.Fatalf("list own shop: %v", err)
	}
	if len(res.GetPayouts()) != 1 || res.GetPayouts()[0].GetShopId() != "shop-a" {
		t.Fatalf("wrong payouts returned: %+v", res.GetPayouts())
	}

	// Not staff of shop-b -> denied.
	if _, err := s.ListPayouts(staffOf("seller-a"), &payoutv1.ListPayoutsRequest{ShopId: "shop-b"}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("cross-shop list: want PermissionDenied, got %v", err)
	}

	// No shop_id and no finance role -> invalid argument.
	if _, err := s.ListPayouts(staffOf("seller-a"), &payoutv1.ListPayoutsRequest{}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("missing shop_id: want InvalidArgument, got %v", err)
	}
}

func TestListPayouts_FinanceSeesEveryShop(t *testing.T) {
	f := newFakeFGA()
	s, st := newSrv(t, f)
	if _, err := st.CreateFromOrder(context.Background(), "order-1", []store.ShopAmount{{ShopID: "shop-a", Amount: usd(1500)}}, ""); err != nil {
		t.Fatalf("seed shop-a: %v", err)
	}
	if _, err := st.CreateFromOrder(context.Background(), "order-2", []store.ShopAmount{{ShopID: "shop-b", Amount: usd(1500)}}, ""); err != nil {
		t.Fatalf("seed shop-b: %v", err)
	}

	res, err := s.ListPayouts(financeUser("ops"), &payoutv1.ListPayoutsRequest{})
	if err != nil || len(res.GetPayouts()) != 2 {
		t.Fatalf("finance list: %v n=%d", err, len(res.GetPayouts()))
	}
}

func TestGetPayout_ShopStaffAndCrossShopDenied(t *testing.T) {
	f := newFakeFGA()
	s, st := newSrv(t, f)
	f.grantStaff("seller-a", "shop-a")
	created, _ := st.CreateFromOrder(context.Background(), "order-1",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: usd(1500)}}, "")
	id := created[0].ID

	if got, err := s.GetPayout(staffOf("seller-a"), &payoutv1.GetPayoutRequest{Id: id}); err != nil || got.GetId() != id {
		t.Fatalf("owner get: %v %+v", err, got)
	}
	if _, err := s.GetPayout(staffOf("someone-else"), &payoutv1.GetPayoutRequest{Id: id}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("non-staff get: want PermissionDenied, got %v", err)
	}
	if _, err := s.GetPayout(context.Background(), &payoutv1.GetPayoutRequest{Id: id}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("anon get: want Unauthenticated, got %v", err)
	}
}

func TestMarkPaid_RoleGated(t *testing.T) {
	f := newFakeFGA()
	s, st := newSrv(t, f)
	created, _ := st.CreateFromOrder(context.Background(), "order-1",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: usd(1500)}}, "")
	id := created[0].ID

	if _, err := s.MarkPaid(staffOf("seller-a"), &payoutv1.MarkPaidRequest{Id: id}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("non-finance MarkPaid: want PermissionDenied, got %v", err)
	}
	paid, err := s.MarkPaid(financeUser("ops"), &payoutv1.MarkPaidRequest{Id: id})
	if err != nil || paid.GetStatus() != payoutv1.PayoutStatus_PAYOUT_STATUS_PAID {
		t.Fatalf("finance MarkPaid: %v %+v", err, paid)
	}
}
