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
)

// indexer is the part of *index.Client this package uses, narrowed so the
// handler can be tested without an OpenSearch behind it.
type indexer interface {
	UpsertFromEvent(ctx context.Context, evt *catalogv1.ProductChanged) error
}

// Retryable is the kafka.DeadLetter policy for Handler's errors: Unavailable
// means OpenSearch is down or shedding load, so the record is only late and its
// offset should be held. Anything else is the record's own problem.
func Retryable(err error) bool { return errs.Is(err, errs.KindUnavailable) }

// Handler returns a kafka.Handler that indexes/deletes products from events.
//
// Indexing failures are returned, all of them. Deciding what happens next is
// pkg/kafka's job, via Retryable: an Unavailable error (OpenSearch down or
// shedding load) holds the offset until the cluster recovers, and anything else
// is parked on the DLQ after its retries. Swallowing an error here would do
// neither — the event would be committed and lost, leaving the index out of
// step with the catalog until that product happens to change again.
func Handler(idx indexer) func(context.Context, *kgo.Record) error {
	return func(ctx context.Context, r *kgo.Record) error {
		var evt catalogv1.ProductChanged
		if err := proto.Unmarshal(r.Value, &evt); err != nil {
			// A poison message: log and skip rather than block the partition.
			slog.ErrorContext(ctx, "skip undecodable product_changed",
				slog.Int64("offset", r.Offset), slog.Any("err", err))
			return nil
		}
		if err := idx.UpsertFromEvent(ctx, &evt); err != nil {
			return err
		}
		slog.DebugContext(ctx, "indexed",
			slog.String("product_id", evt.GetProductId()),
			slog.String("change", evt.GetChange().String()))
		return nil
	}
}
