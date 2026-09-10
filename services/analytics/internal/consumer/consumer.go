// Package consumer flattens order.* / payment.* domain events into analytics
// fact rows and buffers them into the ClickHouse sink.
package consumer

import (
	"context"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/services/analytics/internal/clickhouse"
)

// Topics the analytics service consumes — the checkout funnel + revenue.
func Topics() []string {
	return []string{
		kafka.Topic("order", "created"),
		kafka.Topic("order", "confirmed"),
		kafka.Topic("order", "cancelled"),
		kafka.Topic("order", "fulfilled"),
		kafka.Topic("payment", "authorized"),
		kafka.Topic("payment", "failed"),
		kafka.Topic("payment", "refunded"),
	}
}

// adder is the subset of *clickhouse.Sink the handler needs (for tests).
type adder interface {
	Add(context.Context, clickhouse.Event) error
}

// Handler decodes one record and buffers a fact row. Undecodable / unknown
// records are logged and skipped so a poison message can't stall the partition.
// Analytics is best-effort: at-least-once delivery + an append-only table means
// a rare duplicate is acceptable (dedupe in queries with `argMax`/`LIMIT BY`).
func Handler(sink adder) func(context.Context, *kgo.Record) error {
	return func(ctx context.Context, r *kgo.Record) error {
		ev, ok := flatten(r)
		if !ok {
			return nil
		}
		if err := sink.Add(ctx, ev); err != nil {
			slog.ErrorContext(ctx, "analytics buffer failed",
				slog.String("topic", r.Topic), slog.Any("err", err))
			return err // retry
		}
		return nil
	}
}

func flatten(r *kgo.Record) (clickhouse.Event, bool) {
	switch r.Topic {
	case kafka.Topic("order", "created"):
		var e orderv1.OrderCreated
		if proto.Unmarshal(r.Value, &e) != nil {
			return clickhouse.Event{}, false
		}
		return clickhouse.Event{
			Type: "order_created", OccurredAt: ts(e.GetOccurredAt()),
			OrderID: e.GetOrderId(), OwnerID: e.GetOwnerId(),
			AmountMinor: minor(e.GetTotal()), Currency: cur(e.GetTotal()),
		}, true

	case kafka.Topic("order", "confirmed"):
		var e orderv1.OrderConfirmed
		if proto.Unmarshal(r.Value, &e) != nil {
			return clickhouse.Event{}, false
		}
		return clickhouse.Event{
			Type: "order_confirmed", OccurredAt: ts(e.GetOccurredAt()),
			OrderID: e.GetOrderId(), OwnerID: e.GetOwnerId(), PaymentID: e.GetPaymentId(),
		}, true

	case kafka.Topic("order", "cancelled"):
		var e orderv1.OrderCancelled
		if proto.Unmarshal(r.Value, &e) != nil {
			return clickhouse.Event{}, false
		}
		return clickhouse.Event{
			Type: "order_cancelled", OccurredAt: ts(e.GetOccurredAt()),
			OrderID: e.GetOrderId(), OwnerID: e.GetOwnerId(), Reason: e.GetReason(),
		}, true

	case kafka.Topic("order", "fulfilled"):
		var e orderv1.OrderFulfilled
		if proto.Unmarshal(r.Value, &e) != nil {
			return clickhouse.Event{}, false
		}
		return clickhouse.Event{
			Type: "order_fulfilled", OccurredAt: ts(e.GetOccurredAt()),
			OrderID: e.GetOrderId(), OwnerID: e.GetOwnerId(),
		}, true

	case kafka.Topic("payment", "authorized"):
		var e paymentv1.PaymentAuthorized
		if proto.Unmarshal(r.Value, &e) != nil {
			return clickhouse.Event{}, false
		}
		return clickhouse.Event{
			Type: "payment_authorized", OccurredAt: ts(e.GetOccurredAt()),
			OrderID: e.GetOrderId(), PaymentID: e.GetPaymentId(),
			AmountMinor: minor(e.GetAmount()), Currency: cur(e.GetAmount()),
		}, true

	case kafka.Topic("payment", "failed"):
		var e paymentv1.PaymentFailed
		if proto.Unmarshal(r.Value, &e) != nil {
			return clickhouse.Event{}, false
		}
		return clickhouse.Event{
			Type: "payment_failed", OccurredAt: ts(e.GetOccurredAt()),
			OrderID: e.GetOrderId(), PaymentID: e.GetPaymentId(), Reason: e.GetReason(),
		}, true

	case kafka.Topic("payment", "refunded"):
		var e paymentv1.PaymentRefunded
		if proto.Unmarshal(r.Value, &e) != nil {
			return clickhouse.Event{}, false
		}
		return clickhouse.Event{
			Type: "payment_refunded", OccurredAt: ts(e.GetOccurredAt()),
			OrderID: e.GetOrderId(), PaymentID: e.GetPaymentId(),
			AmountMinor: minor(e.GetAmount()), Currency: cur(e.GetAmount()),
		}, true
	}
	return clickhouse.Event{}, false
}

// minor converts a Money to integer minor units (cents). 1 cent = 10^7 nanos.
func minor(m *commonv1.Money) int64 {
	if m == nil {
		return 0
	}
	return m.GetUnits()*100 + int64(m.GetNanos())/10_000_000
}

func cur(m *commonv1.Money) string {
	if m == nil {
		return ""
	}
	return m.GetCurrencyCode()
}

// ts parses an RFC3339(Nano) event timestamp, falling back to now on garbage
// (so a malformed field doesn't drop the row).
func ts(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}
