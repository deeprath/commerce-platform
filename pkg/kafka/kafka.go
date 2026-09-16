// Package kafka wraps franz-go with the platform conventions: topic naming
// commerce.<domain>.<event>, at-least-once delivery, and idempotent consumers.
// The transactional-outbox relay lives in outbox.go.
package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Topic builds a canonical topic name: commerce.<domain>.<event>.
func Topic(domain, event string) string {
	return fmt.Sprintf("commerce.%s.%s", domain, event)
}

// DLQ returns the dead-letter topic for a topic.
func DLQ(topic string) string { return topic + ".dlq" }

// Producer publishes records. Safe for concurrent use.
type Producer struct{ cl *kgo.Client }

// NewProducer connects to the given brokers (comma-separated host:port list is
// accepted by passing multiple strings).
func NewProducer(brokers ...string) (*Producer, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(0),
		// Let the broker create commerce.* topics on first publish in dev; in
		// production topics are created ahead of time with explicit partitions.
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		return nil, err
	}
	return &Producer{cl: cl}, nil
}

// Publish sends one keyed record synchronously and waits for the ack.
func (p *Producer) Publish(ctx context.Context, topic string, key, value []byte, headers map[string]string) error {
	rec := &kgo.Record{Topic: topic, Key: key, Value: value}
	for k, v := range headers {
		rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return p.cl.ProduceSync(ctx, rec).FirstErr()
}

// Close flushes and disconnects.
func (p *Producer) Close() { p.cl.Close() }

// Publisher is the publishing surface *Producer provides. Taking the interface
// rather than the concrete type lets a dead-letter path be exercised without a
// broker, and keeps testcontainers — and Docker's whole dependency tree — out
// of the module every service imports.
type Publisher interface {
	Publish(ctx context.Context, topic string, key, value []byte, headers map[string]string) error
}

// Handler processes one record. Returning an error retries the record up to the
// configured attempts; after that it is parked on the DLQ (see WithDeadLetter),
// or, with no DLQ configured, offsets stop advancing for the group.
type Handler func(ctx context.Context, r *kgo.Record) error

// DeadLetter configures bounded retry and where a record goes when the handler
// keeps rejecting it.
//
// Without this, a record the handler can never accept — a malformed payload, a
// referenced row that will never exist — wedges the group's offsets forever:
// franz-go advances its own cursor, so the record is not retried in-process and
// later records still run, but the commit never moves past it. Lag grows
// without bound and every restart reprocesses the whole backlog from the stuck
// point. Parking the record trades one lost-to-the-stream event, kept for
// inspection, for a group that keeps up.
type DeadLetter struct {
	// Producer publishes the parked record to DLQ(topic). Required.
	// *Producer satisfies Publisher, so callers pass one directly.
	Producer Publisher
	// Attempts is the total number of handler calls before parking. <=0 means 3.
	Attempts int
	// Backoff is the delay before the second attempt, doubling thereafter.
	// <=0 means 200ms.
	Backoff time.Duration
	// Retryable reports whether an error means "try again later" rather than
	// "this record is bad". A retryable failure is never parked: the record is
	// retried where it stands until the condition clears, and the consumer does
	// not move past it, so the backlog waits in Kafka.
	//
	// This is the difference between a malformed payload, which no amount of
	// waiting will fix and which should be set aside so the group can move on,
	// and a downstream that is merely full or down, where parking the record
	// would be throwing away data that was only ever going to be late.
	//
	// Nil treats every error as the record's own fault.
	Retryable func(error) bool
}

// Consumer runs a consumer-group loop invoking Handler per record.
type Consumer struct {
	cl      *kgo.Client
	handler Handler
	dl      *DeadLetter
}

// NewConsumer joins group for the given topics.
func NewConsumer(group string, topics []string, h Handler, brokers ...string) (*Consumer, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return nil, err
	}
	return &Consumer{cl: cl, handler: h}, nil
}

// defaultBackoff is the first retry delay when none is configured. A var so
// tests can shorten it.
var defaultBackoff = 200 * time.Millisecond

// WithDeadLetter enables bounded retry and DLQ parking. Returns the consumer so
// it can be chained onto NewConsumer.
func (c *Consumer) WithDeadLetter(dl DeadLetter) *Consumer {
	if dl.Attempts <= 0 {
		dl.Attempts = 3
	}
	if dl.Backoff <= 0 {
		dl.Backoff = defaultBackoff
	}
	c.dl = &dl
	return c
}

// handleRecord runs the handler, retrying up to the configured attempts. With
// no DeadLetter configured it is a single attempt, matching the previous
// behaviour.
func (c *Consumer) handleRecord(ctx context.Context, r *kgo.Record) error {
	attempts, backoff := 1, time.Duration(0)
	if c.dl != nil {
		attempts, backoff = c.dl.Attempts, c.dl.Backoff
	}
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		if err = c.handler(ctx, r); err == nil {
			return nil
		}
		// A cancelled context is shutdown, not a poison record: stop retrying
		// and let the caller hold the offset so the work is redone on restart.
		if ctx.Err() != nil {
			return err
		}
	}
	return err
}

// park publishes an exhausted record to its DLQ, preserving key and value so it
// can be replayed, with headers recording why it ended up there.
func (c *Consumer) park(ctx context.Context, r *kgo.Record, cause error) error {
	return c.dl.Producer.Publish(ctx, DLQ(r.Topic), r.Key, r.Value, map[string]string{
		"dlq-origin-topic":     r.Topic,
		"dlq-origin-partition": strconv.FormatInt(int64(r.Partition), 10),
		"dlq-origin-offset":    strconv.FormatInt(r.Offset, 10),
		"dlq-error":            cause.Error(),
		"dlq-parked-at":        time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// dispatch handles one record and reports whether its offset may advance.
//
// It returns false only when ctx is cancelled mid-record: that is unfinished
// work, left uncommitted so a restart redoes it. Every other outcome is decided
// here, before the loop moves on, because moving on is itself a decision. The
// client's poll position has already passed this record, so it will never be
// offered again, and the next commit covers it whether or not it was handled.
// "Hold the offset" therefore has to mean "do not return until the record is
// done or parked"; skipping a single commit holds nothing.
func (c *Consumer) dispatch(ctx context.Context, r *kgo.Record) bool {
	err := c.handleRecord(ctx, r)
	if err == nil {
		return true
	}
	slog.ErrorContext(ctx, "kafka handler error",
		slog.String("topic", r.Topic), slog.Int64("offset", r.Offset), slog.Any("err", err))

	// Interrupted by shutdown: unfinished work, not a verdict on the record.
	if ctx.Err() != nil {
		recordHeld(ctx, r.Topic, "shutdown")
		return false
	}
	if c.shouldHold(err) {
		if err = c.retryInPlace(ctx, r, err); err != nil {
			return false // shutting down
		}
		return true
	}
	return c.parkInPlace(ctx, r, err)
}

// shouldHold reports whether a failure means "not yet" rather than "never".
// Without a DLQ there is nowhere to set a record aside, so every failure is
// held — at the cost, as before, of the group not moving past it.
func (c *Consumer) shouldHold(err error) bool {
	return c.dl == nil || (c.dl.Retryable != nil && c.dl.Retryable(err))
}

// retryInPlace re-runs the handler on r, backing off between attempts, until it
// succeeds, fails in a way that is no longer retryable (then it is parked), or
// ctx ends.
//
// This blocks the whole consumer, not just r's partition. That is deliberate:
// a downstream that is down fails every record that needs it, so there is
// nothing useful the other partitions could do meanwhile, and a partition that
// skipped ahead would break the per-key ordering consumers rely on.
func (c *Consumer) retryInPlace(ctx context.Context, r *kgo.Record, err error) error {
	delay := c.holdBackoff()
	for {
		recordHeld(ctx, r.Topic, c.holdReason())
		slog.WarnContext(ctx, "kafka record held; retrying in place",
			slog.String("topic", r.Topic), slog.Int64("offset", r.Offset),
			slog.Duration("retry_in", delay), slog.Any("err", err))
		if !sleep(ctx, delay) {
			return ctx.Err()
		}
		delay = min(delay*2, maxHoldBackoff)

		if err = c.handler(ctx, r); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !c.shouldHold(err) {
			// The downstream came back and turned the record down: it is the
			// record's fault after all.
			if !c.parkInPlace(ctx, r, err) {
				return ctx.Err()
			}
			return nil
		}
	}
}

// parkInPlace sets r aside on its DLQ, retrying the publish until it lands. A
// record that can be neither handled nor parked must not be skipped either.
// Reports false only if ctx ended first.
func (c *Consumer) parkInPlace(ctx context.Context, r *kgo.Record, cause error) bool {
	delay := c.holdBackoff()
	for {
		perr := c.park(ctx, r, cause)
		if perr == nil {
			recordParked(ctx, r.Topic)
			slog.WarnContext(ctx, "kafka record parked on dlq",
				slog.String("topic", r.Topic), slog.String("dlq", DLQ(r.Topic)),
				slog.Int64("offset", r.Offset), slog.Any("err", cause))
			return true
		}
		recordHeld(ctx, r.Topic, "park_failed")
		slog.ErrorContext(ctx, "kafka dlq publish failed; retrying",
			slog.String("topic", r.Topic), slog.Int64("offset", r.Offset),
			slog.Duration("retry_in", delay), slog.Any("err", perr))
		if !sleep(ctx, delay) {
			return false
		}
		delay = min(delay*2, maxHoldBackoff)
	}
}

// maxHoldBackoff caps the wait between in-place retries, so a long outage is
// still noticed within half a minute of it ending.
const maxHoldBackoff = 30 * time.Second

func (c *Consumer) holdBackoff() time.Duration {
	if c.dl != nil {
		return c.dl.Backoff
	}
	return defaultBackoff
}

func (c *Consumer) holdReason() string {
	if c.dl == nil {
		return "no_dlq"
	}
	return "retryable"
}

// sleep waits d, reporting false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// handleFetches dispatches the records in a fetch, in order, and reports
// whether the group's offsets may be committed.
//
// dispatch only gives up on a record when the consumer is shutting down, and
// then nothing after it is touched either: the restart redoes that record
// first, and running later ones now would apply their effects ahead of it and
// then apply them again.
func (c *Consumer) handleFetches(ctx context.Context, fetches kgo.Fetches) bool {
	committable := true
	fetches.EachRecord(func(r *kgo.Record) {
		if committable {
			committable = c.dispatch(ctx, r)
		}
	})
	return committable
}

// Run polls until ctx is cancelled. Offsets are committed only after every
// record in a fetch has been handled without error (at-least-once).
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			c.cl.Close()
			return ctx.Err()
		}
		fetches := c.cl.PollFetches(ctx)
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				slog.ErrorContext(ctx, "kafka fetch error",
					slog.String("topic", e.Topic), slog.Any("err", e.Err))
			}
			continue
		}
		if c.handleFetches(ctx, fetches) {
			if err := c.cl.CommitUncommittedOffsets(ctx); err != nil {
				slog.ErrorContext(ctx, "kafka commit error", slog.Any("err", err))
			}
		}
	}
}
