package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	pricingv1 "github.com/deeprath/commerce-platform/gen/go/commerce/pricing/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

const cartCookie = "cart_id"

// cartID returns the caller's cart id, minting one (and setting the cookie) on
// first use. The id is an opaque, unguessable token.
func (s *Server) cartID(c echo.Context) string {
	if ck, err := c.Cookie(cartCookie); err == nil && ck.Value != "" {
		return ck.Value
	}
	id := "c_" + uuid.NewString()
	http.SetCookie(c.Response().Writer, &http.Cookie{
		Name: cartCookie, Value: id, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 60 * 60 * 24 * 30,
	})
	return id
}

// money is the plain-JSON shape (protojson-compatible) the storefront expects.
type money struct {
	CurrencyCode string `json:"currency_code"`
	Units        string `json:"units"`
	Nanos        int32  `json:"nanos"`
}

func moneyView(m *commonv1.Money) *money {
	if m == nil {
		return nil
	}
	return &money{CurrencyCode: m.GetCurrencyCode(), Units: itoa64(m.GetUnits()), Nanos: m.GetNanos()}
}

// cartView is the per-view JSON the storefront renders: raw lines enriched with
// catalog data and a live pricing quote.
type cartView struct {
	ID            string         `json:"id"`
	Items         []cartLineView `json:"items"`
	TotalQuantity int32          `json:"total_quantity"`
	Subtotal      *money         `json:"subtotal,omitempty"`
	Discount      *money         `json:"discount,omitempty"`
	Tax           *money         `json:"tax,omitempty"`
	Total         *money         `json:"total,omitempty"`
	CouponCode    string         `json:"coupon_code,omitempty"`
	CouponError   string         `json:"coupon_error,omitempty"`
}

type cartLineView struct {
	ProductID       string `json:"product_id"`
	Slug            string `json:"slug"`
	Title           string `json:"title"`
	Quantity        int32  `json:"quantity"`
	UnitPrice       *money `json:"unit_price,omitempty"`
	LineTotal       *money `json:"line_total,omitempty"`
	PrimaryMediaKey string `json:"primary_media_key"`
}

// respondCart enriches a raw cart and writes it as JSON. couponCode may be "".
func (s *Server) respondCart(c echo.Context, raw *cartv1.Cart, couponCode string) error {
	// Items is always a JSON array (never null) so clients can treat an empty
	// cart the same as a populated one.
	view := cartView{
		ID: raw.GetId(), TotalQuantity: raw.GetTotalQuantity(),
		CouponCode: couponCode, Items: []cartLineView{},
	}
	if len(raw.GetItems()) == 0 {
		return c.JSON(200, view)
	}

	ctx, cancel := outCtx(c)
	defer cancel()

	ids := make([]string, 0, len(raw.GetItems()))
	for _, it := range raw.GetItems() {
		ids = append(ids, it.GetProductId())
	}
	batch, _ := s.cl.Catalog.BatchGetProducts(ctx, &catalogv1.BatchGetProductsRequest{Ids: ids})
	byID := map[string]*catalogv1.Product{}
	for _, p := range batch.GetProducts() {
		byID[p.GetId()] = p
	}

	quoteItems := make([]*pricingv1.QuoteLineInput, 0, len(raw.GetItems()))
	for _, it := range raw.GetItems() {
		quoteItems = append(quoteItems, &pricingv1.QuoteLineInput{ProductId: it.GetProductId(), Quantity: it.GetQuantity()})
	}
	quote, qErr := s.cl.Pricing.QuotePrice(ctx, &pricingv1.QuoteRequest{
		Items: quoteItems, CurrencyCode: "USD", CouponCode: couponCode,
	})
	if qErr != nil {
		// A bad coupon must not break the cart view; retry without it.
		view.CouponError = errs.FromGRPC(qErr).Reason
		quote, _ = s.cl.Pricing.QuotePrice(ctx, &pricingv1.QuoteRequest{Items: quoteItems, CurrencyCode: "USD"})
		view.CouponCode = ""
	}
	lineByID := map[string]*pricingv1.QuoteLine{}
	for _, l := range quote.GetLines() {
		lineByID[l.GetProductId()] = l
	}

	for _, it := range raw.GetItems() {
		lv := cartLineView{ProductID: it.GetProductId(), Quantity: it.GetQuantity()}
		if p := byID[it.GetProductId()]; p != nil {
			lv.Slug, lv.Title = p.GetSlug(), p.GetTitle()
			if mk := p.GetMediaKeys(); len(mk) > 0 {
				lv.PrimaryMediaKey = mk[0]
			}
		}
		if ql := lineByID[it.GetProductId()]; ql != nil {
			lv.UnitPrice, lv.LineTotal = moneyView(ql.GetUnitPrice()), moneyView(ql.GetLineTotal())
		}
		view.Items = append(view.Items, lv)
	}
	if quote != nil {
		view.Subtotal, view.Discount = moneyView(quote.GetSubtotal()), moneyView(quote.GetDiscount())
		view.Tax, view.Total = moneyView(quote.GetTax()), moneyView(quote.GetTotal())
		if quote.GetCouponCode() != "" {
			view.CouponCode = quote.GetCouponCode()
		}
	}
	return c.JSON(200, view)
}

func (s *Server) getCart(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	raw, err := s.cl.Cart.GetCart(ctx, &cartv1.GetCartRequest{CartId: s.cartID(c)})
	if err != nil {
		return fail(c, err)
	}
	return s.respondCart(c, raw, c.QueryParam("coupon"))
}

type addItemBody struct {
	ProductID string `json:"product_id"`
	Quantity  int32  `json:"quantity"`
}

func (s *Server) addCartItem(c echo.Context) error {
	var in addItemBody
	if err := c.Bind(&in); err != nil {
		return c.JSON(400, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "BAD_JSON"})
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	raw, err := s.cl.Cart.AddItem(ctx, &cartv1.AddItemRequest{
		CartId: s.cartID(c), ProductId: in.ProductID, Quantity: in.Quantity,
	})
	if err != nil {
		return fail(c, err)
	}
	return s.respondCart(c, raw, "")
}

type qtyBody struct {
	Quantity int32 `json:"quantity"`
}

func (s *Server) setCartItem(c echo.Context) error {
	var in qtyBody
	if err := c.Bind(&in); err != nil {
		return c.JSON(400, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "BAD_JSON"})
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	raw, err := s.cl.Cart.SetItemQuantity(ctx, &cartv1.SetItemQuantityRequest{
		CartId: s.cartID(c), ProductId: c.Param("productId"), Quantity: in.Quantity,
	})
	if err != nil {
		return fail(c, err)
	}
	return s.respondCart(c, raw, "")
}

func (s *Server) removeCartItem(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	raw, err := s.cl.Cart.RemoveItem(ctx, &cartv1.RemoveItemRequest{
		CartId: s.cartID(c), ProductId: c.Param("productId"),
	})
	if err != nil {
		return fail(c, err)
	}
	return s.respondCart(c, raw, "")
}

func (s *Server) clearCart(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	raw, err := s.cl.Cart.Clear(ctx, &cartv1.ClearRequest{CartId: s.cartID(c)})
	if err != nil {
		return fail(c, err)
	}
	return s.respondCart(c, raw, "")
}
