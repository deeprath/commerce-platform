package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collectOnce installs a real meter provider with a manual reader, so the
// observable callbacks registered by NewOutboxRelay actually run, and returns
// what they reported.
func collectOnce(t *testing.T, build func()) metricdata.ResourceMetrics {
	t.Helper()
	reader := metric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(metric.NewMeterProvider(metric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	build() // must construct the relay *after* the provider is installed

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return rm
}

func gaugeValues(rm metricdata.ResourceMetrics) (depth int64, age float64, found int) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "commerce.outbox.pending":
				if g, ok := m.Data.(metricdata.Gauge[int64]); ok && len(g.DataPoints) > 0 {
					depth = g.DataPoints[0].Value
					found++
				}
			case "commerce.outbox.oldest.age":
				if g, ok := m.Data.(metricdata.Gauge[float64]); ok && len(g.DataPoints) > 0 {
					age = g.DataPoints[0].Value
					found++
				}
			}
		}
	}
	return depth, age, found
}

// The backlog has to be reported by a relay that is not draining — a value
// recorded after a successful drain would simply never appear in that case.
func TestObserveBacklog_ReportsDepthAndAgeOnCollection(t *testing.T) {
	d := &fakeDB{pending: rows(17)}
	rm := collectOnce(t, func() {
		NewOutboxRelay(d, &recordingPublisher{}, time.Hour, 100)
	})

	depth, age, found := gaugeValues(rm)
	if found != 2 {
		t.Fatalf("found %d of the 2 backlog gauges — the callback did not run", found)
	}
	if depth != 17 {
		t.Fatalf("pending = %d, want 17", depth)
	}
	if age <= 0 {
		t.Fatalf("oldest age = %v, want the age of the stuck row", age)
	}
}

// A drained outbox must report zero rather than dropping the series, or the
// alert cannot tell "healthy" from "not reporting".
func TestObserveBacklog_ReportsZeroWhenDrained(t *testing.T) {
	rm := collectOnce(t, func() {
		NewOutboxRelay(&fakeDB{}, &recordingPublisher{}, time.Hour, 100)
	})

	depth, age, found := gaugeValues(rm)
	if found != 2 {
		t.Fatalf("found %d of the 2 backlog gauges on an empty outbox", found)
	}
	if depth != 0 || age != 0 {
		t.Fatalf("drained outbox reported depth=%d age=%v, want zeroes", depth, age)
	}
}

// A database that will not answer must not wedge metric collection for every
// other instrument in the process.
func TestObserveBacklog_QueryFailureDoesNotBreakCollection(t *testing.T) {
	d := &fakeDB{rowErr: errors.New("db down")}
	reader := metric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(metric.NewMeterProvider(metric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	NewOutboxRelay(d, &recordingPublisher{}, time.Hour, 100)

	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	if err == nil {
		t.Fatal("want the callback error surfaced to the reader")
	}
	// The failure is reported, but collection still completes rather than
	// hanging or panicking.
	if _, _, found := gaugeValues(rm); found != 0 {
		t.Fatalf("reported %d gauge values despite the query failing", found)
	}
}
