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

	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/review/internal/consumer"
	"github.com/deeprath/commerce-platform/services/review/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("review"),
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

func TestHandler_RecordsPurchasesFromConfirmedOrder(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	h := consumer.Handler(st)

	evt := &orderv1.OrderConfirmed{
		OrderId: "o1", OwnerId: "u1",
		Lines: []*orderv1.OrderLine{
			{ProductId: "p1", Title: "Desk Lamp", Quantity: 2},
			{ProductId: "p2", Title: "Notebook", Quantity: 1},
		},
	}
	b, _ := proto.Marshal(evt)
	r := &kgo.Record{Topic: kafka.Topic("order", "confirmed"), Partition: 0, Offset: 3, Value: b}

	for i := 0; i < 2; i++ { // redelivery is a no-op
		if err := h(ctx, r); err != nil {
			t.Fatalf("handle %d: %v", i, err)
		}
	}

	for _, pid := range []string{"p1", "p2"} {
		ok, err := st.HasPurchased(ctx, "u1", pid)
		if err != nil || !ok {
			t.Fatalf("HasPurchased(u1,%s) = %v,%v", pid, ok, err)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM purchases`).Scan(&n)
	if n != 2 {
		t.Fatalf("purchases rows = %d, want 2", n)
	}
}

func TestHandler_IgnoresOtherTopicsAndBadPayloads(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	h := consumer.Handler(st)

	if err := h(ctx, &kgo.Record{Topic: "commerce.payment.authorized", Value: []byte("x")}); err != nil {
		t.Fatalf("other topic: %v", err)
	}
	if err := h(ctx, &kgo.Record{Topic: kafka.Topic("order", "confirmed"), Value: []byte("nope")}); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
}

func TestTopics(t *testing.T) {
	if got := consumer.Topics(); len(got) != 1 || got[0] != kafka.Topic("order", "confirmed") {
		t.Fatalf("Topics() = %v", got)
	}
}
