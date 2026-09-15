package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// isolatedReader binds relays built inside build() to a provider of this test's
// own, and returns a reader over it.
//
// Deliberately not the global provider: instruments created through it are
// replayed onto whatever provider is installed next, so relays from other tests
// would report into this collection. Both backlog gauges are unlabelled, so
// those strays are indistinguishable from the relay under test.
func isolatedReader(t *testing.T, build func()) *metric.ManualReader {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))

	prev := meterFor
	meterFor = func(name string) otelmetric.Meter { return mp.Meter(name) }
	t.Cleanup(func() { meterFor = prev })

	build() // the relay must be constructed while meterFor is swapped
	return reader
}

// collectOnce is isolatedReader plus a collection that must succeed.
func collectOnce(t *testing.T, build func()) metricdata.ResourceMetrics {
	t.Helper()
	reader := isolatedReader(t, build)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return rm
}

// gaugeValues reads the two backlog gauges. With the meter isolated there is
// exactly one relay reporting, so more than one data point would mean the
// isolation leaked and the numbers below cannot be trusted.
func gaugeValues(t *testing.T, rm metricdata.ResourceMetrics) (depth int64, age float64, found int) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "commerce.outbox.pending":
				g, ok := m.Data.(metricdata.Gauge[int64])
				if !ok || len(g.DataPoints) == 0 {
					continue
				}
				if len(g.DataPoints) != 1 {
					t.Fatalf("%s has %d data points — another relay leaked into this collection", m.Name, len(g.DataPoints))
				}
				depth = g.DataPoints[0].Value
				found++
			case "commerce.outbox.oldest.age":
				g, ok := m.Data.(metricdata.Gauge[float64])
				if !ok || len(g.DataPoints) == 0 {
					continue
				}
				if len(g.DataPoints) != 1 {
					t.Fatalf("%s has %d data points — another relay leaked into this collection", m.Name, len(g.DataPoints))
				}
				age = g.DataPoints[0].Value
				found++
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

	depth, age, found := gaugeValues(t, rm)
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

	depth, age, found := gaugeValues(t, rm)
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
	reader := isolatedReader(t, func() {
		NewOutboxRelay(d, &recordingPublisher{}, time.Hour, 100)
	})

	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	if err == nil {
		t.Fatal("want the callback error surfaced to the reader")
	}
	// Surfacing the failure is the contract; collection still completes rather
	// than hanging or panicking, which would take every other instrument in the
	// process down with it.
	if _, _, found := gaugeValues(t, rm); found != 0 {
		t.Fatalf("reported %d gauge values despite the query failing", found)
	}
}
