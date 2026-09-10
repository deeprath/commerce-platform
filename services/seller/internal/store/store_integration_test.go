package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/seller/internal/domain"
	"github.com/deeprath/commerce-platform/services/seller/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("seller"),
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

func mustNew(t *testing.T, owner, name string) *domain.Shop {
	t.Helper()
	sh, err := domain.NewShop(owner, name, "", "")
	if err != nil {
		t.Fatalf("NewShop: %v", err)
	}
	return sh
}

func outboxTopics(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT topic FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func TestStore_CreateGetUpdate(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	created, err := st.Create(ctx, mustNew(t, "owner-1", "Blue Widgets"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Slug != "blue-widgets" || created.Status != domain.StatusPendingReview {
		t.Fatalf("created = %+v", created)
	}

	// second shop for the same owner -> ALREADY_EXISTS
	if _, err := st.Create(ctx, mustNew(t, "owner-1", "Second Try")); !errs.Is(err, errs.KindAlreadyExists) {
		t.Fatalf("dup owner err = %v, want AlreadyExists", err)
	}
	// a different owner reusing the name (same slug) -> ALREADY_EXISTS
	if _, err := st.Create(ctx, mustNew(t, "owner-2", "Blue Widgets")); !errs.Is(err, errs.KindAlreadyExists) {
		t.Fatalf("dup slug err = %v, want AlreadyExists", err)
	}

	byOwner, err := st.GetByOwner(ctx, "owner-1")
	if err != nil || byOwner.ID != created.ID {
		t.Fatalf("GetByOwner: %v / %+v", err, byOwner)
	}
	bySlug, err := st.GetBySlug(ctx, "blue-widgets")
	if err != nil || bySlug.ID != created.ID {
		t.Fatalf("GetBySlug: %v / %+v", err, bySlug)
	}

	updated, err := st.Update(ctx, "owner-1", "Blue Widgets Ltd", "We sell widgets.", "hi@bw.example")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Blue Widgets Ltd" || updated.Slug != "blue-widgets" || updated.ContactEmail != "hi@bw.example" {
		t.Fatalf("updated = %+v (slug must be stable)", updated)
	}

	if _, err := st.GetByOwner(ctx, "nobody"); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("GetByOwner(nobody) err = %v, want NotFound", err)
	}
}

func TestStore_TransitionsEmitEventsIdempotently(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	sh, err := st.Create(ctx, mustNew(t, "owner-9", "Corner Store"))
	if err != nil {
		t.Fatal(err)
	}

	act := func(s *domain.Shop) (bool, error) { return s.Activate() }
	susp := func(s *domain.Shop) (bool, error) { return s.Suspend("policy violation") }

	if got, err := st.Transition(ctx, sh.ID, act); err != nil || got.Status != domain.StatusActive {
		t.Fatalf("activate: %v / %+v", err, got)
	}
	// idempotent: a second activate makes no change and no new event
	if _, err := st.Transition(ctx, sh.ID, act); err != nil {
		t.Fatalf("re-activate: %v", err)
	}
	if got, err := st.Transition(ctx, sh.ID, susp); err != nil || got.Status != domain.StatusSuspended || got.SuspensionReason == "" {
		t.Fatalf("suspend: %v / %+v", err, got)
	}

	topics := outboxTopics(t, pool)
	want := []string{"commerce.shop.created", "commerce.shop.activated", "commerce.shop.suspended"}
	if len(topics) != len(want) {
		t.Fatalf("outbox topics = %v, want exactly %v (idempotent transitions emit once)", topics, want)
	}
	for i := range want {
		if topics[i] != want[i] {
			t.Fatalf("outbox[%d] = %q, want %q", i, topics[i], want[i])
		}
	}
}

func TestStore_MalformedIDIsNotFoundNot500(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	if _, err := st.GetByID(ctx, "not-a-uuid"); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("GetByID(garbage) err = %v, want NotFound", err)
	}
	if _, err := st.Transition(ctx, "", func(*domain.Shop) (bool, error) { return true, nil }); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("Transition(empty id) err = %v, want NotFound", err)
	}
}

func TestStore_ListFiltersByStatus(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	a, _ := st.Create(ctx, mustNew(t, "o-a", "Shop A"))
	_, _ = st.Create(ctx, mustNew(t, "o-b", "Shop B"))
	if _, err := st.Transition(ctx, a.ID, func(s *domain.Shop) (bool, error) { return s.Activate() }); err != nil {
		t.Fatal(err)
	}

	all, _, err := st.List(ctx, domain.Status(""), 10, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %v / %d", err, len(all))
	}
	active, _, err := st.List(ctx, domain.StatusActive, 10, "")
	if err != nil || len(active) != 1 || active[0].ID != a.ID {
		t.Fatalf("list active: %v / %+v", err, active)
	}
	pending, _, err := st.List(ctx, domain.StatusPendingReview, 10, "")
	if err != nil || len(pending) != 1 {
		t.Fatalf("list pending: %v / %d", err, len(pending))
	}
}

func TestStore_ListPaginates(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	for i := 0; i < 3; i++ {
		if _, err := st.Create(ctx, mustNew(t, string(rune('a'+i))+"o", "Page Shop "+string(rune('A'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	p1, next, err := st.List(ctx, domain.Status(""), 2, "")
	if err != nil || len(p1) != 2 || next == "" {
		t.Fatalf("page 1: %v len=%d next=%q", err, len(p1), next)
	}
	p2, next2, err := st.List(ctx, domain.Status(""), 2, next)
	if err != nil || len(p2) != 1 || next2 != "" {
		t.Fatalf("page 2: %v len=%d next=%q", err, len(p2), next2)
	}
}

func TestStore_UpdateAndTransitionNotFound(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	if _, err := st.Update(ctx, "owner-with-no-shop", "Name", "", ""); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("Update(no shop) err = %v, want NotFound", err)
	}
	missing := "22222222-2222-2222-2222-222222222222"
	if _, err := st.Transition(ctx, missing, func(s *domain.Shop) (bool, error) { return s.Activate() }); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("Transition(missing) err = %v, want NotFound", err)
	}
}
