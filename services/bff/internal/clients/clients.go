// Package clients holds the BFF's gRPC connections to the domain services.
package clients

import (
	"google.golang.org/grpc"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	mediav1 "github.com/deeprath/commerce-platform/gen/go/commerce/media/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	payoutv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payout/v1"
	pricingv1 "github.com/deeprath/commerce-platform/gen/go/commerce/pricing/v1"
	reviewv1 "github.com/deeprath/commerce-platform/gen/go/commerce/review/v1"
	searchv1 "github.com/deeprath/commerce-platform/gen/go/commerce/search/v1"
	sellerv1 "github.com/deeprath/commerce-platform/gen/go/commerce/seller/v1"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
)

// Targets is the set of downstream addresses.
type Targets struct {
	Catalog, Media, Search, Cart, Pricing, Order, Payment, Fulfillment, Review, Seller, Payout string
}

type Set struct {
	Catalog     catalogv1.CatalogServiceClient
	Media       mediav1.MediaServiceClient
	Search      searchv1.SearchServiceClient
	Cart        cartv1.CartServiceClient
	Pricing     pricingv1.PricingServiceClient
	Order       orderv1.OrderServiceClient
	Payment     paymentv1.PaymentServiceClient
	Fulfillment fulfillmentv1.FulfillmentServiceClient
	Review      reviewv1.ReviewServiceClient
	Seller      sellerv1.SellerServiceClient
	Payout      payoutv1.PayoutServiceClient

	conns []*grpc.ClientConn
}

// Dial opens one connection per downstream service. Per-request the BFF builds
// an outgoing context carrying the caller's token (see api.outCtx), so
// role-gated admin RPCs see the operator.
func Dial(t Targets) (*Set, error) {
	s := &Set{}
	conn := func(addr string) (*grpc.ClientConn, error) {
		cc, err := grpcx.Dial(addr)
		if err != nil {
			s.Close()
			return nil, err
		}
		s.conns = append(s.conns, cc)
		return cc, nil
	}

	for _, w := range []struct {
		addr string
		set  func(*grpc.ClientConn)
	}{
		{t.Catalog, func(c *grpc.ClientConn) { s.Catalog = catalogv1.NewCatalogServiceClient(c) }},
		{t.Media, func(c *grpc.ClientConn) { s.Media = mediav1.NewMediaServiceClient(c) }},
		{t.Search, func(c *grpc.ClientConn) { s.Search = searchv1.NewSearchServiceClient(c) }},
		{t.Cart, func(c *grpc.ClientConn) { s.Cart = cartv1.NewCartServiceClient(c) }},
		{t.Pricing, func(c *grpc.ClientConn) { s.Pricing = pricingv1.NewPricingServiceClient(c) }},
		{t.Order, func(c *grpc.ClientConn) { s.Order = orderv1.NewOrderServiceClient(c) }},
		{t.Payment, func(c *grpc.ClientConn) { s.Payment = paymentv1.NewPaymentServiceClient(c) }},
		{t.Fulfillment, func(c *grpc.ClientConn) { s.Fulfillment = fulfillmentv1.NewFulfillmentServiceClient(c) }},
		{t.Review, func(c *grpc.ClientConn) { s.Review = reviewv1.NewReviewServiceClient(c) }},
		{t.Seller, func(c *grpc.ClientConn) { s.Seller = sellerv1.NewSellerServiceClient(c) }},
		{t.Payout, func(c *grpc.ClientConn) { s.Payout = payoutv1.NewPayoutServiceClient(c) }},
	} {
		cc, err := conn(w.addr)
		if err != nil {
			return nil, err
		}
		w.set(cc)
	}
	return s, nil
}

func (s *Set) Close() {
	for _, c := range s.conns {
		_ = c.Close()
	}
}
