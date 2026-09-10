// Package clickstream flattens commerce.clickstream.tracked browser events into
// `clickstream` rows. It runs in its own consumer group (analytics-clickstream)
// so a clickstream backlog or poison message can't stall funnel ingestion, and
// so it scales on its own lag.
package clickstream

import (
	"context"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	clickstreamv1 "github.com/deeprath/commerce-platform/gen/go/commerce/clickstream/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/services/analytics/internal/clickhouse"
)

// Topics is the single clickstream topic the BFF publishes to.
func Topics() []string { return []string{kafka.Topic("clickstream", "tracked")} }

// adder is the subset of *clickhouse.Sink the handler needs (for tests).
type adder interface {
	AddClick(context.Context, clickhouse.Click) error
}

// Handler decodes one ClientEvent and buffers a clickstream row. Undecodable or
// UNSPECIFIED-type records are logged and skipped so a poison message can't
// stall the partition.
func Handler(sink adder) func(context.Context, *kgo.Record) error {
	return func(ctx context.Context, r *kgo.Record) error {
		var e clickstreamv1.ClientEvent
		if proto.Unmarshal(r.Value, &e) != nil {
			return nil
		}
		typ := typeName(e.GetType())
		if typ == "" {
			return nil
		}
		occurred := ts(e.GetReceivedAt(), time.Now().UTC())
		row := clickhouse.Click{
			Type:        typ,
			OccurredAt:  occurred,
			ClientTime:  ts(e.GetClientTime(), occurred),
			AnonymousID: e.GetAnonymousId(),
			SessionID:   e.GetSessionId(),
			OwnerID:     e.GetOwnerId(),
			Path:        e.GetPath(),
			Referrer:    e.GetReferrer(),
			ProductID:   e.GetProductId(),
			Query:       e.GetQuery(),
			ValueMinor:  e.GetValueMinor(),
			Currency:    e.GetCurrencyCode(),
			UserAgent:   e.GetUserAgent(),
		}
		if err := sink.AddClick(ctx, row); err != nil {
			slog.ErrorContext(ctx, "clickstream buffer failed",
				slog.String("topic", r.Topic), slog.Any("err", err))
			return err // retry
		}
		return nil
	}
}

func typeName(t clickstreamv1.EventType) string {
	switch t {
	case clickstreamv1.EventType_EVENT_TYPE_PAGE_VIEW:
		return "page_view"
	case clickstreamv1.EventType_EVENT_TYPE_PRODUCT_VIEW:
		return "product_view"
	case clickstreamv1.EventType_EVENT_TYPE_SEARCH:
		return "search"
	case clickstreamv1.EventType_EVENT_TYPE_ADD_TO_CART:
		return "add_to_cart"
	case clickstreamv1.EventType_EVENT_TYPE_BEGIN_CHECKOUT:
		return "begin_checkout"
	case clickstreamv1.EventType_EVENT_TYPE_PURCHASE:
		return "purchase"
	default:
		return ""
	}
}

// ts parses an RFC3339(Nano) timestamp, returning def on anything unparseable so
// a malformed clock can't drop the row or write a zero (1970) date.
func ts(s string, def time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	return def
}
