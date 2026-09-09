package kafka

import "testing"

func TestTopicAndDLQ(t *testing.T) {
	if got := Topic("order", "confirmed"); got != "commerce.order.confirmed" {
		t.Fatalf("Topic = %q", got)
	}
	if got := DLQ("commerce.order.confirmed"); got != "commerce.order.confirmed.dlq" {
		t.Fatalf("DLQ = %q", got)
	}
}
