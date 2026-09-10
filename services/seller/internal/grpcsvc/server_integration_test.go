package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	sellerv1 "github.com/deeprath/commerce-platform/gen/go/commerce/seller/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/seller/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/seller/internal/store"
)

func newSrv(t *testing.T) *grpcsvc.Server {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("seller"),
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
	return grpcsvc.New(store.New(pool))
}

func user(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer"}})
}
func admin(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer", "shop_admin"}})
}

func TestSeller_OnboardingLifecycle(t *testing.T) {
	s := newSrv(t)

	// A signed-in user opens a shop; it starts PENDING_REVIEW.
	sh, err := s.CreateShop(user("seller-1"), &sellerv1.CreateShopRequest{Name: "Aloe Vera Depot", Description: "plants"})
	if err != nil {
		t.Fatalf("CreateShop: %v", err)
	}
	if sh.GetStatus() != sellerv1.ShopStatus_SHOP_STATUS_PENDING_REVIEW || sh.GetSlug() != "aloe-vera-depot" {
		t.Fatalf("shop = %+v", sh)
	}

	// Anonymous: a pending shop does not exist.
	if _, err := s.GetShop(context.Background(),
		&sellerv1.GetShopRequest{Selector: &sellerv1.GetShopRequest_Slug{Slug: "aloe-vera-depot"}}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("anon GetShop(pending) err = %v, want NotFound", err)
	}
	// The owner can always see their own.
	if _, err := s.GetMyShop(user("seller-1"), &sellerv1.GetMyShopRequest{}); err != nil {
		t.Fatalf("GetMyShop: %v", err)
	}

	// A plain user cannot list or activate.
	if _, err := s.ListShops(user("nosy"), &sellerv1.ListShopsRequest{}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("ListShops as customer err = %v, want PermissionDenied", err)
	}
	if _, err := s.ActivateShop(user("nosy"), &sellerv1.ActivateShopRequest{Id: sh.GetId()}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("ActivateShop as customer err = %v, want PermissionDenied", err)
	}

	// An operator activates it.
	act, err := s.ActivateShop(admin("op"), &sellerv1.ActivateShopRequest{Id: sh.GetId()})
	if err != nil || act.GetStatus() != sellerv1.ShopStatus_SHOP_STATUS_ACTIVE {
		t.Fatalf("ActivateShop: %v / %+v", err, act)
	}

	// Now it's public.
	pub, err := s.GetShop(context.Background(),
		&sellerv1.GetShopRequest{Selector: &sellerv1.GetShopRequest_Slug{Slug: "aloe-vera-depot"}})
	if err != nil || pub.GetId() != sh.GetId() {
		t.Fatalf("public GetShop: %v / %+v", err, pub)
	}

	// Owner edits display fields; slug is stable.
	upd, err := s.UpdateShop(user("seller-1"),
		&sellerv1.UpdateShopRequest{Name: "Aloe Vera Emporium", Description: "more plants"})
	if err != nil || upd.GetName() != "Aloe Vera Emporium" || upd.GetSlug() != "aloe-vera-depot" {
		t.Fatalf("UpdateShop: %v / %+v", err, upd)
	}

	// Operator suspends with a reason; it disappears from the public API again.
	sus, err := s.SuspendShop(admin("op"), &sellerv1.SuspendShopRequest{Id: sh.GetId(), Reason: "policy"})
	if err != nil || sus.GetStatus() != sellerv1.ShopStatus_SHOP_STATUS_SUSPENDED || sus.GetSuspensionReason() == "" {
		t.Fatalf("SuspendShop: %v / %+v", err, sus)
	}
	if _, err := s.GetShop(context.Background(),
		&sellerv1.GetShopRequest{Selector: &sellerv1.GetShopRequest_Id{Id: sh.GetId()}}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("public GetShop(suspended) err = %v, want NotFound", err)
	}

	// SuspendShop needs a reason.
	other, _ := s.CreateShop(user("seller-2"), &sellerv1.CreateShopRequest{Name: "Second Shop"})
	if _, err := s.SuspendShop(admin("op"), &sellerv1.SuspendShopRequest{Id: other.GetId(), Reason: "  "}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("SuspendShop(no reason) err = %v, want InvalidArgument", err)
	}
}

func TestSeller_CreateRequiresAuth(t *testing.T) {
	s := newSrv(t)
	if _, err := s.CreateShop(context.Background(), &sellerv1.CreateShopRequest{Name: "Anon Shop"}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("anon CreateShop err = %v, want Unauthenticated", err)
	}
}
