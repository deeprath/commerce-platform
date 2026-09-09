// Package clients holds the BFF's gRPC connections to the domain services.
package clients

import (
	"google.golang.org/grpc"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	mediav1 "github.com/deeprath/commerce-platform/gen/go/commerce/media/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	pricingv1 "github.com/deeprath/commerce-platform/gen/go/commerce/pricing/v1"
	searchv1 "github.com/deeprath/commerce-platform/gen/go/commerce/search/v1"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
)

// Targets is the set of downstream addresses.
type Targets struct {
	Catalog, Media, Search, Cart, Pricing, Order, Payment string
}

type Set struct {
	Catalog catalogv1.CatalogServiceClient
	Media   mediav1.MediaServiceClient
	Search  searchv1.SearchServiceClient
	Cart    cartv1.CartServiceClient
	Pricing pricingv1.PricingServiceClient
	Order   orderv1.OrderServiceClient
	Payment paymentv1.PaymentServiceClient

	conns []*grpc.ClientConn
}

// Dial opens one connection per downstream service.
func Dial(t Targets) (*Set, error) {
	s := &Set{}
	dial := func(addr string) (*grpc.ClientConn, error) {
		cc, err := grpcx.Dial(addr)
		if err != nil {
			s.Close()
			return nil, err
		}
		s.conns = append(s.conns, cc)
		return cc, nil
	}
	cc, err := dial(t.Catalog)
	if err != nil {
		return nil, err
	}
	mc, err := dial(t.Media)
	if err != nil {
		return nil, err
	}
	sc, err := dial(t.Search)
	if err != nil {
		return nil, err
	}
	ca, err := dial(t.Cart)
	if err != nil {
		return nil, err
	}
	pr, err := dial(t.Pricing)
	if err != nil {
		return nil, err
	}
	or, err := dial(t.Order)
	if err != nil {
		return nil, err
	}
	pa, err := dial(t.Payment)
	if err != nil {
		return nil, err
	}
	s.Catalog = catalogv1.NewCatalogServiceClient(cc)
	s.Media = mediav1.NewMediaServiceClient(mc)
	s.Search = searchv1.NewSearchServiceClient(sc)
	s.Cart = cartv1.NewCartServiceClient(ca)
	s.Pricing = pricingv1.NewPricingServiceClient(pr)
	s.Order = orderv1.NewOrderServiceClient(or)
	s.Payment = paymentv1.NewPaymentServiceClient(pa)
	return s, nil
}

func (s *Set) Close() {
	for _, c := range s.conns {
		_ = c.Close()
	}
}
