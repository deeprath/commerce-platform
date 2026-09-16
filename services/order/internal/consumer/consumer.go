// Package consumer routes payment.* and inventory.* events into the saga.
package consumer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
)

// Topics the order service consumes.
func Topics() []string {
	return []string{
		kafka.Topic("payment", "authorized"),
		kafka.Topic("payment", "failed"),
		kafka.Topic("inventory", "reservation_expired"),
		kafka.Topic("fulfillment", "delivered"),
	}
}

// steps is the slice of the saga orchestrator this package drives.
//
// Narrowed to an interface so the routing above can be tested for what it
// actually decides — which topic reaches which step, with which fields pulled
// off the event — without standing up an orchestrator, and through it a
// database and three gRPC clients, none of which the routing touches.
// *saga.Orchestrator satisfies it structurally, so no call site changed.
type steps interface {
	OnPaymentAuthorized(ctx context.Context, eventID, orderID string) error
	OnPaymentFailed(ctx context.Context, eventID, orderID, reason string) error
	OnReservationExpired(ctx context.Context, eventID, orderRef string) error
	OnShipmentDelivered(ctx context.Context, eventID, orderID, shopID string) error
}

// Handler dispatches one record to the right saga method.
//
// The event id is the record's coordinates, not anything inside the payload.
// Delivery is at-least-once, so the same record can arrive more than once; its
// topic/partition/offset is the one thing that is identical across those
// deliveries and distinct between genuinely different events, which is what
// makes it usable as the saga's idempotency key.
func Handler(sg steps) func(context.Context, *kgo.Record) error {
	return func(ctx context.Context, r *kgo.Record) error {
		eventID := fmt.Sprintf("%s:%d:%d", r.Topic, r.Partition, r.Offset)
		switch r.Topic {
		case kafka.Topic("payment", "authorized"):
			var e paymentv1.PaymentAuthorized
			if err := proto.Unmarshal(r.Value, &e); err != nil {
				return skip(ctx, r, err)
			}
			return sg.OnPaymentAuthorized(ctx, eventID, e.GetOrderId())

		case kafka.Topic("payment", "failed"):
			var e paymentv1.PaymentFailed
			if err := proto.Unmarshal(r.Value, &e); err != nil {
				return skip(ctx, r, err)
			}
			return sg.OnPaymentFailed(ctx, eventID, e.GetOrderId(), e.GetReason())

		case kafka.Topic("inventory", "reservation_expired"):
			var e inventoryv1.ReservationExpired
			if err := proto.Unmarshal(r.Value, &e); err != nil {
				return skip(ctx, r, err)
			}
			return sg.OnReservationExpired(ctx, eventID, e.GetOrderRef())

		case kafka.Topic("fulfillment", "delivered"):
			var e fulfillmentv1.ShipmentDelivered
			if err := proto.Unmarshal(r.Value, &e); err != nil {
				return skip(ctx, r, err)
			}
			return sg.OnShipmentDelivered(ctx, eventID, e.GetOrderId(), e.GetShopId())

		default:
			return nil
		}
	}
}

func skip(ctx context.Context, r *kgo.Record, err error) error {
	slog.ErrorContext(ctx, "skip undecodable event",
		slog.String("topic", r.Topic), slog.Int64("offset", r.Offset), slog.Any("err", err))
	return nil
}
