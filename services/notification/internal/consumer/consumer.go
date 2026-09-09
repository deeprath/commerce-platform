// Package consumer turns customer-facing lifecycle events into notifications.
package consumer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/services/notification/internal/channel"
	"github.com/deeprath/commerce-platform/services/notification/internal/domain"
	"github.com/deeprath/commerce-platform/services/notification/internal/store"
)

// Topics the notification service consumes — the customer-facing lifecycle.
// All of these carry an owner_id.
func Topics() []string {
	return []string{
		kafka.Topic("order", "created"),
		kafka.Topic("order", "confirmed"),
		kafka.Topic("order", "cancelled"),
		kafka.Topic("order", "fulfilled"),
		kafka.Topic("fulfillment", "shipped"),
		kafka.Topic("fulfillment", "delivered"),
	}
}

// Deps is what the handler needs.
type Deps struct {
	Store   *store.Store
	Channel channel.Sender
}

// msg is the normalised notification input extracted from an event.
type msg struct {
	kind    string
	ownerID string
	refID   string
	data    map[string]string
}

// Handler renders and dispatches one notification per event. Idempotent via the
// store's processed_events row.
func Handler(d Deps) func(context.Context, *kgo.Record) error {
	return func(ctx context.Context, r *kgo.Record) error {
		m, ok := extract(ctx, r)
		if !ok {
			return nil // unknown topic or undecodable payload — skip
		}
		if m.ownerID == "" {
			slog.WarnContext(ctx, "event has no owner_id; dropping notification",
				slog.String("topic", r.Topic), slog.Int64("offset", r.Offset))
			return nil
		}

		rendered, ok := domain.Render(m.kind, m.data)
		if !ok {
			slog.WarnContext(ctx, "no template for kind", slog.String("kind", m.kind))
			return nil
		}

		n := domain.Notification{
			OwnerID: m.ownerID, Kind: m.kind, Channel: domain.ChannelEmail,
			Status: domain.StatusSent, Subject: rendered.Subject, Body: rendered.Body, RefID: m.refID,
		}
		if err := d.Channel.Send(ctx, n); err != nil {
			n.Status = domain.StatusFailed
			slog.ErrorContext(ctx, "channel send failed", slog.String("kind", m.kind), slog.Any("err", err))
		}

		eventID := fmt.Sprintf("%s:%d:%d", r.Topic, r.Partition, r.Offset)
		saved, err := d.Store.Record(ctx, n, eventID)
		if err != nil {
			return err
		}
		if saved != nil {
			slog.InfoContext(ctx, "notification recorded",
				slog.String("id", saved.ID), slog.String("kind", saved.Kind),
				slog.String("status", string(saved.Status)))
		}
		return nil
	}
}

func extract(ctx context.Context, r *kgo.Record) (msg, bool) {
	switch r.Topic {
	case kafka.Topic("order", "created"):
		var e orderv1.OrderCreated
		if !decode(ctx, r, &e) {
			return msg{}, false
		}
		return msg{"order_created", e.GetOwnerId(), e.GetOrderId(), map[string]string{
			"order_id": short(e.GetOrderId()), "total": money(e.GetTotal()),
		}}, true

	case kafka.Topic("order", "confirmed"):
		var e orderv1.OrderConfirmed
		if !decode(ctx, r, &e) {
			return msg{}, false
		}
		return msg{"order_confirmed", e.GetOwnerId(), e.GetOrderId(), map[string]string{
			"order_id": short(e.GetOrderId()),
		}}, true

	case kafka.Topic("order", "cancelled"):
		var e orderv1.OrderCancelled
		if !decode(ctx, r, &e) {
			return msg{}, false
		}
		return msg{"order_cancelled", e.GetOwnerId(), e.GetOrderId(), map[string]string{
			"order_id": short(e.GetOrderId()), "reason": e.GetReason(),
		}}, true

	case kafka.Topic("order", "fulfilled"):
		var e orderv1.OrderFulfilled
		if !decode(ctx, r, &e) {
			return msg{}, false
		}
		return msg{"order_fulfilled", e.GetOwnerId(), e.GetOrderId(), map[string]string{
			"order_id": short(e.GetOrderId()),
		}}, true

	case kafka.Topic("fulfillment", "shipped"):
		var e fulfillmentv1.ShipmentShipped
		if !decode(ctx, r, &e) {
			return msg{}, false
		}
		return msg{"shipment_shipped", e.GetOwnerId(), e.GetOrderId(), map[string]string{
			"order_id": short(e.GetOrderId()), "shipment_id": short(e.GetShipmentId()),
			"carrier": e.GetCarrier(), "tracking_number": e.GetTrackingNumber(),
		}}, true

	case kafka.Topic("fulfillment", "delivered"):
		var e fulfillmentv1.ShipmentDelivered
		if !decode(ctx, r, &e) {
			return msg{}, false
		}
		return msg{"shipment_delivered", e.GetOwnerId(), e.GetOrderId(), map[string]string{
			"order_id": short(e.GetOrderId()), "shipment_id": short(e.GetShipmentId()),
		}}, true

	default:
		return msg{}, false
	}
}

func decode(ctx context.Context, r *kgo.Record, m proto.Message) bool {
	if err := proto.Unmarshal(r.Value, m); err != nil {
		slog.ErrorContext(ctx, "skip undecodable event",
			slog.String("topic", r.Topic), slog.Int64("offset", r.Offset), slog.Any("err", err))
		return false
	}
	return true
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func money(m *commonv1.Money) string {
	if m == nil {
		return "-"
	}
	cents := m.GetUnits()*100 + int64(m.GetNanos())/10_000_000
	return fmt.Sprintf("%s %d.%02d", m.GetCurrencyCode(), cents/100, cents%100)
}
