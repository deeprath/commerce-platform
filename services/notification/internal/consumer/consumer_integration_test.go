package consumer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/notification/internal/channel"
	"github.com/deeprath/commerce-platform/services/notification/internal/consumer"
	"github.com/deeprath/commerce-platform/services/notification/internal/domain"
	"github.com/deeprath/commerce-platform/services/notification/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("notification"),
		tcpostgres.WithUsername("t"), tcpostgres.WithPassword("t"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.Migrate(ctx, dsn, store.Migrations, "migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgx.NewPool(ctx, pgx.PoolConfig{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type failChannel struct{}

func (failChannel) Send(context.Context, domain.Notification) error {
	return errors.New("provider down")
}

func rec(topic string, offset int64, m proto.Message) *kgo.Record {
	b, _ := proto.Marshal(m)
	return &kgo.Record{Topic: topic, Partition: 0, Offset: offset, Value: b}
}

func TestHandler_EachLifecycleEventBecomesANotification(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	h := consumer.Handler(consumer.Deps{Store: st, Channel: channel.LogChannel{}})

	usd := &commonv1.Money{CurrencyCode: "USD", Units: 12, Nanos: 0}
	events := []struct {
		topic string
		msg   proto.Message
		kind  string
	}{
		{kafka.Topic("order", "created"), &orderv1.OrderCreated{OrderId: "o1", OwnerId: "u1", Total: usd}, "order_created"},
		{kafka.Topic("order", "confirmed"), &orderv1.OrderConfirmed{OrderId: "o1", OwnerId: "u1"}, "order_confirmed"},
		{kafka.Topic("order", "cancelled"), &orderv1.OrderCancelled{OrderId: "o1", OwnerId: "u1", Reason: "PAYMENT_FAILED:CARD_DECLINED"}, "order_cancelled"},
		{kafka.Topic("order", "fulfilled"), &orderv1.OrderFulfilled{OrderId: "o1", OwnerId: "u1"}, "order_fulfilled"},
		{kafka.Topic("fulfillment", "shipped"), &fulfillmentv1.ShipmentShipped{ShipmentId: "s1", OrderId: "o1", OwnerId: "u1", Carrier: "SANDBOX", TrackingNumber: "TRK-1"}, "shipment_shipped"},
		{kafka.Topic("fulfillment", "delivered"), &fulfillmentv1.ShipmentDelivered{ShipmentId: "s1", OrderId: "o1", OwnerId: "u1"}, "shipment_delivered"},
	}
	for i, e := range events {
		if err := h(ctx, rec(e.topic, int64(i), e.msg)); err != nil {
			t.Fatalf("%s: %v", e.kind, err)
		}
	}

	var kinds []string
	rows, _ := pool.Query(ctx, `SELECT kind FROM notifications WHERE owner_id='u1' ORDER BY created_at`)
	defer rows.Close()
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		kinds = append(kinds, k)
	}
	if len(kinds) != len(events) {
		t.Fatalf("got %d notifications, want %d (%v)", len(kinds), len(events), kinds)
	}
	for i, e := range events {
		if kinds[i] != e.kind {
			t.Fatalf("notification %d kind = %s, want %s", i, kinds[i], e.kind)
		}
	}

	// Subject rendered from the template + event data.
	var subject string
	_ = pool.QueryRow(ctx, `SELECT subject FROM notifications WHERE kind='shipment_shipped'`).Scan(&subject)
	if subject == "" || subject == "Your order {order_id} has shipped" {
		t.Fatalf("shipment_shipped subject not rendered: %q", subject)
	}
}

func TestHandler_Idempotent(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	h := consumer.Handler(consumer.Deps{Store: st, Channel: channel.LogChannel{}})

	r := rec(kafka.Topic("order", "confirmed"), 5, &orderv1.OrderConfirmed{OrderId: "o1", OwnerId: "u1"})
	for i := 0; i < 3; i++ {
		if err := h(ctx, r); err != nil {
			t.Fatalf("handle %d: %v", i, err)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&n)
	if n != 1 {
		t.Fatalf("redelivered event created %d notifications, want 1", n)
	}
}

func TestHandler_SkipsAndRecordsFailures(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	// Unknown topic, undecodable payload, and an event with no owner_id are all
	// skipped without error and without a row.
	h := consumer.Handler(consumer.Deps{Store: st, Channel: channel.LogChannel{}})
	for _, r := range []*kgo.Record{
		{Topic: "commerce.payment.authorized", Value: []byte("x")},
		{Topic: kafka.Topic("order", "confirmed"), Value: []byte("not-proto")},
		rec(kafka.Topic("order", "confirmed"), 9, &orderv1.OrderConfirmed{OrderId: "o1"}), // no OwnerId
	} {
		if err := h(ctx, r); err != nil {
			t.Fatalf("skip case: %v", err)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&n)
	if n != 0 {
		t.Fatalf("skip cases created %d rows", n)
	}

	// A channel failure still records the notification, marked FAILED.
	hf := consumer.Handler(consumer.Deps{Store: st, Channel: failChannel{}})
	if err := hf(ctx, rec(kafka.Topic("order", "confirmed"), 10, &orderv1.OrderConfirmed{OrderId: "o1", OwnerId: "u2"})); err != nil {
		t.Fatalf("fail-channel handle: %v", err)
	}
	var status string
	_ = pool.QueryRow(ctx, `SELECT status FROM notifications WHERE owner_id='u2'`).Scan(&status)
	if status != "FAILED" {
		t.Fatalf("channel failure not recorded as FAILED: %q", status)
	}
}

func TestTopics(t *testing.T) {
	if got := consumer.Topics(); len(got) != 6 {
		t.Fatalf("Topics() = %v", got)
	}
}
