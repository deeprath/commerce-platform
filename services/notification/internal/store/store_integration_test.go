package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/proto"

	notificationv1 "github.com/deeprath/commerce-platform/gen/go/commerce/notification/v1"
	"github.com/deeprath/commerce-platform/pkg/pgx"
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

func note(owner, kind, ref string) domain.Notification {
	return domain.Notification{
		OwnerID: owner, Kind: kind, Channel: domain.ChannelEmail, Status: domain.StatusSent,
		Subject: "s", Body: "b", RefID: ref,
	}
}

func TestRecord_PersistsAndEmitsOutboxIdempotently(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	n, err := st.Record(ctx, note("owner-1", "order_confirmed", "order-1"), "evt:0:1")
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if n == nil || n.ID == "" || n.Kind != "order_confirmed" || n.Status != domain.StatusSent {
		t.Fatalf("bad notification: %+v", n)
	}

	// Duplicate event id -> short-circuit, no second row, no second event.
	dup, err := st.Record(ctx, note("owner-1", "order_confirmed", "order-1"), "evt:0:1")
	if err != nil || dup != nil {
		t.Fatalf("duplicate event id should be a no-op: %+v %v", dup, err)
	}

	var rows, events int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&rows)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.notification.sent'`).Scan(&events)
	if rows != 1 || events != 1 {
		t.Fatalf("idempotency broken: rows=%d events=%d", rows, events)
	}

	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM outbox ORDER BY id LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var e notificationv1.NotificationSent
	if err := proto.Unmarshal(payload, &e); err != nil {
		t.Fatalf("event payload: %v", err)
	}
	if e.GetNotificationId() != n.ID || e.GetOwnerId() != "owner-1" || e.GetKind() != "order_confirmed" || e.GetRefId() != "order-1" {
		t.Fatalf("event fields: %+v", &e)
	}

	// Empty event id (RPC path) still records and emits.
	if _, err := st.Record(ctx, note("owner-1", "test", ""), ""); err != nil {
		t.Fatalf("record (no eventID): %v", err)
	}
}

func TestList_OwnerScopedPagination(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	for i := 0; i < 3; i++ {
		if _, err := st.Record(ctx, note("pager", "order_created", "o"), ""); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := st.Record(ctx, note("other", "order_created", "o"), ""); err != nil {
		t.Fatalf("seed other: %v", err)
	}

	p1, next, err := st.List(ctx, "pager", 2, "")
	if err != nil || len(p1) != 2 || next == "" {
		t.Fatalf("page 1: n=%d next=%q err=%v", len(p1), next, err)
	}
	p2, next2, err := st.List(ctx, "pager", 2, next)
	if err != nil || len(p2) != 1 || next2 != "" {
		t.Fatalf("page 2: n=%d next=%q err=%v", len(p2), next2, err)
	}
	// "other" owner is not visible.
	all, _, _ := st.List(ctx, "pager", 50, "")
	for _, n := range all {
		if n.OwnerID != "pager" {
			t.Fatalf("leaked another owner's notification: %+v", n)
		}
	}
}
