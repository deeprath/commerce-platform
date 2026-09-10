// Package events publishes browser clickstream events onto the Kafka backbone.
// The BFF is the only HTTP producer on the platform; every other service emits
// via the transactional outbox. Clickstream has no such durability need — a
// dropped beacon is an acceptable loss — so it publishes directly.
package events

import (
	"context"

	"github.com/deeprath/commerce-platform/pkg/kafka"
)

// KafkaPublisher fans enriched clickstream events onto a single topic, keyed by
// the visitor's anonymous id so one visitor's events stay ordered on a
// partition.
type KafkaPublisher struct {
	p     *kafka.Producer
	topic string
}

// NewKafkaPublisher connects a producer to brokers (a comma-separated list is
// accepted as one string).
func NewKafkaPublisher(topic string, brokers ...string) (*KafkaPublisher, error) {
	p, err := kafka.NewProducer(brokers...)
	if err != nil {
		return nil, err
	}
	return &KafkaPublisher{p: p, topic: topic}, nil
}

// Publish sends one already-marshalled ClientEvent. Synchronous with an ack; the
// caller treats an error as best-effort (log and move on).
func (k *KafkaPublisher) Publish(ctx context.Context, key string, value []byte) error {
	return k.p.Publish(ctx, k.topic, []byte(key), value, nil)
}

// Close flushes and disconnects.
func (k *KafkaPublisher) Close() { k.p.Close() }
