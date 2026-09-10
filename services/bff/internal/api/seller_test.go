package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sellerv1 "github.com/deeprath/commerce-platform/gen/go/commerce/seller/v1"
	"github.com/deeprath/commerce-platform/services/bff/internal/clients"
)

type fakeSeller struct {
	sellerv1.SellerServiceClient
	lastCreate   *sellerv1.CreateShopRequest
	lastGetShop  *sellerv1.GetShopRequest
	lastList     *sellerv1.ListShopsRequest
	lastActivate *sellerv1.ActivateShopRequest
	lastSuspend  *sellerv1.SuspendShopRequest
	err          error
}

func (f *fakeSeller) CreateShop(_ context.Context, in *sellerv1.CreateShopRequest, _ ...grpc.CallOption) (*sellerv1.Shop, error) {
	f.lastCreate = in
	if f.err != nil {
		return nil, f.err
	}
	return &sellerv1.Shop{Id: "shop-1", Name: in.GetName(), Slug: "the-shop", Status: sellerv1.ShopStatus_SHOP_STATUS_PENDING_REVIEW}, nil
}
func (f *fakeSeller) GetMyShop(_ context.Context, _ *sellerv1.GetMyShopRequest, _ ...grpc.CallOption) (*sellerv1.Shop, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sellerv1.Shop{Id: "shop-1", Slug: "the-shop"}, nil
}
func (f *fakeSeller) UpdateShop(_ context.Context, in *sellerv1.UpdateShopRequest, _ ...grpc.CallOption) (*sellerv1.Shop, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sellerv1.Shop{Id: "shop-1", Name: in.GetName(), Slug: "the-shop"}, nil
}
func (f *fakeSeller) GetShop(_ context.Context, in *sellerv1.GetShopRequest, _ ...grpc.CallOption) (*sellerv1.Shop, error) {
	f.lastGetShop = in
	if f.err != nil {
		return nil, f.err
	}
	return &sellerv1.Shop{Id: "shop-1", Slug: in.GetSlug(), Status: sellerv1.ShopStatus_SHOP_STATUS_ACTIVE}, nil
}
func (f *fakeSeller) ListShops(_ context.Context, in *sellerv1.ListShopsRequest, _ ...grpc.CallOption) (*sellerv1.ListShopsResponse, error) {
	f.lastList = in
	if f.err != nil {
		return nil, f.err
	}
	return &sellerv1.ListShopsResponse{Shops: []*sellerv1.Shop{{Id: "shop-1"}}}, nil
}
func (f *fakeSeller) ActivateShop(_ context.Context, in *sellerv1.ActivateShopRequest, _ ...grpc.CallOption) (*sellerv1.Shop, error) {
	f.lastActivate = in
	return &sellerv1.Shop{Id: in.GetId(), Status: sellerv1.ShopStatus_SHOP_STATUS_ACTIVE}, f.err
}
func (f *fakeSeller) SuspendShop(_ context.Context, in *sellerv1.SuspendShopRequest, _ ...grpc.CallOption) (*sellerv1.Shop, error) {
	f.lastSuspend = in
	return &sellerv1.Shop{Id: in.GetId(), Status: sellerv1.ShopStatus_SHOP_STATUS_SUSPENDED}, f.err
}

func sellerReq(t *testing.T, s *Server, method, path, body string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if authed {
		r.Header.Set("Authorization", "Bearer t")
	}
	rec := httptest.NewRecorder()
	s.Router([]string{"*"}).ServeHTTP(rec, r)
	return rec
}

func TestCreateShop_RequiresAuthAndForwards(t *testing.T) {
	fs := &fakeSeller{}
	s := &Server{cl: &clients.Set{Seller: fs}}

	if rec := sellerReq(t, s, http.MethodPost, "/api/v1/seller/shops", `{"name":"The Shop"}`, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon create status = %d, want 401", rec.Code)
	}
	rec := sellerReq(t, s, http.MethodPost, "/api/v1/seller/shops", `{"name":"The Shop","description":"d"}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", rec.Code, rec.Body.String())
	}
	if fs.lastCreate.GetName() != "The Shop" {
		t.Fatalf("forwarded %+v", fs.lastCreate)
	}
}

func TestGetShop_PublicNoAuthNeeded(t *testing.T) {
	fs := &fakeSeller{}
	s := &Server{cl: &clients.Set{Seller: fs}}
	rec := sellerReq(t, s, http.MethodGet, "/api/v1/shops/the-shop", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if fs.lastGetShop.GetSlug() != "the-shop" {
		t.Fatalf("slug not forwarded: %+v", fs.lastGetShop)
	}
}

func TestAdminSuspendShop_ForwardsIdAndReason(t *testing.T) {
	fs := &fakeSeller{}
	s := &Server{cl: &clients.Set{Seller: fs}}
	rec := sellerReq(t, s, http.MethodPost, "/api/v1/admin/seller/shops/shop-9/suspend", `{"reason":"policy"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if fs.lastSuspend.GetId() != "shop-9" || fs.lastSuspend.GetReason() != "policy" {
		t.Fatalf("forwarded %+v", fs.lastSuspend)
	}
}

func TestAdminListShops_MapsStatusFilter(t *testing.T) {
	fs := &fakeSeller{}
	s := &Server{cl: &clients.Set{Seller: fs}}
	if rec := sellerReq(t, s, http.MethodGet, "/api/v1/admin/seller/shops?status=PENDING_REVIEW", "", true); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if fs.lastList.GetStatus() != sellerv1.ShopStatus_SHOP_STATUS_PENDING_REVIEW {
		t.Fatalf("status filter = %v", fs.lastList.GetStatus())
	}
}

func TestSeller_MapsGrpcError(t *testing.T) {
	fs := &fakeSeller{err: status.Error(codes.AlreadyExists, "SHOP_EXISTS")}
	s := &Server{cl: &clients.Set{Seller: fs}}
	rec := sellerReq(t, s, http.MethodPost, "/api/v1/seller/shops", `{"name":"Dup Shop"}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestGetMyShop_AuthGate(t *testing.T) {
	s := &Server{cl: &clients.Set{Seller: &fakeSeller{}}}
	if rec := sellerReq(t, s, http.MethodGet, "/api/v1/seller/shops/me", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon status = %d, want 401", rec.Code)
	}
	if rec := sellerReq(t, s, http.MethodGet, "/api/v1/seller/shops/me", "", true); rec.Code != http.StatusOK {
		t.Fatalf("authed status = %d, want 200", rec.Code)
	}
}

func TestUpdateShop_AuthGateAndForward(t *testing.T) {
	fs := &fakeSeller{}
	s := &Server{cl: &clients.Set{Seller: fs}}
	if rec := sellerReq(t, s, http.MethodPut, "/api/v1/seller/shops/me", `{"name":"New"}`, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon status = %d, want 401", rec.Code)
	}
	rec := sellerReq(t, s, http.MethodPut, "/api/v1/seller/shops/me", `{"name":"Renamed Shop","description":"d"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Renamed Shop") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAdminActivateShop_ForwardsID(t *testing.T) {
	fs := &fakeSeller{}
	s := &Server{cl: &clients.Set{Seller: fs}}
	rec := sellerReq(t, s, http.MethodPost, "/api/v1/admin/seller/shops/shop-42/activate", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if fs.lastActivate.GetId() != "shop-42" {
		t.Fatalf("forwarded id = %q", fs.lastActivate.GetId())
	}
}
