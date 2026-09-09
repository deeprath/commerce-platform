// Package kafka wraps franz-go with the platform conventions: topic naming
// commerce.<domain>.<event>, at-least-once delivery, and idempotent consumers.
// The transactional-outbox relay lives in outbox.go.
package kafka

import (
	"context"
	"fmt"
	"log/slog"

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

// Handler processes one record. Returning an error causes the record to be
// retried; after the caller's retry budget it should be routed to the DLQ.
type Handler func(ctx context.Context, r *kgo.Record) error

// Consumer runs a consumer-group loop invoking Handler per record.
type Consumer struct {
	cl      *kgo.Client
	handler Handler
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
		var failed bool
		fetches.EachRecord(func(r *kgo.Record) {
			if err := c.handler(ctx, r); err != nil {
				failed = true
				slog.ErrorContext(ctx, "kafka handler error",
					slog.String("topic", r.Topic), slog.Int64("offset", r.Offset), slog.Any("err", err))
			}
		})
		if !failed {
			if err := c.cl.CommitUncommittedOffsets(ctx); err != nil {
				slog.ErrorContext(ctx, "kafka commit error", slog.Any("err", err))
			}
		}
	}
}
