package consumer_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/consumer"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("fulfillment"),
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

func record(t *testing.T, offset int64, e *orderv1.OrderConfirmed) *kgo.Record {
	t.Helper()
	b, err := proto.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{Topic: kafka.Topic("order", "confirmed"), Partition: 0, Offset: offset, Value: b}
}

func TestHandler_CreatesShipmentFromEventAndDedupes(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	h := consumer.Handler(st)

	evt := &orderv1.OrderConfirmed{
		OrderId: "order-1", OwnerId: "owner-1",
		ShipTo: &commonv1.Address{FullName: "Buyer", Line1: "1 Main St", City: "Springfield", Region: "IL", PostalCode: "62701", CountryCode: "US"},
		Lines: []*orderv1.OrderLine{
			{ProductId: "p1", Title: "Desk Lamp", Quantity: 2},
			{ProductId: "p2", Title: "Notebook", Quantity: 1},
		},
	}

	if err := h(ctx, record(t, 1, evt)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	var (
		status, name string
		nItems       int
	)
	if err := pool.QueryRow(ctx, `
		SELECT status, ship_to->>'FullName', jsonb_array_length(items)
		FROM shipments WHERE order_id = 'order-1'`).Scan(&status, &name, &nItems); err != nil {
		t.Fatalf("row: %v", err)
	}
	if status != "PENDING" || name != "Buyer" || nItems != 2 {
		t.Fatalf("shipment not built from event: status=%s name=%s items=%d", status, name, nItems)
	}

	// Same record again (redelivery) -> no error, still exactly one shipment and
	// one shipment_created outbox row.
	if err := h(ctx, record(t, 1, evt)); err != nil {
		t.Fatalf("redeliver same offset: %v", err)
	}
	// A different offset for the same order -> still one shipment, one event.
	if err := h(ctx, record(t, 7, evt)); err != nil {
		t.Fatalf("redeliver new offset: %v", err)
	}

	var shipments, events int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM shipments WHERE order_id='order-1'`).Scan(&shipments)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.fulfillment.shipment_created'`).Scan(&events)
	if shipments != 1 || events != 1 {
		t.Fatalf("idempotency broken: shipments=%d events=%d", shipments, events)
	}
}

func TestHandler_IgnoresOtherTopicsAndBadPayloads(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	h := consumer.Handler(st)

	// Wrong topic -> no-op.
	if err := h(ctx, &kgo.Record{Topic: "commerce.payment.authorized", Value: []byte("x")}); err != nil {
		t.Fatalf("other topic should be ignored: %v", err)
	}
	// Undecodable payload on the right topic -> skipped, not retried forever.
	if err := h(ctx, &kgo.Record{Topic: kafka.Topic("order", "confirmed"), Value: []byte("not-proto")}); err != nil {
		t.Fatalf("bad payload should be skipped: %v", err)
	}
}
