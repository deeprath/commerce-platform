package consumer

import (
	"context"
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
)

// call records one saga step invocation: which step, and the arguments the
// routing pulled off the event.
type call struct {
	step string
	args []string
}

// fakeSaga records what it was asked to do and can be told to fail.
type fakeSaga struct {
	calls []call
	err   error
}

func (f *fakeSaga) record(step string, args ...string) error {
	f.calls = append(f.calls, call{step: step, args: args})
	return f.err
}

func (f *fakeSaga) OnPaymentAuthorized(_ context.Context, eventID, orderID string) error {
	return f.record("OnPaymentAuthorized", eventID, orderID)
}

func (f *fakeSaga) OnPaymentFailed(_ context.Context, eventID, orderID, reason string) error {
	return f.record("OnPaymentFailed", eventID, orderID, reason)
}

func (f *fakeSaga) OnReservationExpired(_ context.Context, eventID, orderRef string) error {
	return f.record("OnReservationExpired", eventID, orderRef)
}

func (f *fakeSaga) OnShipmentDelivered(_ context.Context, eventID, orderID, shopID string) error {
	return f.record("OnShipmentDelivered", eventID, orderID, shopID)
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal %T: %v", m, err)
	}
	return b
}

func rec(topic string, value []byte) *kgo.Record {
	return &kgo.Record{Topic: topic, Partition: 3, Offset: 42, Value: value}
}

// Each topic has to reach its own saga step with the right fields lifted off
// the payload. Getting this wrong is silent: the event decodes, a step runs,
// and the order advances to a state nothing that happened justifies.
func TestHandler_RoutesEachTopicToItsOwnStep(t *testing.T) {
	// Every record below is handed over at the same coordinates, so each case's
	// expected event id is just its own topic with those appended.
	const coords = ":3:42" // partition:offset, matching rec()

	tests := []struct {
		name  string
		topic string
		msg   proto.Message
		want  call
	}{
		{
			name:  "payment authorized",
			topic: kafka.Topic("payment", "authorized"),
			msg:   &paymentv1.PaymentAuthorized{PaymentId: "pay-1", OrderId: "ord-1"},
			want:  call{"OnPaymentAuthorized", []string{"ord-1"}},
		},
		{
			name:  "payment failed carries the reason through",
			topic: kafka.Topic("payment", "failed"),
			msg:   &paymentv1.PaymentFailed{PaymentId: "pay-1", OrderId: "ord-1", Reason: "card_declined"},
			want:  call{"OnPaymentFailed", []string{"ord-1", "card_declined"}},
		},
		{
			// inventory names it order_ref, not order_id — an easy field to
			// mis-wire, and the compensation would then target nothing.
			name:  "reservation expired reads order_ref",
			topic: kafka.Topic("inventory", "reservation_expired"),
			msg:   &inventoryv1.ReservationExpired{ReservationId: "res-1", OrderRef: "ord-1"},
			want:  call{"OnReservationExpired", []string{"ord-1"}},
		},
		{
			name:  "shipment delivered carries the shop id",
			topic: kafka.Topic("fulfillment", "delivered"),
			msg:   &fulfillmentv1.ShipmentDelivered{ShipmentId: "shp-1", OrderId: "ord-1", ShopId: "shop-7"},
			want:  call{"OnShipmentDelivered", []string{"ord-1", "shop-7"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sg := &fakeSaga{}

			if err := Handler(sg)(context.Background(), rec(tc.topic, mustMarshal(t, tc.msg))); err != nil {
				t.Fatalf("Handler: %v", err)
			}
			if len(sg.calls) != 1 {
				t.Fatalf("saga calls = %v, want exactly one", sg.calls)
			}
			got := sg.calls[0]
			if got.step != tc.want.step {
				t.Errorf("step = %s, want %s", got.step, tc.want.step)
			}
			// args[0] is always the event id; the rest come off the payload.
			wantArgs := append([]string{tc.topic + coords}, tc.want.args...)
			if len(got.args) != len(wantArgs) {
				t.Fatalf("args = %v, want %v", got.args, wantArgs)
			}
			for i := range wantArgs {
				if got.args[i] != wantArgs[i] {
					t.Errorf("arg %d = %q, want %q", i, got.args[i], wantArgs[i])
				}
			}
		})
	}
}

// Delivery is at-least-once, so the saga dedupes on the event id. That only
// works if the id is a property of the record: identical across redeliveries of
// the same record, and different for every other one.
func TestHandler_EventIDIdentifiesTheRecordNotItsContents(t *testing.T) {
	topic := kafka.Topic("payment", "authorized")
	payload := mustMarshal(t, &paymentv1.PaymentAuthorized{OrderId: "ord-1"})

	idFor := func(partition int32, offset int64) string {
		sg := &fakeSaga{}
		r := &kgo.Record{Topic: topic, Partition: partition, Offset: offset, Value: payload}
		if err := Handler(sg)(context.Background(), r); err != nil {
			t.Fatalf("Handler: %v", err)
		}
		return sg.calls[0].args[0]
	}

	// Redelivery: the same record handed over twice must dedupe.
	if first, again := idFor(3, 42), idFor(3, 42); first != again {
		t.Errorf("redelivery produced %q then %q; the saga would process it twice", first, again)
	}
	// Distinct records must not collide, including across partitions — offsets
	// are only unique within one.
	seen := map[string]string{}
	for _, c := range []struct {
		name      string
		partition int32
		offset    int64
	}{
		{"p3 o42", 3, 42},
		{"p3 o43", 3, 43},
		{"p4 o42", 4, 42},
	} {
		id := idFor(c.partition, c.offset)
		if prev, dup := seen[id]; dup {
			t.Errorf("%s and %s share event id %q; one would be dropped as a duplicate", prev, c.name, id)
		}
		seen[id] = c.name
	}
}

// A record that cannot be decoded will never decode, however many times it is
// retried. It is dropped rather than returned as an error, which would stall
// the consumer group's committed offset behind a record that can never succeed.
func TestHandler_UndecodableRecordIsDroppedNotRetried(t *testing.T) {
	for _, topic := range Topics() {
		sg := &fakeSaga{}
		// Field 1 typed as varint, then a truncated value: not a valid message
		// for any of the four schemas.
		err := Handler(sg)(context.Background(), rec(topic, []byte{0x08, 0xff}))
		if err != nil {
			t.Errorf("%s: err = %v, want nil so the offset can advance", topic, err)
		}
		if len(sg.calls) != 0 {
			t.Errorf("%s: garbage reached the saga: %v", topic, sg.calls)
		}
	}
}

// The consumer is subscribed only to Topics(), but a rebalance or a config
// change can hand it something else; it must not guess.
func TestHandler_UnknownTopicIsIgnored(t *testing.T) {
	sg := &fakeSaga{}
	valid := mustMarshal(t, &paymentv1.PaymentAuthorized{OrderId: "ord-1"})

	if err := Handler(sg)(context.Background(), rec("commerce.payment.refunded", valid)); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(sg.calls) != 0 {
		t.Fatalf("an unsubscribed topic reached the saga: %v", sg.calls)
	}
}

// A saga failure is transient — the database or a downstream service is down —
// so it must surface. pkg/kafka retries it and parks it on the DLQ; swallowing
// it here would advance the offset and lose the event silently.
func TestHandler_SagaErrorSurfaces(t *testing.T) {
	want := errors.New("reserve: connection refused")
	sg := &fakeSaga{err: want}

	err := Handler(sg)(context.Background(),
		rec(kafka.Topic("payment", "authorized"),
			mustMarshal(t, &paymentv1.PaymentAuthorized{OrderId: "ord-1"})))

	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want it to carry %v", err, want)
	}
}

// Topics() is the subscription and the switch in Handler is the dispatch. They
// are written in two places and drift apart silently: a topic added to the
// subscription but not the switch is consumed and discarded, and its offset
// committed, so the events are gone with nothing logged.
func TestTopics_AreAllHandled(t *testing.T) {
	// A payload every one of the four schemas can decode. proto is permissive
	// across messages, so an empty message decodes as any of them — which is
	// what lets one payload probe every topic.
	empty := []byte{}

	for _, topic := range Topics() {
		sg := &fakeSaga{}
		if err := Handler(sg)(context.Background(), rec(topic, empty)); err != nil {
			t.Fatalf("%s: %v", topic, err)
		}
		if len(sg.calls) == 0 {
			t.Errorf("%s is subscribed but Handler has no case for it: its events would be dropped", topic)
		}
	}
}

func TestTopics_AreDistinctAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, topic := range Topics() {
		if seen[topic] {
			t.Errorf("%s is subscribed twice", topic)
		}
		seen[topic] = true
	}
	if len(Topics()) == 0 {
		t.Fatal("the order service subscribes to nothing")
	}
}
