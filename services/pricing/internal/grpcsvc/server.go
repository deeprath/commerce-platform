// Package grpcsvc implements PricingService: it resolves catalog prices, applies
// a coupon and tax, and returns a signed quote.
package grpcsvc

import (
	"context"
	"time"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	pricingv1 "github.com/deeprath/commerce-platform/gen/go/commerce/pricing/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/pricing/internal/domain"
	"github.com/deeprath/commerce-platform/services/pricing/internal/store"
)

// TaxRates maps an ISO country code to a tax rate in basis points. Fallback is
// Default. Real tax is a rules engine; this is a v1 placeholder.
type TaxRates struct {
	Default int32
	ByCode  map[string]int32
}

func (t TaxRates) For(country string) int32 {
	if bps, ok := t.ByCode[country]; ok {
		return bps
	}
	return t.Default
}

type Server struct {
	pricingv1.UnimplementedPricingServiceServer
	store   *store.Store
	catalog catalogv1.CatalogServiceClient
	tax     TaxRates
}

func New(s *store.Store, catalog catalogv1.CatalogServiceClient, tax TaxRates) *Server {
	return &Server{store: s, catalog: catalog, tax: tax}
}

func (s *Server) QuotePrice(ctx context.Context, req *pricingv1.QuoteRequest) (*pricingv1.Quote, error) {
	cur := req.GetCurrencyCode()
	if len(cur) != 3 {
		return nil, errs.New(errs.KindInvalidArgument, "BAD_CURRENCY", "currency_code must be 3 letters")
	}
	if len(req.GetItems()) == 0 {
		return nil, errs.New(errs.KindInvalidArgument, "NO_ITEMS", "at least one item is required")
	}

	qty, err := quantitiesByProduct(req.GetItems())
	if err != nil {
		return nil, err
	}

	byID, err := s.catalogProductsByID(ctx, qty)
	if err != nil {
		return nil, err
	}

	lines, err := priceLines(qty, byID, cur)
	if err != nil {
		return nil, err
	}

	coupon, err := s.resolveCoupon(ctx, req.GetCouponCode())
	if err != nil {
		return nil, err
	}

	taxBps := s.tax.For(req.GetShipTo().GetCountryCode())
	q, err := domain.Price(cur, lines, coupon, taxBps, time.Now())
	if err != nil {
		return nil, err
	}
	return toProtoQuote(q), nil
}

// quantitiesByProduct validates every requested line and collapses duplicate
// product ids into one summed quantity.
func quantitiesByProduct(items []*pricingv1.QuoteLineInput) (map[string]int32, error) {
	qty := map[string]int32{}
	for _, it := range items {
		if it.GetQuantity() <= 0 {
			return nil, errs.New(errs.KindInvalidArgument, "BAD_QUANTITY", "quantity must be > 0")
		}
		qty[it.GetProductId()] += it.GetQuantity()
	}
	return qty, nil
}

func (s *Server) catalogProductsByID(ctx context.Context, qty map[string]int32) (map[string]*catalogv1.Product, error) {
	ids := make([]string, 0, len(qty))
	for id := range qty {
		ids = append(ids, id)
	}
	batch, err := s.catalog.BatchGetProducts(ctx, &catalogv1.BatchGetProductsRequest{Ids: ids})
	if err != nil {
		return nil, errs.Wrap(err, errs.KindUnavailable, "CATALOG_UNAVAILABLE", "cannot resolve product prices")
	}
	byID := map[string]*catalogv1.Product{}
	for _, p := range batch.GetProducts() {
		byID[p.GetId()] = p
	}
	return byID, nil
}

// priceLines joins each requested product with its catalog price, failing if
// any product is unknown or priced in a different currency than the quote.
func priceLines(qty map[string]int32, byID map[string]*catalogv1.Product, cur string) ([]domain.Line, error) {
	var lines []domain.Line
	for id, q := range qty {
		p := byID[id]
		if p == nil {
			return nil, errs.New(errs.KindFailedPrecondition, "PRODUCT_UNPRICEABLE", "product "+id+" not found in catalog").WithMeta("product_id", id)
		}
		mp := p.GetListPrice()
		if mp.GetCurrencyCode() != cur {
			return nil, errs.New(errs.KindFailedPrecondition, "CURRENCY_MISMATCH", "product priced in a different currency").WithMeta("product_id", id)
		}
		lines = append(lines, domain.Line{
			ProductID: id, Title: p.GetTitle(), Quantity: q, ShopID: p.GetShopId(),
			UnitPrice: domain.FromUnitsNanos(cur, mp.GetUnits(), mp.GetNanos()),
		})
	}
	return lines, nil
}

// resolveCoupon looks up code, if given; "" means no coupon (not an error).
func (s *Server) resolveCoupon(ctx context.Context, code string) (*domain.Coupon, error) {
	if code == "" {
		return nil, nil
	}
	c, err := s.store.GetCoupon(ctx, code)
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return nil, errs.New(errs.KindFailedPrecondition, "COUPON_UNKNOWN", "coupon code is not recognised")
		}
		return nil, err
	}
	return c, nil
}

func (s *Server) ValidateCoupon(ctx context.Context, req *pricingv1.ValidateCouponRequest) (*pricingv1.CouponInfo, error) {
	c, err := s.store.GetCoupon(ctx, req.GetCode())
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return &pricingv1.CouponInfo{Code: req.GetCode(), Valid: false, Reason: "UNKNOWN"}, nil
		}
		return nil, err
	}
	ok, reason := c.Valid(time.Now())
	info := &pricingv1.CouponInfo{
		Code: c.Code, Valid: ok, Kind: string(c.Kind),
		PercentOff: c.PercentOff, Reason: reason,
	}
	if !c.ExpiresAt.IsZero() {
		info.ExpiresAt = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if c.Kind == domain.CouponAmount {
		u, n := c.AmountOff.UnitsNanos()
		info.AmountOff = &commonv1.Money{CurrencyCode: c.AmountOff.Currency, Units: u, Nanos: n}
	}
	return info, nil
}

func toProtoQuote(q *domain.Quote) *pricingv1.Quote {
	out := &pricingv1.Quote{
		Subtotal: money(q.Subtotal), Discount: money(q.Discount),
		Tax: money(q.Tax), Total: money(q.Total),
		CouponCode: q.Coupon, PricingSignature: q.Signature,
	}
	for _, l := range q.Lines {
		out.Lines = append(out.Lines, &pricingv1.QuoteLine{
			ProductId: l.ProductID, Title: l.Title, Quantity: l.Quantity, ShopId: l.ShopID,
			UnitPrice: money(l.UnitPrice), LineTotal: money(l.LineTotal),
		})
	}
	return out
}

func money(m domain.Money) *commonv1.Money {
	u, n := m.UnitsNanos()
	return &commonv1.Money{CurrencyCode: m.Currency, Units: u, Nanos: n}
}
