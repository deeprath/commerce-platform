package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	pricingv1 "github.com/deeprath/commerce-platform/gen/go/commerce/pricing/v1"
	"github.com/deeprath/commerce-platform/services/bff/internal/clients"
)

// --- fakes -----------------------------------------------------------------

type fakeCatalog struct {
	catalogv1.CatalogServiceClient
	resp   *catalogv1.BatchGetProductsResponse
	gotIDs []string
}

func (f *fakeCatalog) BatchGetProducts(
	_ context.Context, in *catalogv1.BatchGetProductsRequest, _ ...grpc.CallOption,
) (*catalogv1.BatchGetProductsResponse, error) {
	f.gotIDs = in.GetIds()
	return f.resp, nil
}

type fakePricing struct {
	pricingv1.PricingServiceClient
	quote       *pricingv1.Quote
	errOnCoupon bool
	coupons     []string // coupon codes seen, in call order
}

func (f *fakePricing) QuotePrice(
	_ context.Context, in *pricingv1.QuoteRequest, _ ...grpc.CallOption,
) (*pricingv1.Quote, error) {
	f.coupons = append(f.coupons, in.GetCouponCode())
	if f.errOnCoupon && in.GetCouponCode() != "" {
		return nil, status.Error(codes.NotFound, "COUPON_NOT_FOUND")
	}
	return f.quote, nil
}

type fakeCart struct {
	cartv1.CartServiceClient
	cart      *cartv1.Cart
	gotCartID string
	added     *cartv1.AddItemRequest
}

func (f *fakeCart) GetCart(
	_ context.Context, in *cartv1.GetCartRequest, _ ...grpc.CallOption,
) (*cartv1.Cart, error) {
	f.gotCartID = in.GetCartId()
	return f.cart, nil
}

func (f *fakeCart) AddItem(
	_ context.Context, in *cartv1.AddItemRequest, _ ...grpc.CallOption,
) (*cartv1.Cart, error) {
	f.added = in
	return f.cart, nil
}

func (f *fakeCart) SetItemQuantity(
	_ context.Context, in *cartv1.SetItemQuantityRequest, _ ...grpc.CallOption,
) (*cartv1.Cart, error) {
	f.gotCartID = in.GetCartId()
	return f.cart, nil
}

func (f *fakeCart) RemoveItem(
	_ context.Context, in *cartv1.RemoveItemRequest, _ ...grpc.CallOption,
) (*cartv1.Cart, error) {
	f.gotCartID = in.GetCartId()
	return f.cart, nil
}

func (f *fakeCart) Clear(
	_ context.Context, in *cartv1.ClearRequest, _ ...grpc.CallOption,
) (*cartv1.Cart, error) {
	f.gotCartID = in.GetCartId()
	return f.cart, nil
}

func usd(units int64, nanos int32) *commonv1.Money {
	return &commonv1.Money{CurrencyCode: "USD", Units: units, Nanos: nanos}
}

func newCtx() (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cart", nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) cartView {
	t.Helper()
	var v cartView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, rec.Body.String())
	}
	return v
}

// --- tests ---------------------------------------------------------------------

func TestRespondCart_EmptyCartEmitsJSONArray(t *testing.T) {
	s := &Server{cl: &clients.Set{}}
	c, rec := newCtx()

	if err := s.respondCart(c, &cartv1.Cart{Id: "c_1"}, ""); err != nil {
		t.Fatalf("respondCart: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The storefront does cart.items.length; it must never see null.
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty cart body missing items array: %s", rec.Body.String())
	}
	if v := decode(t, rec); v.Items == nil || len(v.Items) != 0 {
		t.Fatalf("Items = %#v, want non-nil empty slice", v.Items)
	}
}

func TestRespondCart_EnrichesLinesAndTotals(t *testing.T) {
	cat := &fakeCatalog{resp: &catalogv1.BatchGetProductsResponse{Products: []*catalogv1.Product{
		{Id: "p1", Slug: "desk-lamp", Title: "Desk Lamp", MediaKeys: []string{"k1", "k2"}},
		{Id: "p2", Slug: "notebook", Title: "Notebook"},
	}}}
	pr := &fakePricing{quote: &pricingv1.Quote{
		Lines: []*pricingv1.QuoteLine{
			{ProductId: "p1", UnitPrice: usd(34, 990000000), LineTotal: usd(69, 980000000)},
			{ProductId: "p2", UnitPrice: usd(12, 0), LineTotal: usd(12, 0)},
		},
		Subtotal: usd(81, 980000000),
		Discount: usd(0, 0),
		Tax:      usd(6, 560000000),
		Total:    usd(88, 540000000),
	}}
	s := &Server{cl: &clients.Set{Catalog: cat, Pricing: pr}}
	c, rec := newCtx()

	raw := &cartv1.Cart{
		Id:            "c_1",
		TotalQuantity: 3,
		Items: []*cartv1.CartItem{
			{ProductId: "p1", Quantity: 2},
			{ProductId: "p2", Quantity: 1},
		},
	}
	if err := s.respondCart(c, raw, ""); err != nil {
		t.Fatalf("respondCart: %v", err)
	}

	v := decode(t, rec)
	if got := strings.Join(cat.gotIDs, ","); got != "p1,p2" {
		t.Fatalf("catalog batch ids = %q, want p1,p2", got)
	}
	if v.TotalQuantity != 3 || len(v.Items) != 2 {
		t.Fatalf("view = %+v", v)
	}
	if v.Items[0].Slug != "desk-lamp" || v.Items[0].Title != "Desk Lamp" || v.Items[0].PrimaryMediaKey != "k1" {
		t.Fatalf("line 0 not enriched from catalog: %+v", v.Items[0])
	}
	if v.Items[0].UnitPrice == nil || v.Items[0].UnitPrice.Units != "34" || v.Items[0].LineTotal.Units != "69" {
		t.Fatalf("line 0 pricing not applied: %+v", v.Items[0])
	}
	if v.Subtotal == nil || v.Subtotal.Units != "81" || v.Tax.Units != "6" || v.Total.Units != "88" {
		t.Fatalf("totals not carried from quote: %+v", v)
	}
}

func TestRespondCart_BadCouponRetriesWithoutIt(t *testing.T) {
	cat := &fakeCatalog{resp: &catalogv1.BatchGetProductsResponse{Products: []*catalogv1.Product{
		{Id: "p1", Slug: "desk-lamp", Title: "Desk Lamp"},
	}}}
	pr := &fakePricing{
		errOnCoupon: true,
		quote:       &pricingv1.Quote{Subtotal: usd(34, 990000000), Tax: usd(2, 800000000), Total: usd(37, 790000000)},
	}
	s := &Server{cl: &clients.Set{Catalog: cat, Pricing: pr}}
	c, rec := newCtx()

	raw := &cartv1.Cart{Id: "c_1", TotalQuantity: 1, Items: []*cartv1.CartItem{{ProductId: "p1", Quantity: 1}}}
	if err := s.respondCart(c, raw, "SAVE10"); err != nil {
		t.Fatalf("respondCart: %v", err)
	}

	v := decode(t, rec)
	if v.CouponError != "COUPON_NOT_FOUND" {
		t.Fatalf("CouponError = %q, want COUPON_NOT_FOUND", v.CouponError)
	}
	if v.CouponCode != "" {
		t.Fatalf("CouponCode = %q, want empty after a rejected coupon", v.CouponCode)
	}
	if strings.Join(pr.coupons, ",") != "SAVE10," {
		t.Fatalf("pricing called with coupons %v, want [SAVE10, \"\"] (retry without)", pr.coupons)
	}
	if v.Total == nil || v.Total.Units != "37" {
		t.Fatalf("totals from the retry quote missing: %+v", v)
	}
}

// serve runs one request through the real Echo router so the thin cart
// handlers (cookie minting, coupon query param, respondCart wiring) are exercised.
func serve(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Router([]string{"*"}).ServeHTTP(rec, req)
	return rec
}

func TestGetCartHandler_MintsCookieAndPassesCoupon(t *testing.T) {
	fc := &fakeCart{cart: &cartv1.Cart{Id: "srv", TotalQuantity: 1, Items: []*cartv1.CartItem{{ProductId: "p1", Quantity: 1}}}}
	cat := &fakeCatalog{resp: &catalogv1.BatchGetProductsResponse{Products: []*catalogv1.Product{{Id: "p1", Slug: "desk-lamp", Title: "Desk Lamp"}}}}
	pr := &fakePricing{errOnCoupon: true, quote: &pricingv1.Quote{Subtotal: usd(34, 990000000), Total: usd(37, 790000000)}}
	s := &Server{cl: &clients.Set{Cart: fc, Catalog: cat, Pricing: pr}}

	rec := serve(s, httptest.NewRequest(http.MethodGet, "/api/v1/cart?coupon=SAVE10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), cartCookie+"=c_") {
		t.Fatalf("cart_id cookie not minted: %q", rec.Header().Get("Set-Cookie"))
	}
	if fc.gotCartID == "" || !strings.HasPrefix(fc.gotCartID, "c_") {
		t.Fatalf("downstream cart id = %q, want minted c_ token", fc.gotCartID)
	}
	if v := decode(t, rec); v.CouponError != "COUPON_NOT_FOUND" {
		t.Fatalf("coupon query param not forwarded to pricing: %+v", v)
	}
}

func TestAddCartItemHandler_ReusesCookieAndReturnsEnrichedView(t *testing.T) {
	fc := &fakeCart{cart: &cartv1.Cart{Id: "srv", TotalQuantity: 2, Items: []*cartv1.CartItem{{ProductId: "p1", Quantity: 2}}}}
	cat := &fakeCatalog{resp: &catalogv1.BatchGetProductsResponse{Products: []*catalogv1.Product{{Id: "p1", Slug: "desk-lamp", Title: "Desk Lamp"}}}}
	pr := &fakePricing{quote: &pricingv1.Quote{
		Lines:    []*pricingv1.QuoteLine{{ProductId: "p1", UnitPrice: usd(34, 990000000), LineTotal: usd(69, 980000000)}},
		Subtotal: usd(69, 980000000), Total: usd(75, 580000000),
	}}
	s := &Server{cl: &clients.Set{Cart: fc, Catalog: cat, Pricing: pr}}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/cart/items", strings.NewReader(`{"product_id":"p1","quantity":2}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.AddCookie(&http.Cookie{Name: cartCookie, Value: "c_existing"})

	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if fc.added.GetCartId() != "c_existing" {
		t.Fatalf("existing cart cookie not reused: got %q", fc.added.GetCartId())
	}
	if fc.added.GetProductId() != "p1" || fc.added.GetQuantity() != 2 {
		t.Fatalf("add request not bound from body: %+v", fc.added)
	}
	v := decode(t, rec)
	if len(v.Items) != 1 || v.Items[0].Title != "Desk Lamp" || v.Total.Units != "75" {
		t.Fatalf("response not the enriched view: %+v", v)
	}
}

// The mutating handlers (set qty / remove / clear) all funnel their result
// through respondCart; check each is wired and returns the enriched view.
func TestCartMutationHandlers_ReturnEnrichedView(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"set quantity", http.MethodPut, "/api/v1/cart/items/p1", `{"quantity":3}`},
		{"remove item", http.MethodDelete, "/api/v1/cart/items/p1", ""},
		{"clear cart", http.MethodPost, "/api/v1/cart/clear", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCart{cart: &cartv1.Cart{Id: "srv"}} // downstream returns an empty cart
			s := &Server{cl: &clients.Set{Cart: fc, Catalog: &fakeCatalog{}, Pricing: &fakePricing{}}}

			var bodyReader *strings.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			} else {
				bodyReader = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, bodyReader)
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			req.AddCookie(&http.Cookie{Name: cartCookie, Value: "c_existing"})

			rec := serve(s, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if fc.gotCartID != "c_existing" {
				t.Fatalf("cart cookie not forwarded: got %q", fc.gotCartID)
			}
			// Empty cart still serialises items as [] (never null).
			if !strings.Contains(rec.Body.String(), `"items":[]`) {
				t.Fatalf("body not the enriched empty view: %s", rec.Body.String())
			}
		})
	}
}
