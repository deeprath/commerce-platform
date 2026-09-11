// Package consumer turns commerce.order.confirmed events into per-shop
// payouts.
package consumer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/services/payout/internal/domain"
	"github.com/deeprath/commerce-platform/services/payout/internal/store"
)

// Topics the payout service consumes.
func Topics() []string {
	return []string{kafka.Topic("order", "confirmed")}
}

// Handler creates one payout per shop group for each confirmed order.
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
		payouts, err := st.CreateFromOrder(ctx, e.GetOrderId(), groupsFrom(e.GetLines()), eventID)
		if err != nil {
			return err
		}
		for _, p := range payouts {
			slog.InfoContext(ctx, "payout created",
				slog.String("payout_id", p.ID), slog.String("order_id", p.OrderID),
				slog.String("shop_id", p.ShopID), slog.Int64("amount_cents", p.Amount.Cents))
		}
		return nil
	}
}

// groupsFrom sums an order's lines by shop_id — one group per distinct shop.
// First-party lines (shop_id "") are summed too so CreateFromOrder's
// shop-only filter has a well-formed (and harmless) group to skip.
func groupsFrom(lines []*orderv1.OrderLine) []store.ShopAmount {
	byShop := map[string]domain.Money{}
	order := []string{}
	for _, l := range lines {
		shop := l.GetShopId()
		if _, seen := byShop[shop]; !seen {
			order = append(order, shop)
		}
		lt := l.GetLineTotal()
		byShop[shop] = byShop[shop].Add(domain.FromUnitsNanos(lt.GetCurrencyCode(), lt.GetUnits(), lt.GetNanos()))
	}
	groups := make([]store.ShopAmount, 0, len(order))
	for _, shop := range order {
		groups = append(groups, store.ShopAmount{ShopID: shop, Amount: byShop[shop]})
	}
	return groups
}
