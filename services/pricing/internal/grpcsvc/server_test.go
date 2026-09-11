package grpcsvc

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	pricingv1 "github.com/deeprath/commerce-platform/gen/go/commerce/pricing/v1"
	"github.com/deeprath/commerce-platform/services/pricing/internal/store"
)

// fakeCatalog serves a fixed product list to BatchGetProducts; every other
// method is unused by QuotePrice and left to the embedded nil interface (a
// call would nil-panic, which is what we want if QuotePrice starts calling
// something it shouldn't).
type fakeCatalog struct {
	catalogv1.CatalogServiceClient
	products []*catalogv1.Product
}

func (f *fakeCatalog) BatchGetProducts(
	_ context.Context, _ *catalogv1.BatchGetProductsRequest, _ ...grpc.CallOption,
) (*catalogv1.BatchGetProductsResponse, error) {
	return &catalogv1.BatchGetProductsResponse{Products: f.products}, nil
}

func newSrv(products []*catalogv1.Product) *Server {
	// No coupon code is exercised below, so QuotePrice never touches the
	// store — a nil-pooled Store is safe to pass here.
	return New(store.New(nil), &fakeCatalog{products: products}, TaxRates{Default: 0})
}

func TestQuotePrice_CopiesShopIDFromCatalogOntoEachLine(t *testing.T) {
	s := newSrv([]*catalogv1.Product{
		{Id: "p-shop", Title: "Shop Widget", ListPrice: &commonv1.Money{CurrencyCode: "USD", Units: 10}, ShopId: "shop-1"},
		{Id: "p-first-party", Title: "Platform Widget", ListPrice: &commonv1.Money{CurrencyCode: "USD", Units: 5}},
	})
	q, err := s.QuotePrice(context.Background(), &pricingv1.QuoteRequest{
		Items: []*pricingv1.QuoteLineInput{
			{ProductId: "p-shop", Quantity: 1},
			{ProductId: "p-first-party", Quantity: 1},
		},
		CurrencyCode: "USD",
	})
	if err != nil {
		t.Fatalf("QuotePrice: %v", err)
	}
	byID := map[string]string{}
	for _, l := range q.GetLines() {
		byID[l.GetProductId()] = l.GetShopId()
	}
	if byID["p-shop"] != "shop-1" {
		t.Fatalf("shop_id not copied for a marketplace line: %+v", byID)
	}
	if byID["p-first-party"] != "" {
		t.Fatalf("first-party line should have an empty shop_id: %+v", byID)
	}
}
