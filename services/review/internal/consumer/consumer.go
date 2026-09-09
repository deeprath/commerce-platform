// Package consumer records verified purchases from commerce.order.confirmed so
// a customer can review the products they have bought.
package consumer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/services/review/internal/store"
)

// Topics the review service consumes.
func Topics() []string {
	return []string{kafka.Topic("order", "confirmed")}
}

// Handler records the (owner, product) pairs from each confirmed order.
// Idempotent via the store's processed_events row.
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

		ids := make([]string, 0, len(e.GetLines()))
		for _, l := range e.GetLines() {
			ids = append(ids, l.GetProductId())
		}
		eventID := fmt.Sprintf("%s:%d:%d", r.Topic, r.Partition, r.Offset)
		if err := st.RecordPurchases(ctx, e.GetOwnerId(), ids, eventID); err != nil {
			return err
		}
		slog.InfoContext(ctx, "recorded verified purchases",
			slog.String("owner_id", e.GetOwnerId()), slog.Int("products", len(ids)))
		return nil
	}
}
