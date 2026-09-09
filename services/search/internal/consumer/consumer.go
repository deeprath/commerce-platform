// Package consumer applies commerce.catalog.product_changed events to the
// OpenSearch index. Idempotent: re-processing an event re-writes the same doc.
package consumer

import (
	"context"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/search/internal/index"
)

// Handler returns a kafka.Handler that indexes/deletes products from events.
func Handler(idx *index.Client) func(context.Context, *kgo.Record) error {
	return func(ctx context.Context, r *kgo.Record) error {
		var evt catalogv1.ProductChanged
		if err := proto.Unmarshal(r.Value, &evt); err != nil {
			// A poison message: log and skip rather than block the partition.
			slog.ErrorContext(ctx, "skip undecodable product_changed",
				slog.Int64("offset", r.Offset), slog.Any("err", err))
			return nil
		}
		if err := idx.UpsertFromEvent(ctx, &evt); err != nil {
			if errs.Is(err, errs.KindUnavailable) {
				return err // retry: OpenSearch is down
			}
			slog.ErrorContext(ctx, "index apply failed (skipping)",
				slog.String("product_id", evt.GetProductId()), slog.Any("err", err))
			return nil
		}
		slog.DebugContext(ctx, "indexed",
			slog.String("product_id", evt.GetProductId()),
			slog.String("change", evt.GetChange().String()))
		return nil
	}
}
