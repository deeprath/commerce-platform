package kafka

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

type capturedRecord struct {
	topic   string
	key     []byte
	value   []byte
	headers map[string]string
}

type fakePublisher struct {
	mu   sync.Mutex
	recs []capturedRecord
	err  error
}

func (p *fakePublisher) Publish(_ context.Context, topic string, key, value []byte, headers map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.recs = append(p.recs, capturedRecord{topic: topic, key: key, value: value, headers: headers})
	return nil
}

func (p *fakePublisher) parked() []capturedRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]capturedRecord(nil), p.recs...)
}

func rec() *kgo.Record {
	return &kgo.Record{
		Topic: "commerce.order.confirmed", Partition: 2, Offset: 41,
		Key: []byte("order-1"), Value: []byte("payload"),
	}
}

// A transient failure must not cost the record: the retry budget exists so a
// blip doesn't park something that would have succeeded a moment later.
func TestHandleRecord_RetriesUntilItSucceeds(t *testing.T) {
	var calls int
	c := &Consumer{handler: func(context.Context, *kgo.Record) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	}}
	c.WithDeadLetter(DeadLetter{Producer: &fakePublisher{}, Attempts: 3, Backoff: time.Millisecond})

	if err := c.handleRecord(context.Background(), rec()); err != nil {
		t.Fatalf("handleRecord: %v", err)
	}
	if calls != 3 {
		t.Fatalf("handler called %d times, want 3 (retried until it succeeded)", calls)
	}
}

func TestHandleRecord_StopsAfterTheAttemptBudget(t *testing.T) {
	var calls int
	c := &Consumer{handler: func(context.Context, *kgo.Record) error {
		calls++
		return errors.New("poison")
	}}
	c.WithDeadLetter(DeadLetter{Producer: &fakePublisher{}, Attempts: 4, Backoff: time.Millisecond})

	if err := c.handleRecord(context.Background(), rec()); err == nil {
		t.Fatal("handleRecord succeeded on a permanently failing handler")
	}
	if calls != 4 {
		t.Fatalf("handler called %d times, want exactly the 4-attempt budget", calls)
	}
}

// Without a DLQ the behaviour is unchanged from before: one attempt, and the
// offset is held.
func TestHandleRecord_WithoutDeadLetterIsASingleAttempt(t *testing.T) {
	var calls int
	c := &Consumer{handler: func(context.Context, *kgo.Record) error {
		calls++
		return errors.New("nope")
	}}
	if err := c.handleRecord(context.Background(), rec()); err == nil {
		t.Fatal("want the handler error")
	}
	if calls != 1 {
		t.Fatalf("handler called %d times, want 1 without a retry budget", calls)
	}
}

// The point of the change: a record the handler can never accept stops wedging
// the group. It is parked, and the offset is allowed to advance.
func TestDispatch_ParksAPoisonRecordAndLetsTheOffsetAdvance(t *testing.T) {
	pub := &fakePublisher{}
	c := &Consumer{handler: func(context.Context, *kgo.Record) error {
		return errors.New("malformed payload")
	}}
	c.WithDeadLetter(DeadLetter{Producer: pub, Attempts: 2, Backoff: time.Millisecond})

	if !c.dispatch(context.Background(), rec()) {
		t.Fatal("offset held for a parked record — the group would still be stuck")
	}

	parked := pub.parked()
	if len(parked) != 1 {
		t.Fatalf("parked %d records, want 1", len(parked))
	}
	p := parked[0]
	if p.topic != "commerce.order.confirmed.dlq" {
		t.Fatalf("dlq topic = %q", p.topic)
	}
	// Key and value must survive so the record can be replayed.
	if string(p.key) != "order-1" || string(p.value) != "payload" {
		t.Fatalf("parked record not preserved: key=%q value=%q", p.key, p.value)
	}
	// And enough context to work out what happened without the original log.
	for _, h := range []string{"dlq-origin-topic", "dlq-origin-partition", "dlq-origin-offset", "dlq-error", "dlq-parked-at"} {
		if p.headers[h] == "" {
			t.Fatalf("header %q missing: %+v", h, p.headers)
		}
	}
	if p.headers["dlq-origin-offset"] != "41" || p.headers["dlq-origin-partition"] != "2" {
		t.Fatalf("origin coordinates wrong: %+v", p.headers)
	}
	if p.headers["dlq-error"] != "malformed payload" {
		t.Fatalf("dlq-error = %q", p.headers["dlq-error"])
	}
}

// With no DLQ configured, a failure must still hold the offset — losing the
// record silently would be worse than the backlog.
func TestDispatch_WithoutDeadLetterHoldsTheOffset(t *testing.T) {
	c := &Consumer{handler: func(context.Context, *kgo.Record) error {
		return errors.New("nope")
	}}
	if c.dispatch(context.Background(), rec()) {
		t.Fatal("offset advanced past a failed record with no DLQ to park it on")
	}
}

// If the record cannot even be parked, holding the offset is the only way not
// to drop it.
func TestDispatch_HoldsTheOffsetWhenParkingFails(t *testing.T) {
	pub := &fakePublisher{err: errors.New("broker down")}
	c := &Consumer{handler: func(context.Context, *kgo.Record) error {
		return errors.New("poison")
	}}
	c.WithDeadLetter(DeadLetter{Producer: pub, Attempts: 1})

	if c.dispatch(context.Background(), rec()) {
		t.Fatal("offset advanced although the record was never parked — it would be lost")
	}
}

// Shutdown is not a poison record. A cancelled context must hold the offset so
// the work is redone on restart, not parked as if the payload were bad.
func TestDispatch_CancelledContextHoldsTheOffsetRatherThanParking(t *testing.T) {
	pub := &fakePublisher{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &Consumer{handler: func(context.Context, *kgo.Record) error {
		return errors.New("interrupted mid-write")
	}}
	c.WithDeadLetter(DeadLetter{Producer: pub, Attempts: 3, Backoff: time.Millisecond})

	if c.dispatch(ctx, rec()) {
		t.Fatal("offset advanced during shutdown — unfinished work would be skipped")
	}
	if len(pub.parked()) != 0 {
		t.Fatal("record parked on the DLQ during shutdown; it should be retried on restart")
	}
}

func TestDispatch_SuccessAdvancesAndParksNothing(t *testing.T) {
	pub := &fakePublisher{}
	c := &Consumer{handler: func(context.Context, *kgo.Record) error { return nil }}
	c.WithDeadLetter(DeadLetter{Producer: pub})

	if !c.dispatch(context.Background(), rec()) {
		t.Fatal("offset held for a record that handled cleanly")
	}
	if len(pub.parked()) != 0 {
		t.Fatalf("parked %d records on success", len(pub.parked()))
	}
}

func TestWithDeadLetter_AppliesDefaults(t *testing.T) {
	c := (&Consumer{}).WithDeadLetter(DeadLetter{Producer: &fakePublisher{}})
	if c.dl.Attempts != 3 || c.dl.Backoff != 200*time.Millisecond {
		t.Fatalf("defaults = %d attempts / %v backoff", c.dl.Attempts, c.dl.Backoff)
	}
}

// A downstream that is full or unreachable will take the record later; parking
// it would discard data that was only ever going to be late. Such a failure
// holds the offset so the backlog waits in Kafka instead.
func TestDispatch_RetryableFailureHoldsTheOffsetInsteadOfParking(t *testing.T) {
	errFull := errors.New("sink buffer full")
	pub := &fakePublisher{}
	c := &Consumer{handler: func(context.Context, *kgo.Record) error { return errFull }}
	c.WithDeadLetter(DeadLetter{
		Producer:  pub,
		Attempts:  2,
		Backoff:   time.Millisecond,
		Retryable: func(err error) bool { return errors.Is(err, errFull) },
	})

	if c.dispatch(context.Background(), rec()) {
		t.Fatal("offset advanced past a record the sink will accept later — the event is lost")
	}
	if len(pub.parked()) != 0 {
		t.Fatal("backpressure parked on the DLQ; it should wait in Kafka")
	}
}

// The predicate must still let genuinely bad records through to the DLQ, or it
// would reintroduce the wedge it exists to avoid.
func TestDispatch_RetryablePredicateStillParksOtherFailures(t *testing.T) {
	pub := &fakePublisher{}
	c := &Consumer{handler: func(context.Context, *kgo.Record) error {
		return errors.New("malformed payload")
	}}
	c.WithDeadLetter(DeadLetter{
		Producer:  pub,
		Attempts:  1,
		Retryable: func(err error) bool { return errors.Is(err, context.DeadlineExceeded) },
	})

	if !c.dispatch(context.Background(), rec()) {
		t.Fatal("offset held for a poison record despite a DLQ being configured")
	}
	if len(pub.parked()) != 1 {
		t.Fatalf("parked %d records, want 1", len(pub.parked()))
	}
}

func fetchOf(recs ...*kgo.Record) kgo.Fetches {
	return kgo.Fetches{{
		Topics: []kgo.FetchTopic{{
			Topic: "commerce.order.confirmed",
			Partitions: []kgo.FetchPartition{{
				Partition: 2,
				Records:   recs,
			}},
		}},
	}}
}

// One unhandleable record holds the commit for the whole fetch: the offset is
// per-partition, so there is no way to skip past just that one.
func TestHandleFetches_OneHeldRecordHoldsTheWholeCommit(t *testing.T) {
	var seen int
	c := &Consumer{handler: func(_ context.Context, r *kgo.Record) error {
		seen++
		if r.Offset == 42 {
			return errors.New("poison")
		}
		return nil
	}}
	// No DLQ, so the poison record cannot be parked and must hold the commit.
	if c.handleFetches(context.Background(), fetchOf(
		&kgo.Record{Topic: "commerce.order.confirmed", Offset: 41},
		&kgo.Record{Topic: "commerce.order.confirmed", Offset: 42},
		&kgo.Record{Topic: "commerce.order.confirmed", Offset: 43},
	)) {
		t.Fatal("commit allowed despite a record that could not be handled")
	}
	if seen != 3 {
		t.Fatalf("handled %d records, want all 3 attempted", seen)
	}
}

// With a DLQ the poison record is parked, so the fetch becomes committable and
// the group keeps moving — the whole point of the change.
func TestHandleFetches_ParkedRecordsDoNotHoldTheCommit(t *testing.T) {
	pub := &fakePublisher{}
	c := &Consumer{handler: func(_ context.Context, r *kgo.Record) error {
		if r.Offset == 42 {
			return errors.New("poison")
		}
		return nil
	}}
	c.WithDeadLetter(DeadLetter{Producer: pub, Attempts: 1})

	if !c.handleFetches(context.Background(), fetchOf(
		&kgo.Record{Topic: "commerce.order.confirmed", Offset: 41},
		&kgo.Record{Topic: "commerce.order.confirmed", Offset: 42},
		&kgo.Record{Topic: "commerce.order.confirmed", Offset: 43},
	)) {
		t.Fatal("commit held although the only failure was parked")
	}
	if len(pub.parked()) != 1 {
		t.Fatalf("parked %d records, want the single poison one", len(pub.parked()))
	}
}

func TestHandleFetches_CleanFetchIsCommittable(t *testing.T) {
	c := &Consumer{handler: func(context.Context, *kgo.Record) error { return nil }}
	if !c.handleFetches(context.Background(), fetchOf(
		&kgo.Record{Topic: "commerce.order.confirmed", Offset: 1},
	)) {
		t.Fatal("clean fetch not committable")
	}
}
