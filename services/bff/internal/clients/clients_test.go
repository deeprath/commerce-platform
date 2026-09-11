package clients

import "testing"

func TestDialWiresEveryClient(t *testing.T) {
	// grpc.NewClient is lazy — no connection is made until the first RPC — so
	// Dial succeeds with placeholder addresses and every field is populated.
	set, err := Dial(Targets{
		Catalog: "catalog:1", Media: "media:1", Search: "search:1", Cart: "cart:1",
		Pricing: "pricing:1", Order: "order:1", Payment: "payment:1",
		Fulfillment: "fulfillment:1", Review: "review:1", Seller: "seller:1", Payout: "payout:1",
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if set.Catalog == nil || set.Media == nil || set.Search == nil || set.Cart == nil ||
		set.Pricing == nil || set.Order == nil || set.Payment == nil ||
		set.Fulfillment == nil || set.Review == nil || set.Seller == nil || set.Payout == nil {
		t.Fatalf("a client is nil: %+v", set)
	}
	if len(set.conns) != 11 {
		t.Fatalf("conns = %d, want 11", len(set.conns))
	}
	set.Close() // must not panic and closes every conn
}
