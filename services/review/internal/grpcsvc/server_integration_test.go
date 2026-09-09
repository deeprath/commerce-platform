package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	reviewv1 "github.com/deeprath/commerce-platform/gen/go/commerce/review/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/review/internal/domain"
	"github.com/deeprath/commerce-platform/services/review/internal/grpcsvc"
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

func customer(sub, email string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Email: email, Roles: []string{"customer"}})
}

func manager(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer", "catalog_manager"}})
}

func TestCreateReview_VerifiedPurchaseGate(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	s := grpcsvc.New(st)

	// Not signed in.
	if _, err := s.CreateReview(ctx, &reviewv1.CreateReviewRequest{ProductId: "p1", Rating: 5, Body: "b"}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("anon: want Unauthenticated, got %v", err)
	}
	// Signed in but never bought p1.
	if _, err := s.CreateReview(customer("u1", "u1@x.com"), &reviewv1.CreateReviewRequest{ProductId: "p1", Rating: 5, Body: "b"}); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("not purchased: want FailedPrecondition, got %v", err)
	}

	// Record a purchase, then the review succeeds and uses the email local-part.
	if err := st.RecordPurchases(ctx, "u1", []string{"p1"}, ""); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	r, err := s.CreateReview(customer("u1", "ada@example.com"), &reviewv1.CreateReviewRequest{
		ProductId: "p1", Rating: 5, Title: "Nice", Body: "Works great",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if r.GetAuthorName() != "ada" || !r.GetVerifiedPurchase() || r.GetRating() != 5 {
		t.Fatalf("bad review proto: %+v", r)
	}
	// Second review by the same user -> ALREADY_EXISTS.
	if _, err := s.CreateReview(customer("u1", "ada@example.com"), &reviewv1.CreateReviewRequest{ProductId: "p1", Rating: 1, Body: "changed my mind"}); !errs.Is(err, errs.KindAlreadyExists) {
		t.Fatalf("dup review: want AlreadyExists, got %v", err)
	}
	// Bad rating -> InvalidArgument.
	_ = st.RecordPurchases(ctx, "u1", []string{"p2"}, "")
	if _, err := s.CreateReview(customer("u1", "ada@example.com"), &reviewv1.CreateReviewRequest{ProductId: "p2", Rating: 9, Body: "b"}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("bad rating: want InvalidArgument, got %v", err)
	}
}

func TestListAndSummary_PublicAndModeration(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	s := grpcsvc.New(st)

	for i, rt := range []int32{5, 3, 4} {
		uid := "buyer" + string(rune('a'+i))
		_ = st.RecordPurchases(ctx, uid, []string{"p1"}, "")
		if _, err := s.CreateReview(customer(uid, uid+"@x.com"), &reviewv1.CreateReviewRequest{ProductId: "p1", Rating: rt, Body: "review body"}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// ListReviews is public (no principal).
	list, err := s.ListReviews(ctx, &reviewv1.ListReviewsRequest{ProductId: "p1"})
	if err != nil || len(list.GetReviews()) != 3 {
		t.Fatalf("public list: %v %d", err, len(list.GetReviews()))
	}
	if _, err := s.ListReviews(ctx, &reviewv1.ListReviewsRequest{}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("list without product_id: want InvalidArgument, got %v", err)
	}

	sum, err := s.GetRatingSummary(ctx, &reviewv1.GetRatingSummaryRequest{ProductId: "p1"})
	if err != nil || sum.GetCount() != 3 || sum.GetAverage() < 3.99 || sum.GetAverage() > 4.01 {
		t.Fatalf("summary: %v %+v", err, sum)
	}
	if len(sum.GetHistogram()) != 5 {
		t.Fatalf("histogram len = %d", len(sum.GetHistogram()))
	}

	// Moderation is role-gated; a customer cannot.
	target := list.GetReviews()[0].GetId()
	if _, err := s.ModerateReview(customer("u1", ""), &reviewv1.ModerateReviewRequest{Id: target, Status: reviewv1.ReviewStatus_REVIEW_STATUS_HIDDEN}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("customer moderate: want PermissionDenied, got %v", err)
	}
	if _, err := s.ModerateReview(manager("mgr"), &reviewv1.ModerateReviewRequest{Id: target, Status: reviewv1.ReviewStatus_REVIEW_STATUS_HIDDEN}); err != nil {
		t.Fatalf("manager moderate: %v", err)
	}
	// The hidden review drops out of the public list and the summary.
	list2, _ := s.ListReviews(ctx, &reviewv1.ListReviewsRequest{ProductId: "p1"})
	if len(list2.GetReviews()) != 2 {
		t.Fatalf("after hide, list = %d, want 2", len(list2.GetReviews()))
	}
	sum2, _ := s.GetRatingSummary(ctx, &reviewv1.GetRatingSummaryRequest{ProductId: "p1"})
	if sum2.GetCount() != 2 {
		t.Fatalf("after hide, summary count = %d, want 2", sum2.GetCount())
	}
	// Invalid moderation target.
	if _, err := s.ModerateReview(manager("mgr"), &reviewv1.ModerateReviewRequest{Id: target, Status: reviewv1.ReviewStatus_REVIEW_STATUS_UNSPECIFIED}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("bad target: want InvalidArgument, got %v", err)
	}

	_ = domain.StatusPublished
}
