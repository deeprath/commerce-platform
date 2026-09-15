package kafka

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/deeprath/commerce-platform/pkg/telemetry"
)

// Instruments for the two outcomes an operator needs to know about but which
// previously existed only as log lines: a record set aside on a DLQ, and a
// record that could not even be set aside, which freezes the group's offsets.
//
// Created once at package level. The global meter is a no-op until
// telemetry.Setup runs, so this is safe in tests and in any service that does
// not configure telemetry.
var (
	meter = telemetry.Meter("github.com/deeprath/commerce-platform/pkg/kafka")

	dlqParked = mustInt64Counter(meter, "commerce.kafka.dlq.parked",
		"Records set aside on a dead-letter topic after exhausting their retries.")

	offsetsHeld = mustInt64Counter(meter, "commerce.kafka.offsets.held",
		"Records the consumer could not complete or park, so the group's offsets stopped advancing.")
)

func mustInt64Counter(m metric.Meter, name, desc string) metric.Int64Counter {
	c, err := m.Int64Counter(name, metric.WithDescription(desc))
	if err != nil {
		// Only reachable on a malformed instrument name, which is a programming
		// error rather than a runtime condition. Fall back to the no-op so a
		// metrics problem can never stop a consumer.
		slog.Error("kafka: could not create metric", slog.String("name", name), slog.Any("err", err))
		noop, _ := telemetry.Meter("noop").Int64Counter("noop")
		return noop
	}
	return c
}

func recordParked(ctx context.Context, topic string) {
	dlqParked.Add(ctx, 1, metric.WithAttributes(attribute.String("topic", topic)))
}

func recordHeld(ctx context.Context, topic, reason string) {
	offsetsHeld.Add(ctx, 1, metric.WithAttributes(
		attribute.String("topic", topic),
		attribute.String("reason", reason),
	))
}

// ObserveBacklog registers the relay's backlog as observable gauges: depth, and
// the age of the oldest unpublished row.
//
// Observable rather than recorded after each drain, deliberately. A recorded
// value goes stale the moment the relay stops ticking — which is exactly the
// failure you want to see. An observable callback is driven by the metrics
// reader instead, so a wedged relay reports a backlog that keeps growing rather
// than a number frozen at its last success.
//
// Returns a cancel func that unregisters the callback.
func (r *OutboxRelay) ObserveBacklog(service string) (func() error, error) {
	depth, err := meter.Int64ObservableGauge("commerce.outbox.pending",
		metric.WithDescription("Outbox rows written but not yet published to Kafka."))
	if err != nil {
		return nil, err
	}
	age, err := meter.Float64ObservableGauge("commerce.outbox.oldest.age",
		metric.WithDescription("Age of the oldest unpublished outbox row."),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}

	attrs := metric.WithAttributes(attribute.String("service", service))
	reg, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		// Bounded: this runs on the collection interval, and a slow query must
		// not hold up every other instrument in the process.
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		count, oldest, err := r.Pending(ctx)
		if err != nil {
			return err
		}
		o.ObserveInt64(depth, count, attrs)
		o.ObserveFloat64(age, oldest.Seconds(), attrs)
		return nil
	}, depth, age)
	if err != nil {
		return nil, err
	}
	return reg.Unregister, nil
}
