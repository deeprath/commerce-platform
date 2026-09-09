// Package consumer turns commerce.order.confirmed events into shipments.
package consumer

import (
	"context"
	"fmt"
	"log/slog"

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

// Handler creates the shipment for each confirmed order. Idempotent via the
// store's processed_events row and the one-shipment-per-order constraint.
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
		sh, err := st.CreateFromOrder(ctx, e.GetOrderId(), e.GetOwnerId(),
			addrFrom(e.GetShipTo()), itemsFrom(e.GetLines()), eventID)
		if err != nil {
			return err
		}
		if sh != nil {
			slog.InfoContext(ctx, "shipment created",
				slog.String("shipment_id", sh.ID), slog.String("order_id", sh.OrderID))
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

func itemsFrom(lines []*orderv1.OrderLine) []domain.Item {
	out := make([]domain.Item, 0, len(lines))
	for _, l := range lines {
		out = append(out, domain.Item{
			ProductID: l.GetProductId(), Title: l.GetTitle(), Quantity: l.GetQuantity(),
		})
	}
	return out
}
