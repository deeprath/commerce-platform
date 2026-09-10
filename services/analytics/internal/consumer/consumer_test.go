package consumer_test

import (
	"context"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/services/analytics/internal/clickhouse"
	"github.com/deeprath/commerce-platform/services/analytics/internal/consumer"
)

func TestTopics_CoverTheFunnel(t *testing.T) {
	got := map[string]bool{}
	for _, tp := range consumer.Topics() {
		got[tp] = true
	}
	for _, want := range []string{
		"commerce.order.created", "commerce.order.confirmed", "commerce.order.cancelled",
		"commerce.order.fulfilled", "commerce.payment.authorized", "commerce.payment.failed",
		"commerce.payment.refunded",
	} {
		if !got[want] {
			t.Errorf("Topics() missing %q", want)
		}
	}
}

// fakeSink records what the handler buffers.
type fakeSink struct {
	events []clickhouse.Event
	err    error
}

func (f *fakeSink) Add(_ context.Context, e clickhouse.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func b(t *testing.T, m proto.Message) []byte {
	t.Helper()
	v, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func money(u int64, n int32) *commonv1.Money {
	return &commonv1.Money{CurrencyCode: "USD", Units: u, Nanos: n}
}

func TestHandler_MapsEveryEventType(t *testing.T) {
	ts := "2026-09-10T12:00:00Z"
	when, _ := time.Parse(time.RFC3339, ts)

	cases := []struct {
		topic string
		msg   proto.Message
		want  clickhouse.Event
	}{
		{
			"commerce.order.created",
			&orderv1.OrderCreated{OrderId: "o1", OwnerId: "u1", Total: money(75, 580000000), OccurredAt: ts},
			clickhouse.Event{Type: "order_created", OccurredAt: when, OrderID: "o1", OwnerID: "u1", AmountMinor: 7558, Currency: "USD"},
		},
		{
			"commerce.order.confirmed",
			&orderv1.OrderConfirmed{OrderId: "o1", OwnerId: "u1", PaymentId: "p1", OccurredAt: ts},
			clickhouse.Event{Type: "order_confirmed", OccurredAt: when, OrderID: "o1", OwnerID: "u1", PaymentID: "p1"},
		},
		{
			"commerce.order.cancelled",
			&orderv1.OrderCancelled{OrderId: "o1", OwnerId: "u1", Reason: "PAYMENT_FAILED", OccurredAt: ts},
			clickhouse.Event{Type: "order_cancelled", OccurredAt: when, OrderID: "o1", OwnerID: "u1", Reason: "PAYMENT_FAILED"},
		},
		{
			"commerce.order.fulfilled",
			&orderv1.OrderFulfilled{OrderId: "o1", OwnerId: "u1", OccurredAt: ts},
			clickhouse.Event{Type: "order_fulfilled", OccurredAt: when, OrderID: "o1", OwnerID: "u1"},
		},
		{
			"commerce.payment.authorized",
			&paymentv1.PaymentAuthorized{PaymentId: "p1", OrderId: "o1", Amount: money(12, 0), OccurredAt: ts},
			clickhouse.Event{Type: "payment_authorized", OccurredAt: when, OrderID: "o1", PaymentID: "p1", AmountMinor: 1200, Currency: "USD"},
		},
		{
			"commerce.payment.failed",
			&paymentv1.PaymentFailed{PaymentId: "p1", OrderId: "o1", Reason: "pm_card_declined", OccurredAt: ts},
			clickhouse.Event{Type: "payment_failed", OccurredAt: when, OrderID: "o1", PaymentID: "p1", Reason: "pm_card_declined"},
		},
		{
			"commerce.payment.refunded",
			&paymentv1.PaymentRefunded{PaymentId: "p1", OrderId: "o1", Amount: money(5, 250000000), OccurredAt: ts},
			clickhouse.Event{Type: "payment_refunded", OccurredAt: when, OrderID: "o1", PaymentID: "p1", AmountMinor: 525, Currency: "USD"},
		},
	}

	for _, c := range cases {
		t.Run(c.topic, func(t *testing.T) {
			fs := &fakeSink{}
			err := consumer.Handler(fs)(context.Background(), &kgo.Record{Topic: c.topic, Value: b(t, c.msg)})
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if len(fs.events) != 1 {
				t.Fatalf("buffered %d events, want 1", len(fs.events))
			}
			if fs.events[0] != c.want {
				t.Fatalf("got  %+v\nwant %+v", fs.events[0], c.want)
			}
		})
	}
}

func TestHandler_SkipsUnknownAndUndecodable(t *testing.T) {
	fs := &fakeSink{}
	h := consumer.Handler(fs)

	// unknown topic
	if err := h(context.Background(), &kgo.Record{Topic: "commerce.foo.bar", Value: []byte("x")}); err != nil {
		t.Fatalf("unknown topic: %v", err)
	}
	// every known topic with a garbage payload -> skipped, no buffer, no error.
	garbage := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	for _, tp := range consumer.Topics() {
		if err := h(context.Background(), &kgo.Record{Topic: tp, Value: garbage}); err != nil {
			t.Fatalf("undecodable %s: %v", tp, err)
		}
	}
	if len(fs.events) != 0 {
		t.Fatalf("buffered %d, want 0", len(fs.events))
	}
}

func TestHandler_PropagatesSinkError(t *testing.T) {
	fs := &fakeSink{err: context.DeadlineExceeded}
	err := consumer.Handler(fs)(context.Background(),
		&kgo.Record{Topic: "commerce.order.created", Value: b(t, &orderv1.OrderCreated{OrderId: "o"})})
	if err == nil {
		t.Fatal("want the sink error propagated for retry, got nil")
	}
}

func TestHandler_MissingTimestampFallsBackToNow(t *testing.T) {
	fs := &fakeSink{}
	_ = consumer.Handler(fs)(context.Background(),
		&kgo.Record{Topic: "commerce.order.created", Value: b(t, &orderv1.OrderCreated{OrderId: "o", OccurredAt: "garbage"})})
	if len(fs.events) != 1 || fs.events[0].OccurredAt.IsZero() {
		t.Fatalf("expected a non-zero fallback timestamp, got %+v", fs.events)
	}
}
