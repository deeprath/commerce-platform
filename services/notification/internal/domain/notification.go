// Package domain holds the notification aggregate and the template registry.
// No proto/SQL here.
package domain

import (
	"strings"
	"time"
)

// Channel is how a notification is delivered.
type Channel string

const (
	ChannelEmail Channel = "EMAIL"
	ChannelSMS   Channel = "SMS"
	ChannelPush  Channel = "PUSH"
)

// Status is the delivery outcome.
type Status string

const (
	StatusSent   Status = "SENT"
	StatusFailed Status = "FAILED"
)

// Notification is one rendered, dispatched message.
type Notification struct {
	ID        string
	OwnerID   string
	Kind      string
	Channel   Channel
	Status    Status
	Subject   string
	Body      string
	RefID     string
	CreatedAt time.Time
}

// Rendered is a subject+body pair produced from a template.
type Rendered struct {
	Subject string
	Body    string
}

// tmpl is one message template. Placeholders are "{name}".
type tmpl struct {
	subject string
	body    string
}

// templates maps a notification kind to its template. Kinds are derived from the
// inbound event topic (see internal/consumer).
var templates = map[string]tmpl{
	"order_created": {
		subject: "We received your order {order_id}",
		body:    "Thanks! Order {order_id} for {total} is being processed. We'll email you when payment is confirmed.",
	},
	"order_confirmed": {
		subject: "Your order {order_id} is confirmed",
		body:    "Payment went through and order {order_id} is confirmed. We'll let you know when it ships.",
	},
	"order_cancelled": {
		subject: "Your order {order_id} was cancelled",
		body:    "Order {order_id} was cancelled ({reason}). Any hold on your payment has been released.",
	},
	"order_fulfilled": {
		subject: "Order {order_id} delivered",
		body:    "All items in order {order_id} have been delivered. Thanks for shopping with us!",
	},
	"shipment_shipped": {
		subject: "Your order {order_id} has shipped",
		body:    "Shipment {shipment_id} is on its way via {carrier}. Track it with {tracking_number}.",
	},
	"shipment_delivered": {
		subject: "Your order {order_id} was delivered",
		body:    "Shipment {shipment_id} was delivered. Enjoy!",
	},
	"test": {
		subject: "Test notification",
		body:    "This is a sample notification confirming your delivery settings work.",
	},
}

// Kinds returns the known template kinds (sorted-ish is not required).
func Kinds() []string {
	out := make([]string, 0, len(templates))
	for k := range templates {
		out = append(out, k)
	}
	return out
}

// Render fills a template for kind with data. ok is false for an unknown kind.
func Render(kind string, data map[string]string) (Rendered, bool) {
	t, ok := templates[kind]
	if !ok {
		return Rendered{}, false
	}
	return Rendered{Subject: subst(t.subject, data), Body: subst(t.body, data)}, true
}

func subst(s string, data map[string]string) string {
	if len(data) == 0 {
		return s
	}
	pairs := make([]string, 0, len(data)*2)
	for k, v := range data {
		if v == "" {
			v = "-"
		}
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}
