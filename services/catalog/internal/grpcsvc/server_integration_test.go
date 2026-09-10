package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/fga"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/catalog/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/catalog/internal/store"
)

// fakeFGA — exact-tuple only; enough for the catalog's Check/Write paths.
type fakeFGA struct{ t map[string]bool }

func newFakeFGA() *fakeFGA { return &fakeFGA{t: map[string]bool{}} }

func k(u, r, o string) string { return u + "|" + r + "|" + o }

func (f *fakeFGA) Check(_ context.Context, u, r, o string) (bool, error) { return f.t[k(u, r, o)], nil }
func (f *fakeFGA) Write(_ context.Context, u, r, o string) error {
	if f.t[k(u, r, o)] {
		return &fga.Error{Status: 400, Message: "tuple already exists"}
	}
	f.t[k(u, r, o)] = true
	return nil
}
func (f *fakeFGA) Delete(_ context.Context, u, r, o string) error {
	if !f.t[k(u, r, o)] {
		return &fga.Error{Status: 400, Message: "not found"}
	}
	delete(f.t, k(u, r, o))
	return nil
}
func (f *fakeFGA) Read(context.Context, string) ([]fga.Tuple, error) { return nil, nil }

func (f *fakeFGA) grantStaff(sub, shop string) {
	f.t[k(fga.UserObject(sub), fga.RelationStaff, fga.ShopObject(shop))] = true
}

func newSrv(t *testing.T, f fga.API) *grpcsvc.Server {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("catalog"),
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

func mgr(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"catalog_manager"}})
}
func customer(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer"}})
}

func createReq(slug, shopID string) *catalogv1.CreateProductRequest {
	return &catalogv1.CreateProductRequest{
		Slug: slug, Title: "T " + slug, CategoryId: "c",
		ListPrice: &commonv1.Money{CurrencyCode: "USD", Units: 1000},
		ShopId:    shopID,
	}
}

func TestCatalog_FirstPartyWritesStillNeedTheRole(t *testing.T) {
	s := newSrv(t, newFakeFGA())
	if _, err := s.CreateProduct(customer("nobody"), createReq("p-anon", "")); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("customer CreateProduct(first-party) err = %v, want PermissionDenied", err)
	}
	if _, err := s.CreateProduct(mgr("op"), createReq("p-plat", "")); err != nil {
		t.Fatalf("catalog_manager CreateProduct(first-party): %v", err)
	}
}

func TestCatalog_SellerWriteRequiresShopStaff(t *testing.T) {
	f := newFakeFGA()
	s := newSrv(t, f)
	const shop = "11111111-1111-1111-1111-111111111111"

	// Not staff -> denied.
	if _, err := s.CreateProduct(customer("outsider"), createReq("p-x", shop)); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("non-staff CreateProduct err = %v, want PermissionDenied", err)
	}

	// Grant staff, then it works and a product#shop tuple is recorded.
	f.t[k(fga.UserObject("seller-1"), fga.RelationStaff, fga.ShopObject(shop))] = true
	res, err := s.CreateProduct(customer("seller-1"), createReq("p-shop", shop))
	if err != nil {
		t.Fatalf("staff CreateProduct: %v", err)
	}
	pid := res.GetProduct().GetId()
	if res.GetProduct().GetShopId() != shop {
		t.Fatalf("shop_id not persisted: %+v", res.GetProduct())
	}
	if !f.t[k(fga.ShopObject(shop), fga.RelationShop, fga.ProductObject(pid))] {
		t.Fatal("product#shop tuple not written on create")
	}

	// A platform catalog_manager may still manage any shop's product.
	if _, err := s.UpdateProduct(mgr("op"), &catalogv1.UpdateProductRequest{
		Id: pid, Title: "renamed", CategoryId: "c",
		ListPrice: &commonv1.Money{CurrencyCode: "USD", Units: 1200},
	}); err != nil {
		t.Fatalf("catalog_manager UpdateProduct(shop product): %v", err)
	}

	// A different seller (not staff of this shop) cannot update or archive it.
	upd := &catalogv1.UpdateProductRequest{Id: pid, Title: "hijack", CategoryId: "c",
		ListPrice: &commonv1.Money{CurrencyCode: "USD", Units: 1}}
	if _, err := s.UpdateProduct(customer("seller-2"), upd); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("non-staff UpdateProduct err = %v, want PermissionDenied", err)
	}
	if _, err := s.ArchiveProduct(customer("seller-2"), &catalogv1.ArchiveProductRequest{Id: pid}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("non-staff ArchiveProduct err = %v, want PermissionDenied", err)
	}

	// The shop's own staff can.
	if _, err := s.UpdateProduct(customer("seller-1"), upd); err != nil {
		t.Fatalf("staff UpdateProduct: %v", err)
	}
	if _, err := s.ArchiveProduct(customer("seller-1"), &catalogv1.ArchiveProductRequest{Id: pid}); err != nil {
		t.Fatalf("staff ArchiveProduct: %v", err)
	}
}

func TestCatalog_ListShopProducts(t *testing.T) {
	f := newFakeFGA()
	s := newSrv(t, f)
	const shop = "33333333-3333-3333-3333-333333333333"
	f.grantStaff("seller-1", shop)

	// empty shop_id -> InvalidArgument
	if _, err := s.ListShopProducts(customer("seller-1"), &catalogv1.ListShopProductsRequest{}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("empty shop_id err = %v, want InvalidArgument", err)
	}
	// a non-staff customer -> PermissionDenied
	if _, err := s.ListShopProducts(customer("outsider"), &catalogv1.ListShopProductsRequest{ShopId: shop}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("non-staff list err = %v, want PermissionDenied", err)
	}

	// seed 3 products: one stays DRAFT, one is activated, one is archived.
	var ids []string
	for i, sl := range []string{"lsp-a", "lsp-b", "lsp-c"} {
		r, err := s.CreateProduct(customer("seller-1"), createReq(sl, shop))
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, r.GetProduct().GetId())
	}
	if _, err := s.UpdateProduct(customer("seller-1"), &catalogv1.UpdateProductRequest{
		Id: ids[1], Title: "b", CategoryId: "c",
		ListPrice: &commonv1.Money{CurrencyCode: "USD", Units: 1000},
		Status:    catalogv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveProduct(customer("seller-1"), &catalogv1.ArchiveProductRequest{Id: ids[2]}); err != nil {
		t.Fatal(err)
	}

	// The seller sees all three regardless of status.
	res, err := s.ListShopProducts(customer("seller-1"), &catalogv1.ListShopProductsRequest{ShopId: shop})
	if err != nil {
		t.Fatalf("ListShopProducts: %v", err)
	}
	if len(res.GetProducts()) != 3 {
		t.Fatalf("got %d products, want 3 (all statuses)", len(res.GetProducts()))
	}
	statuses := map[catalogv1.ProductStatus]int{}
	for _, p := range res.GetProducts() {
		statuses[p.GetStatus()]++
		if p.GetShopId() != shop {
			t.Fatalf("product from another shop leaked: %+v", p)
		}
	}
	if statuses[catalogv1.ProductStatus_PRODUCT_STATUS_DRAFT] != 1 ||
		statuses[catalogv1.ProductStatus_PRODUCT_STATUS_ACTIVE] != 1 ||
		statuses[catalogv1.ProductStatus_PRODUCT_STATUS_ARCHIVED] != 1 {
		t.Fatalf("status mix = %v", statuses)
	}

	// A platform catalog_manager may list any shop.
	if _, err := s.ListShopProducts(mgr("op"), &catalogv1.ListShopProductsRequest{ShopId: shop}); err != nil {
		t.Fatalf("catalog_manager ListShopProducts: %v", err)
	}
}

func TestCatalog_ShopWriteWithoutFGAIsDenied(t *testing.T) {
	s := newSrv(t, nil) // no OpenFGA wired
	if _, err := s.CreateProduct(customer("seller-1"), createReq("p-nofga", "22222222-2222-2222-2222-222222222222")); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("shop CreateProduct without FGA err = %v, want PermissionDenied", err)
	}
	// first-party still fine for an operator
	if _, err := s.CreateProduct(mgr("op"), createReq("p-nofga-plat", "")); err != nil {
		t.Fatalf("catalog_manager first-party without FGA: %v", err)
	}
}
