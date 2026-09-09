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

	reviewv1 "github.com/deeprath/commerce-platform/gen/go/commerce/review/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/review/internal/domain"
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

func mustReview(t *testing.T, pid, uid string, rating int32) *domain.Review {
	t.Helper()
	r, err := domain.NewReview(pid, uid, "Tester", rating, "T", "body text")
	if err != nil {
		t.Fatalf("NewReview: %v", err)
	}
	return r
}

func TestPurchasesIndexAndVerification(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	if err := st.RecordPurchases(ctx, "u1", []string{"p1", "p2", ""}, "order.confirmed:0:1"); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Redelivery of the same event is a no-op.
	if err := st.RecordPurchases(ctx, "u1", []string{"p1"}, "order.confirmed:0:1"); err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	// A later confirmed order adds more.
	if err := st.RecordPurchases(ctx, "u1", []string{"p2", "p3"}, "order.confirmed:0:9"); err != nil {
		t.Fatalf("second order: %v", err)
	}

	for pid, want := range map[string]bool{"p1": true, "p2": true, "p3": true, "p9": false} {
		got, err := st.HasPurchased(ctx, "u1", pid)
		if err != nil || got != want {
			t.Fatalf("HasPurchased(u1,%s) = %v,%v want %v", pid, got, err, want)
		}
	}
	if got, _ := st.HasPurchased(ctx, "u2", "p1"); got {
		t.Fatal("purchases must be per-owner")
	}
}

func TestCreate_OneReviewPerCustomerAndOutbox(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	r, err := st.Create(ctx, mustReview(t, "p1", "u1", 4))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if r.ID == "" || r.Status != domain.StatusPublished {
		t.Fatalf("bad review: %+v", r)
	}

	// Same author + product -> ALREADY_EXISTS.
	if _, err := st.Create(ctx, mustReview(t, "p1", "u1", 2)); !errs.Is(err, errs.KindAlreadyExists) {
		t.Fatalf("second review: want AlreadyExists, got %v", err)
	}
	// A different author can review the same product.
	if _, err := st.Create(ctx, mustReview(t, "p1", "u2", 5)); err != nil {
		t.Fatalf("other author: %v", err)
	}

	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.review.published'`).Scan(&n)
	if n != 2 {
		t.Fatalf("expected 2 review.published events, got %d", n)
	}
	var payload []byte
	_ = pool.QueryRow(ctx, `SELECT payload FROM outbox ORDER BY id LIMIT 1`).Scan(&payload)
	var e reviewv1.ReviewPublished
	if err := proto.Unmarshal(payload, &e); err != nil || e.GetProductId() != "p1" || e.GetRating() != 4 {
		t.Fatalf("event payload bad: %v %+v", err, &e)
	}
}

func TestListPublishedAndSummary(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	ratings := []int32{5, 4, 5, 1}
	for i, rt := range ratings {
		if _, err := st.Create(ctx, mustReview(t, "p1", "u"+string(rune('a'+i)), rt)); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	// One review on another product, and one hidden review on p1.
	if _, err := st.Create(ctx, mustReview(t, "p2", "ux", 3)); err != nil {
		t.Fatalf("seed p2: %v", err)
	}
	hid, _ := st.Create(ctx, mustReview(t, "p1", "uz", 2))
	if _, err := st.Moderate(ctx, hid.ID, domain.StatusHidden); err != nil {
		t.Fatalf("hide: %v", err)
	}

	// Summary counts only published p1 reviews: 5,4,5,1 -> avg 3.75, count 4.
	sum, err := st.Summary(ctx, "p1")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Count != 4 || sum.Average < 3.74 || sum.Average > 3.76 {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.Histogram != [5]int32{1, 0, 0, 1, 2} {
		t.Fatalf("histogram = %v", sum.Histogram)
	}

	// List is published-only, newest first, paginated.
	p1, next, err := st.ListPublished(ctx, "p1", 2, "")
	if err != nil || len(p1) != 2 || next == "" {
		t.Fatalf("page 1: n=%d next=%q err=%v", len(p1), next, err)
	}
	p2, next2, err := st.ListPublished(ctx, "p1", 2, next)
	if err != nil || len(p2) != 2 || next2 != "" {
		t.Fatalf("page 2: n=%d next=%q err=%v", len(p2), next2, err)
	}
	for _, r := range append(p1, p2...) {
		if r.Status != domain.StatusPublished || r.ProductID != "p1" {
			t.Fatalf("list leaked %+v", r)
		}
	}
}

func TestModerate_IdempotentAndEvents(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	r, _ := st.Create(ctx, mustReview(t, "p1", "u1", 4))

	hidden, err := st.Moderate(ctx, r.ID, domain.StatusHidden)
	if err != nil || hidden.Status != domain.StatusHidden {
		t.Fatalf("hide: %v %+v", err, hidden)
	}
	// Idempotent.
	if _, err := st.Moderate(ctx, r.ID, domain.StatusHidden); err != nil {
		t.Fatalf("hide again: %v", err)
	}
	// Re-publish.
	pub, err := st.Moderate(ctx, r.ID, domain.StatusPublished)
	if err != nil || pub.Status != domain.StatusPublished {
		t.Fatalf("republish: %v %+v", err, pub)
	}
	// Missing id.
	if _, err := st.Moderate(ctx, "00000000-0000-0000-0000-000000000000", domain.StatusHidden); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("moderate missing: want NotFound, got %v", err)
	}

	var pubN, hidN int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.review.published'`).Scan(&pubN)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.review.hidden'`).Scan(&hidN)
	if pubN != 2 || hidN != 1 { // create + republish; one hide (idempotent 2nd emits nothing)
		t.Fatalf("events: published=%d hidden=%d", pubN, hidN)
	}
}
