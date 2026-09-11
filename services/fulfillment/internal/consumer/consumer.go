// Package consumer turns commerce.order.confirmed events into shipments.
package consumer

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/domain"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/store"
)

// Topics the fulfillment service consumes.
func Topics() []string {
	return []string{kafka.Topic("order", "confirmed")}
}

// Handler creates one shipment per shop group for each confirmed order.
// Idempotent via the store's processed_events row and the (order, shop)
// uniqueness constraint.
func Handler(st *store.Store) func(context.Context, *kgo.Record) error {
	return func(ctx context.Context, r *kgo.Record) error {
		if r.Topic != kafka.Topic("order", "confirmed") {
			return nil
		}
		var e orderv1.OrderConfirmed
		if err := proto.Unmarshal(r.Value, &e); err != nil {
			slog.ErrorContext(ctx, "skip undecodable order.confirmed",
				slog.Int64("offset", r.Offset), slog.Any("err", err))
			return nil
		}

		eventID := fmt.Sprintf("%s:%d:%d", r.Topic, r.Partition, r.Offset)
		shipments, err := st.CreateFromOrder(ctx, e.GetOrderId(), e.GetOwnerId(),
			addrFrom(e.GetShipTo()), groupsFrom(e.GetLines()), eventID)
		if err != nil {
			return err
		}
		for _, sh := range shipments {
			slog.InfoContext(ctx, "shipment created",
				slog.String("shipment_id", sh.ID), slog.String("order_id", sh.OrderID),
				slog.String("shop_id", sh.ShopID))
		}
		return nil
	}
}

func addrFrom(a *commonv1.Address) domain.Address {
	return domain.Address{
		FullName: a.GetFullName(), Line1: a.GetLine1(), Line2: a.GetLine2(), City: a.GetCity(),
		Region: a.GetRegion(), PostalCode: a.GetPostalCode(), CountryCode: a.GetCountryCode(), Phone: a.GetPhone(),
	}
}

// groupsFrom buckets an order's lines by shop_id — one group per distinct
// shop (plus one for any first-party lines, shop_id "") — sorted for a
// deterministic shipment-creation order.
func groupsFrom(lines []*orderv1.OrderLine) []store.ShopItems {
	byShop := map[string][]domain.Item{}
	for _, l := range lines {
		byShop[l.GetShopId()] = append(byShop[l.GetShopId()], domain.Item{
			ProductID: l.GetProductId(), Title: l.GetTitle(), Quantity: l.GetQuantity(),
		})
	}
	shopIDs := make([]string, 0, len(byShop))
	for id := range byShop {
		shopIDs = append(shopIDs, id)
	}
	sort.Strings(shopIDs)

	groups := make([]store.ShopItems, 0, len(shopIDs))
	for _, id := range shopIDs {
		groups = append(groups, store.ShopItems{ShopID: id, Items: byShop[id]})
	}
	return groups
}
