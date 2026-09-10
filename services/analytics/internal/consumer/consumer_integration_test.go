package consumer_test

import (
	"context"
	"testing"
	"time"

	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/services/analytics/internal/clickhouse"
	"github.com/deeprath/commerce-platform/services/analytics/internal/consumer"
)

func spinUp(t *testing.T) *clickhouse.Sink {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	ch, err := tcclickhouse.Run(ctx, "clickhouse/clickhouse-server:24.8-alpine",
		tcclickhouse.WithDatabase("analytics"),
		tcclickhouse.WithUsername("t"), tcclickhouse.WithPassword("t"),
	)
	if err != nil {
		t.Fatalf("start clickhouse: %v", err)
	}
	t.Cleanup(func() { _ = ch.Terminate(ctx) })

	dsn, err := ch.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := clickhouse.Open(ctx, dsn, 100)
	if err != nil {
		t.Fatalf("open sink: %v", err)
	}
	t.Cleanup(func() { _ = sink.Close(context.Background()) })
	return sink
}

func rec(topic string, m proto.Message) *kgo.Record {
	b, _ := proto.Marshal(m)
	return &kgo.Record{Topic: topic, Value: b}
}

func usd(u int64, n int32) *commonv1.Money {
	return &commonv1.Money{CurrencyCode: "USD", Units: u, Nanos: n}
}

func TestHandler_FlattensFunnelAndRevenue(t *testing.T) {
	ctx := context.Background()
	sink := spinUp(t)
	h := consumer.Handler(sink)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	events := []*kgo.Record{
		rec(kafka.Topic("order", "created"),
			&orderv1.OrderCreated{OrderId: "o1", OwnerId: "u1", Total: usd(75, 580000000), OccurredAt: now}),
		rec(kafka.Topic("payment", "authorized"),
			&paymentv1.PaymentAuthorized{PaymentId: "p1", OrderId: "o1", Amount: usd(75, 580000000), OccurredAt: now}),
		rec(kafka.Topic("order", "confirmed"),
			&orderv1.OrderConfirmed{OrderId: "o1", OwnerId: "u1", PaymentId: "p1", OccurredAt: now}),
		rec(kafka.Topic("order", "created"),
			&orderv1.OrderCreated{OrderId: "o2", OwnerId: "u2", Total: usd(12, 0), OccurredAt: now}),
		rec(kafka.Topic("payment", "failed"),
			&paymentv1.PaymentFailed{PaymentId: "p2", OrderId: "o2", Reason: "pm_card_declined", OccurredAt: now}),
		rec("commerce.unknown.thing", &orderv1.OrderCreated{OrderId: "x"}), // ignored
	}
	for _, r := range events {
		if err := h(ctx, r); err != nil {
			t.Fatalf("handler(%s): %v", r.Topic, err)
		}
	}
	if err := sink.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	conn := sink.Conn()

	var total uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM events`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Fatalf("row count = %d, want 5 (unknown topic dropped)", total)
	}

	// Funnel by type.
	rows, err := conn.Query(ctx,
		`SELECT event_type, count() FROM events GROUP BY event_type ORDER BY event_type`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]uint64{}
	for rows.Next() {
		var k string
		var n uint64
		if err := rows.Scan(&k, &n); err != nil {
			t.Fatal(err)
		}
		got[k] = n
	}
	for k, want := range map[string]uint64{
		"order_created": 2, "order_confirmed": 1, "payment_authorized": 1, "payment_failed": 1,
	} {
		if got[k] != want {
			t.Errorf("event_type %s = %d, want %d (all: %v)", k, got[k], want, got)
		}
	}

	// Revenue: authorized amount stored as integer cents (75.58 -> 7558).
	var cents int64
	if err := conn.QueryRow(ctx,
		`SELECT sum(amount_minor) FROM events WHERE event_type='payment_authorized'`).Scan(&cents); err != nil {
		t.Fatal(err)
	}
	if cents != 7558 {
		t.Fatalf("authorized revenue = %d cents, want 7558", cents)
	}

	// The rollup MV populated funnel_daily.
	var mvRows uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM funnel_daily`).Scan(&mvRows); err != nil {
		t.Fatal(err)
	}
	if mvRows == 0 {
		t.Fatal("funnel_daily rollup is empty; the materialized view didn't fire")
	}
}
