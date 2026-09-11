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
	"github.com/deeprath/commerce-platform/services/payout/internal/consumer"
	"github.com/deeprath/commerce-platform/services/payout/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("payout"),
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

func money(units int64) *commonv1.Money { return &commonv1.Money{CurrencyCode: "USD", Units: units} }

func TestHandler_CreatesPayoutsPerShopAndDedupes(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	h := consumer.Handler(st)

	evt := &orderv1.OrderConfirmed{
		OrderId: "order-1", OwnerId: "owner-1",
		Lines: []*orderv1.OrderLine{
			{ProductId: "fp", LineTotal: money(10)},
			{ProductId: "a1", ShopId: "shop-a", LineTotal: money(15)},
			{ProductId: "a2", ShopId: "shop-a", LineTotal: money(5)},
			{ProductId: "b1", ShopId: "shop-b", LineTotal: money(20)},
		},
	}
	if err := h(ctx, record(t, 1, evt)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	rows, err := pool.Query(ctx, `SELECT shop_id, amount_cents FROM payouts WHERE order_id = 'order-1' ORDER BY shop_id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var shop string
		var cents int64
		if err := rows.Scan(&shop, &cents); err != nil {
			t.Fatal(err)
		}
		got[shop] = cents
	}
	// shop-a's two lines (15+5 => $20.00 => 2000 cents) combine into one payout;
	// the first-party line ("") produces no payout at all.
	want := map[string]int64{"shop-a": 2000, "shop-b": 2000}
	if len(got) != len(want) || got["shop-a"] != want["shop-a"] || got["shop-b"] != want["shop-b"] {
		t.Fatalf("payouts by shop = %v, want %v", got, want)
	}

	// Redelivery (same offset, then a new offset for the same order) does not
	// create duplicates.
	if err := h(ctx, record(t, 1, evt)); err != nil {
		t.Fatalf("redeliver same offset: %v", err)
	}
	if err := h(ctx, record(t, 7, evt)); err != nil {
		t.Fatalf("redeliver new offset: %v", err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM payouts WHERE order_id='order-1'`).Scan(&n)
	if n != 2 {
		t.Fatalf("idempotency broken: %d payouts, want 2", n)
	}
}

func TestTopics(t *testing.T) {
	got := consumer.Topics()
	if len(got) != 1 || got[0] != kafka.Topic("order", "confirmed") {
		t.Fatalf("Topics() = %v", got)
	}
}

func TestHandler_IgnoresOtherTopicsAndBadPayloads(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	h := consumer.Handler(st)

	if err := h(ctx, &kgo.Record{Topic: "commerce.payment.authorized", Value: []byte("x")}); err != nil {
		t.Fatalf("other topic should be ignored: %v", err)
	}
	if err := h(ctx, &kgo.Record{Topic: kafka.Topic("order", "confirmed"), Value: []byte("not-proto")}); err != nil {
		t.Fatalf("bad payload should be skipped: %v", err)
	}
}
