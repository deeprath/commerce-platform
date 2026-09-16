package kafka_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/deeprath/commerce-platform/pkg/kafka"
)

// These run the real consumer loop against kfake, an in-process Kafka that
// speaks the wire protocol. The unit tests in dlq_test.go check what dispatch
// *returns*; only a real poll/commit loop shows what that return value does to
// the group's offsets.

const liveTopic = "commerce.test.events"

var errDownstreamDown = errors.New("downstream down")

func liveCluster(t *testing.T) string {
	t.Helper()
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, liveTopic))
	if err != nil {
		t.Fatalf("kfake: %v", err)
	}
	t.Cleanup(c.Close)
	return c.ListenAddrs()[0]
}

func produce(t *testing.T, broker string, values ...string) {
	t.Helper()
	p, err := kafka.NewProducer(broker)
	if err != nil {
		t.Fatalf("producer: %v", err)
	}
	defer p.Close()
	for _, v := range values {
		if err := p.Publish(context.Background(), liveTopic, nil, []byte(v), nil); err != nil {
			t.Fatalf("produce %s: %v", v, err)
		}
	}
}

// ledger records, in order, every record a handler finished successfully.
type ledger struct {
	mu   sync.Mutex
	done []string
}

func (l *ledger) add(v string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = append(l.done, v)
}

func (l *ledger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.done...)
}

func (l *ledger) has(v string) bool {
	for _, d := range l.snapshot() {
		if d == v {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startConsumer runs a consumer in group until the returned stop is called.
func startConsumer(t *testing.T, broker, group string, h kafka.Handler, dl *kafka.DeadLetter) (stop func()) {
	t.Helper()
	c, err := kafka.NewConsumer(group, []string{liveTopic}, h, broker)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	if dl != nil {
		c.WithDeadLetter(*dl)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	return func() { cancel(); <-done }
}

// A record held because its downstream is down has to be delivered once the
// downstream recovers — before anything after it on the partition, and
// without depending on the process happening to restart in between.
//
// The failure this guards against: holding used to mean only "skip this
// commit". The client's poll position had already moved past the record, so it
// was never offered again, and the next fetch that succeeded committed straight
// over it.
func TestLive_HeldRecordIsDeliveredAfterTheOutage(t *testing.T) {
	broker := liveCluster(t)
	var down atomic.Bool
	down.Store(true)
	var failures atomic.Int32
	got := &ledger{}

	handler := func(_ context.Context, r *kgo.Record) error {
		v := string(r.Value)
		if v == "during-outage" && down.Load() {
			failures.Add(1)
			return errDownstreamDown
		}
		got.add(v)
		return nil
	}
	dl := &kafka.DeadLetter{
		Producer:  kafka.Publisher(nil), // never reached: the failure is retryable
		Attempts:  1,
		Backoff:   10 * time.Millisecond,
		Retryable: func(err error) bool { return errors.Is(err, errDownstreamDown) },
	}

	stop := startConsumer(t, broker, "hold-live", handler, dl)
	produce(t, broker, "before", "during-outage")
	waitFor(t, "the outage to be hit", func() bool { return failures.Load() > 0 })

	// The downstream recovers, and traffic continues.
	down.Store(false)
	produce(t, broker, "after")
	waitFor(t, "the record after the outage", func() bool { return got.has("after") })
	stop()

	// A restart is the only thing that could have rescued the held record under
	// the old behaviour — and by now the commit has already moved past it.
	again := &ledger{}
	stop = startConsumer(t, broker, "hold-live", func(_ context.Context, r *kgo.Record) error {
		again.add(string(r.Value))
		return nil
	}, nil)
	time.Sleep(time.Second)
	stop()

	if !got.has("during-outage") && !again.has("during-outage") {
		t.Fatalf("the record held during the outage was never delivered: first run %v, after restart %v",
			got.snapshot(), again.snapshot())
	}
	// Order within the partition is part of the contract: the saga, for one,
	// must not see a later event for an order before an earlier one.
	if want := []string{"before", "during-outage", "after"}; !equal(got.snapshot(), want) {
		t.Errorf("first run handled %v, want %v in that order", got.snapshot(), want)
	}
}

// A consumer with no DLQ holds on any failure, so the same rule applies.
func TestLive_WithoutDeadLetterAFailedRecordIsRetriedInPlace(t *testing.T) {
	broker := liveCluster(t)
	var calls atomic.Int32
	got := &ledger{}

	stop := startConsumer(t, broker, "hold-nodlq", func(_ context.Context, r *kgo.Record) error {
		v := string(r.Value)
		if v == "flaky" && calls.Add(1) < 3 {
			return errDownstreamDown
		}
		got.add(v)
		return nil
	}, nil)
	defer stop()

	produce(t, broker, "flaky", "next")
	waitFor(t, "both records", func() bool { return got.has("flaky") && got.has("next") })
	if want := []string{"flaky", "next"}; !equal(got.snapshot(), want) {
		t.Errorf("handled %v, want %v", got.snapshot(), want)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
