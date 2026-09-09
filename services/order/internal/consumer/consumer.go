// Package consumer routes payment.* and inventory.* events into the saga.
package consumer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/services/order/internal/saga"
)

// Topics the order service consumes.
func Topics() []string {
	return []string{
		kafka.Topic("payment", "authorized"),
		kafka.Topic("payment", "failed"),
		kafka.Topic("inventory", "reservation_expired"),
	}
}

// Handler dispatches one record to the right saga method.
func Handler(sg *saga.Orchestrator) func(context.Context, *kgo.Record) error {
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
