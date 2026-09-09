// Package consumer creates a stock row for every new catalog product so
// checkout can reserve against it. Dev environments seed a default on-hand.
package consumer

import (
	"context"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	"github.com/deeprath/commerce-platform/services/inventory/internal/store"
)

// Handler upserts a stock row on catalog.product_changed.
func Handler(st *store.Store, defaultOnHand int) func(context.Context, *kgo.Record) error {
	return func(ctx context.Context, r *kgo.Record) error {
		var evt catalogv1.ProductChanged
		if err := proto.Unmarshal(r.Value, &evt); err != nil {
			slog.ErrorContext(ctx, "skip undecodable product_changed", slog.Any("err", err))
			return nil
		}
		if evt.GetProductId() == "" {
			return nil
		}
		if err := st.EnsureStockRow(ctx, evt.GetProductId(), defaultOnHand); err != nil {
			return err // retry
		}
		slog.DebugContext(ctx, "stock row ensured", slog.String("product_id", evt.GetProductId()))
		return nil
	}
}
