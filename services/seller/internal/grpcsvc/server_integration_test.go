package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	sellerv1 "github.com/deeprath/commerce-platform/gen/go/commerce/seller/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/fga"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/seller/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/seller/internal/store"
)

// fakeFGA is an in-memory fga.API — exact-tuple only (no userset resolution),
// which is enough for the seller service's Write/Delete/Read paths.
type fakeFGA struct{ tuples map[string]bool }

func newFakeFGA() *fakeFGA { return &fakeFGA{tuples: map[string]bool{}} }

func fk(u, r, o string) string { return u + "|" + r + "|" + o }

func (f *fakeFGA) Check(_ context.Context, u, r, o string) (bool, error) {
	return f.tuples[fk(u, r, o)], nil
}
func (f *fakeFGA) Write(_ context.Context, u, r, o string) error {
	if f.tuples[fk(u, r, o)] {
		return &fga.Error{Status: 400, Message: "tuple already exists"}
	}
	f.tuples[fk(u, r, o)] = true
	return nil
}
func (f *fakeFGA) Delete(_ context.Context, u, r, o string) error {
	if !f.tuples[fk(u, r, o)] {
		return &fga.Error{Status: 400, Message: "tuple not found"}
	}
	delete(f.tuples, fk(u, r, o))
	return nil
}
func (f *fakeFGA) Read(_ context.Context, o string) ([]fga.Tuple, error) {
	var out []fga.Tuple
	for k := range f.tuples {
		p := splitKey(k)
		if p[2] == o {
			out = append(out, fga.Tuple{User: p[0], Relation: p[1], Object: p[2]})
		}
	}
	return out, nil
}

func splitKey(k string) [3]string {
	var out [3]string
	i, start := 0, 0
	for j := 0; j < len(k) && i < 2; j++ {
		if k[j] == '|' {
			out[i] = k[start:j]
			i, start = i+1, j+1
		}
	}
	out[2] = k[start:]
	return out
}

func newSrv(t *testing.T) *grpcsvc.Server { return newSrvFGA(t, newFakeFGA()) }

func newSrvFGA(t *testing.T, f fga.API) *grpcsvc.Server {
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
	return grpcsvc.New(store.New(pool), f)
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

func TestSeller_ShopStaff(t *testing.T) {
	f := newFakeFGA()
	s := newSrvFGA(t, f)

	// no shop yet -> NotFound
	if _, err := s.AddShopStaff(user("owner-s"), &sellerv1.AddShopStaffRequest{StaffSubject: "helper"}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("AddShopStaff(no shop) err = %v, want NotFound", err)
	}

	sh, err := s.CreateShop(user("owner-s"), &sellerv1.CreateShopRequest{Name: "Staffed Shop"})
	if err != nil {
		t.Fatal(err)
	}
	// CreateShop wrote the owner tuple.
	if ok, _ := f.Check(context.Background(), fga.UserObject("owner-s"), fga.RelationOwner, fga.ShopObject(sh.GetId())); !ok {
		t.Fatal("CreateShop did not record the shop#owner tuple")
	}

	// owner adds a staff member (idempotent)
	for i := 0; i < 2; i++ {
		if _, err := s.AddShopStaff(user("owner-s"), &sellerv1.AddShopStaffRequest{StaffSubject: "helper"}); err != nil {
			t.Fatalf("AddShopStaff #%d: %v", i, err)
		}
	}
	if ok, _ := f.Check(context.Background(), fga.UserObject("helper"), fga.RelationStaff, fga.ShopObject(sh.GetId())); !ok {
		t.Fatal("staff tuple not written")
	}

	// can't add yourself
	if _, err := s.AddShopStaff(user("owner-s"), &sellerv1.AddShopStaffRequest{StaffSubject: "owner-s"}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("self-add err = %v, want InvalidArgument", err)
	}
	// empty subject
	if _, err := s.AddShopStaff(user("owner-s"), &sellerv1.AddShopStaffRequest{StaffSubject: "  "}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("empty subject err = %v, want InvalidArgument", err)
	}

	// list shows the staff + names the owner
	lst, err := s.ListShopStaff(user("owner-s"), &sellerv1.ListShopStaffRequest{})
	if err != nil {
		t.Fatalf("ListShopStaff: %v", err)
	}
	if lst.GetOwnerSubject() != "owner-s" || len(lst.GetStaffSubjects()) != 1 || lst.GetStaffSubjects()[0] != "helper" {
		t.Fatalf("list = %+v", lst)
	}

	// a different user can't manage this shop's staff (they have no shop)
	if _, err := s.AddShopStaff(user("stranger"), &sellerv1.AddShopStaffRequest{StaffSubject: "x"}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("stranger AddShopStaff err = %v, want NotFound", err)
	}

	// remove (idempotent)
	for i := 0; i < 2; i++ {
		if _, err := s.RemoveShopStaff(user("owner-s"), &sellerv1.RemoveShopStaffRequest{StaffSubject: "helper"}); err != nil {
			t.Fatalf("RemoveShopStaff #%d: %v", i, err)
		}
	}
	if ok, _ := f.Check(context.Background(), fga.UserObject("helper"), fga.RelationStaff, fga.ShopObject(sh.GetId())); ok {
		t.Fatal("staff tuple still present after remove")
	}
}

func TestSeller_StaffRPCsNeedAuthAndFGA(t *testing.T) {
	// nil FGA -> Unavailable
	nofga := newSrvFGA(t, nil)
	if _, err := nofga.AddShopStaff(user("u"), &sellerv1.AddShopStaffRequest{StaffSubject: "x"}); !errs.Is(err, errs.KindUnavailable) {
		t.Fatalf("nil-FGA AddShopStaff err = %v, want Unavailable", err)
	}
	// anon -> Unauthenticated
	s := newSrv(t)
	if _, err := s.ListShopStaff(context.Background(), &sellerv1.ListShopStaffRequest{}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("anon ListShopStaff err = %v, want Unauthenticated", err)
	}
}

func TestSeller_CreateRequiresAuth(t *testing.T) {
	s := newSrv(t)
	if _, err := s.CreateShop(context.Background(), &sellerv1.CreateShopRequest{Name: "Anon Shop"}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("anon CreateShop err = %v, want Unauthenticated", err)
	}
	if _, err := s.CreateShop(user("u"), &sellerv1.CreateShopRequest{Name: "x"}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("CreateShop(bad name) err = %v, want InvalidArgument", err)
	}
}

func TestSeller_GetShopSelectors(t *testing.T) {
	s := newSrv(t)
	sh, err := s.CreateShop(user("owner-x"), &sellerv1.CreateShopRequest{Name: "Selector Shop"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateShop(admin("op"), &sellerv1.ActivateShopRequest{Id: sh.GetId()}); err != nil {
		t.Fatal(err)
	}

	// by id
	byID, err := s.GetShop(context.Background(),
		&sellerv1.GetShopRequest{Selector: &sellerv1.GetShopRequest_Id{Id: sh.GetId()}})
	if err != nil || byID.GetSlug() != "selector-shop" {
		t.Fatalf("GetShop by id: %v / %+v", err, byID)
	}
	// no selector -> InvalidArgument
	if _, err := s.GetShop(context.Background(), &sellerv1.GetShopRequest{}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("GetShop(no selector) err = %v, want InvalidArgument", err)
	}
	// unknown id -> NotFound
	if _, err := s.GetShop(context.Background(),
		&sellerv1.GetShopRequest{Selector: &sellerv1.GetShopRequest_Id{Id: "11111111-1111-1111-1111-111111111111"}}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("GetShop(unknown id) err = %v, want NotFound", err)
	}
}

func TestSeller_MyShopAndUpdateNeedAShop(t *testing.T) {
	s := newSrv(t)
	if _, err := s.GetMyShop(user("no-shop"), &sellerv1.GetMyShopRequest{}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("GetMyShop(no shop) err = %v, want NotFound", err)
	}
	if _, err := s.UpdateShop(user("no-shop"), &sellerv1.UpdateShopRequest{Name: "Nope"}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("UpdateShop(no shop) err = %v, want NotFound", err)
	}
	if _, err := s.GetMyShop(context.Background(), &sellerv1.GetMyShopRequest{}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("GetMyShop(anon) err = %v, want Unauthenticated", err)
	}
	if _, err := s.UpdateShop(context.Background(), &sellerv1.UpdateShopRequest{Name: "Nope"}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("UpdateShop(anon) err = %v, want Unauthenticated", err)
	}
}

func TestSeller_ListShopsFilterAndPaginate(t *testing.T) {
	s := newSrv(t)
	// three shops; activate one, suspend one, leave one pending.
	var ids []string
	for i, name := range []string{"List Shop A", "List Shop B", "List Shop C"} {
		sh, err := s.CreateShop(user(string(rune('a'+i))+"-owner"), &sellerv1.CreateShopRequest{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sh.GetId())
	}
	if _, err := s.ActivateShop(admin("op"), &sellerv1.ActivateShopRequest{Id: ids[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateShop(admin("op"), &sellerv1.ActivateShopRequest{Id: ids[1]}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SuspendShop(admin("op"), &sellerv1.SuspendShopRequest{Id: ids[1], Reason: "test"}); err != nil {
		t.Fatal(err)
	}

	count := func(ctx context.Context, req *sellerv1.ListShopsRequest) int {
		t.Helper()
		res, err := s.ListShops(ctx, req)
		if err != nil {
			t.Fatalf("ListShops: %v", err)
		}
		return len(res.GetShops())
	}
	if n := count(admin("op"), &sellerv1.ListShopsRequest{Status: sellerv1.ShopStatus_SHOP_STATUS_ACTIVE}); n != 1 {
		t.Fatalf("ACTIVE count = %d, want 1", n)
	}
	if n := count(admin("op"), &sellerv1.ListShopsRequest{Status: sellerv1.ShopStatus_SHOP_STATUS_SUSPENDED}); n != 1 {
		t.Fatalf("SUSPENDED count = %d, want 1", n)
	}
	if n := count(admin("op"), &sellerv1.ListShopsRequest{Status: sellerv1.ShopStatus_SHOP_STATUS_PENDING_REVIEW}); n != 1 {
		t.Fatalf("PENDING count = %d, want 1", n)
	}

	// page_size 2 -> a next page token that yields the remainder.
	first, err := s.ListShops(admin("op"), &sellerv1.ListShopsRequest{Page: &commonv1.PageRequest{PageSize: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.GetShops()) != 2 || first.GetPage().GetNextPageToken() == "" {
		t.Fatalf("first page = %d shops, token %q", len(first.GetShops()), first.GetPage().GetNextPageToken())
	}
	second, err := s.ListShops(admin("op"), &sellerv1.ListShopsRequest{
		Page: &commonv1.PageRequest{PageSize: 2, PageToken: first.GetPage().GetNextPageToken()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.GetShops()) != 1 {
		t.Fatalf("second page = %d shops, want 1", len(second.GetShops()))
	}
}
