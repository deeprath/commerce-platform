// Package consumer turns commerce.order.confirmed events into per-shop
// payouts, and commerce.order.return_approved events into reversals against
// those payouts.
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
//
// The refund path is driven by order.return_approved rather than
// payment.refunded, for two reasons: only the order service knows which shop
// each refunded line belonged to (payment sees an order-level amount), and an
// approved return already causes the refund — consuming both would reverse the
// same money twice. An operator-initiated refund with no return behind it
// therefore has no shop attribution and is not reversed automatically; that is
// a known limit, recorded in docs/PRD.md §3.3.
func Topics() []string {
	return []string{
		kafka.Topic("order", "confirmed"),
		kafka.Topic("order", "return_approved"),
	}
}

// Handler creates one payout per shop group for each confirmed order, and
// reverses the matching payouts when a return is approved. Both paths are
// idempotent via the store's processed_events row; creation additionally leans
// on the (order, shop) uniqueness constraint.
func Handler(st *store.Store) func(context.Context, *kgo.Record) error {
	return func(ctx context.Context, r *kgo.Record) error {
		switch r.Topic {
		case kafka.Topic("order", "confirmed"):
			return handleConfirmed(ctx, st, r)
		case kafka.Topic("order", "return_approved"):
			return handleReturnApproved(ctx, st, r)
		default:
			return nil
		}
	}
}

func handleConfirmed(ctx context.Context, st *store.Store, r *kgo.Record) error {
	var e orderv1.OrderConfirmed
	if err := proto.Unmarshal(r.Value, &e); err != nil {
		slog.ErrorContext(ctx, "skip undecodable order.confirmed",
			slog.Int64("offset", r.Offset), slog.Any("err", err))
		return nil
	}

	payouts, err := st.CreateFromOrder(ctx, e.GetOrderId(), groupsFrom(e.GetLines()), eventIDOf(r))
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

func handleReturnApproved(ctx context.Context, st *store.Store, r *kgo.Record) error {
	var e orderv1.ReturnApproved
	if err := proto.Unmarshal(r.Value, &e); err != nil {
		slog.ErrorContext(ctx, "skip undecodable order.return_approved",
			slog.Int64("offset", r.Offset), slog.Any("err", err))
		return nil
	}

	refunds := refundsFrom(e.GetShopRefunds())
	if len(refunds) == 0 {
		// A return that touched only first-party lines, or an event published
		// before the breakdown existed. Nothing to reverse either way.
		return nil
	}

	reversed, err := st.ReverseFromReturn(ctx, e.GetOrderId(), refunds, eventIDOf(r))
	if err != nil {
		return err
	}
	for _, p := range reversed {
		slog.InfoContext(ctx, "payout reversed",
			slog.String("payout_id", p.ID), slog.String("order_id", p.OrderID),
			slog.String("shop_id", p.ShopID),
			slog.Int64("reversed_cents", p.Reversed.Cents),
			slog.Int64("outstanding_cents", p.Outstanding().Cents),
			slog.Bool("was_paid", p.WasPaid()))
	}
	return nil
}

func eventIDOf(r *kgo.Record) string {
	return fmt.Sprintf("%s:%d:%d", r.Topic, r.Partition, r.Offset)
}

// refundsFrom maps the event's per-shop breakdown onto the store's unit,
// dropping the first-party share (no payout exists for it).
func refundsFrom(shopRefunds []*orderv1.ShopRefund) []store.ShopAmount {
	out := make([]store.ShopAmount, 0, len(shopRefunds))
	for _, sr := range shopRefunds {
		if sr.GetShopId() == "" {
			continue
		}
		a := sr.GetAmount()
		out = append(out, store.ShopAmount{
			ShopID: sr.GetShopId(),
			Amount: domain.FromUnitsNanos(a.GetCurrencyCode(), a.GetUnits(), a.GetNanos()),
		})
	}
	return out
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
