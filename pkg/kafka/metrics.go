package kafka

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/deeprath/commerce-platform/pkg/telemetry"
)

// Instruments for the two consumer outcomes an operator needs to know about but
// which previously existed only as log lines: a record set aside on a DLQ, and a
// record that could not even be set aside, which freezes the group's offsets.
//
// The global meter is a no-op until telemetry.Setup runs, so these are safe at
// package init and in any process — a test included — that never configures
// telemetry. Errors are ignored for the same reason they are in the reconciler:
// the names are compile-time constants, so a failure here would be a
// programming error, and a metrics problem must never stop a consumer.
var (
	consumerMeter = telemetry.Meter("github.com/deeprath/commerce-platform/pkg/kafka")

	dlqParked, _ = consumerMeter.Int64Counter("commerce.kafka.dlq.parked",
		metric.WithDescription("Records set aside on a dead-letter topic after exhausting their retries."))

	offsetsHeld, _ = consumerMeter.Int64Counter("commerce.kafka.offsets.held",
		metric.WithDescription("Records the consumer could not complete or park, so the group's offsets stopped advancing."))
)

func recordParked(ctx context.Context, topic string) {
	dlqParked.Add(ctx, 1, metric.WithAttributes(attribute.String("topic", topic)))
}

func recordHeld(ctx context.Context, topic, reason string) {
	offsetsHeld.Add(ctx, 1, metric.WithAttributes(
		attribute.String("topic", topic),
		attribute.String("reason", reason),
	))
}

// observeBacklog registers the relay's backlog as observable gauges: depth, and
// the age of the oldest unpublished row. Called by NewOutboxRelay, so a relay
// always reports its own backlog and there is no per-service wiring to forget.
//
// Observable rather than recorded after each drain, deliberately. A recorded
// value goes stale the moment the relay stops ticking — which is exactly the
// failure you want to see. An observable callback is driven by the metrics
// reader instead, so a wedged relay reports a backlog that keeps growing rather
// than a number frozen at its last success.
//
// The gauges carry no service attribute: service.name is already a resource
// attribute on everything this process exports, and the alert rules aggregate
// by job like the rest of the platform's.
func (r *OutboxRelay) observeBacklog() {
	// Resolved here rather than at package init so a process — or a test — that
	// installs a meter provider after this package loads still gets real
	// instruments.
	m := telemetry.Meter("github.com/deeprath/commerce-platform/pkg/kafka")

	depth, err := m.Int64ObservableGauge("commerce.outbox.pending",
		metric.WithDescription("Outbox rows written but not yet published to Kafka."))
	if err != nil {
		slog.Error("outbox: backlog depth metric unavailable", slog.Any("err", err))
		return
	}
	age, err := m.Float64ObservableGauge("commerce.outbox.oldest.age",
		metric.WithDescription("Age of the oldest unpublished outbox row."),
		metric.WithUnit("s"))
	if err != nil {
		slog.Error("outbox: backlog age metric unavailable", slog.Any("err", err))
		return
	}

	if _, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		// Bounded: this runs on the collection interval, and a slow query must
		// not hold up every other instrument in the process.
		ctx, cancel := context.WithTimeout(ctx, backlogQueryTimeout)
		defer cancel()

		count, oldest, err := r.Pending(ctx)
		if err != nil {
			return err
		}
		o.ObserveInt64(depth, count)
		o.ObserveFloat64(age, oldest.Seconds())
		return nil
	}, depth, age); err != nil {
		slog.Error("outbox: could not register the backlog callback", slog.Any("err", err))
	}
}

const backlogQueryTimeout = 2 * time.Second
