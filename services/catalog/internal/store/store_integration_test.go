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

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/catalog/internal/domain"
	"github.com/deeprath/commerce-platform/services/catalog/internal/store"
)

// spinUp starts a throwaway Postgres, runs migrations, and returns a pool.
func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()

	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("catalog"),
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

func newDraft(t *testing.T, slug string) *domain.Product {
	t.Helper()
	p, err := domain.NewProduct(slug, "T "+slug, "d", "cat-a",
		domain.Money{CurrencyCode: "USD", Units: 1999}, []string{"m1"},
		map[string]string{"color": "blue"}, "mgr-1")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateReadArchiveAndOutbox(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	// Create
	created, err := st.Create(ctx, newDraft(t, "widget-one"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.Status != domain.StatusDraft {
		t.Fatalf("bad created product: %+v", created)
	}

	// Duplicate slug -> AlreadyExists
	if _, err := st.Create(ctx, newDraft(t, "widget-one")); !errs.Is(err, errs.KindAlreadyExists) {
		t.Fatalf("dup slug => %v", err)
	}

	// Get by id and slug
	got, err := st.Get(ctx, created.ID)
	if err != nil || got.Slug != "widget-one" {
		t.Fatalf("get by id: %+v %v", got, err)
	}
	if _, err := st.GetBySlug(ctx, "widget-one"); err != nil {
		t.Fatalf("get by slug: %v", err)
	}
	if _, err := st.Get(ctx, "00000000-0000-0000-0000-000000000000"); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("missing id => %v", err)
	}

	// Activate then it shows in List
	if err := got.ApplyUpdate(got.Title, got.Description, got.CategoryID, got.ListPrice,
		got.MediaKeys, got.Attributes, domain.StatusActive); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, _, err := st.List(ctx, "", 10, nil)
	if err != nil || len(list) != 1 {
		t.Fatalf("list active: %d %v", len(list), err)
	}

	// Archive removes it from List
	if _, err := st.Archive(ctx, created.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	list, _, _ = st.List(ctx, "", 10, nil)
	if len(list) != 0 {
		t.Fatalf("archived product still listed: %d", len(list))
	}

	// Outbox: one row per write (create, update, archive) = 3, topic + decodable payload
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.catalog.product_changed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("expected 3 outbox rows, got %d", n)
	}
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM outbox ORDER BY id LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt catalogv1.ProductChanged
	if err := proto.Unmarshal(payload, &evt); err != nil {
		t.Fatalf("outbox payload not a ProductChanged: %v", err)
	}
	if evt.GetChange() != catalogv1.ChangeType_CHANGE_TYPE_CREATED || evt.GetProductId() != created.ID {
		t.Fatalf("unexpected first event: %+v", &evt)
	}
}

func TestListKeysetPagination(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	for i := 0; i < 5; i++ {
		p := newDraft(t, "page-"+string(rune('a'+i)))
		created, err := st.Create(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		created.Status = domain.StatusActive
		if _, err := st.Update(ctx, created); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct created_at ordering
	}

	page1, next, err := st.List(ctx, "", 2, nil)
	if err != nil || len(page1) != 2 || next == nil {
		t.Fatalf("page1: len=%d next=%v err=%v", len(page1), next, err)
	}
	page2, next2, err := st.List(ctx, "", 2, next)
	if err != nil || len(page2) != 2 || next2 == nil {
		t.Fatalf("page2: len=%d err=%v", len(page2), err)
	}
	if page1[0].ID == page2[0].ID {
		t.Fatal("pages overlap")
	}
	page3, next3, _ := st.List(ctx, "", 2, next2)
	if len(page3) != 1 || next3 != nil {
		t.Fatalf("page3 should be the last one: len=%d next=%v", len(page3), next3)
	}
}
