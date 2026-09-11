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

	payoutv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payout/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/payout/internal/domain"
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

func usd(cents int64) domain.Money { return domain.Money{Currency: "USD", Cents: cents} }

func TestCreateFromOrder_OneShopGroupAndIdempotency(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	created, err := st.CreateFromOrder(ctx, "order-1",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: usd(1500)}}, "e:0:1")
	if err != nil || len(created) != 1 {
		t.Fatalf("create: %v %+v", err, created)
	}
	p := created[0]
	if p.OrderID != "order-1" || p.ShopID != "shop-a" || p.Amount.Cents != 1500 || p.Status != domain.StatusPending {
		t.Fatalf("bad payout: %+v", p)
	}

	// Same event id again -> short-circuit, no new payout, no extra outbox row.
	dup, err := st.CreateFromOrder(ctx, "order-1",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: usd(1500)}}, "e:0:1")
	if err != nil || dup != nil {
		t.Fatalf("duplicate event id should short-circuit: %+v %v", dup, err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.payout.created'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 payout_created event, got %d", n)
	}
}

// First-party lines (shop_id "") create no payout; only marketplace shops do.
func TestCreateFromOrder_SkipsFirstPartyGroup(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	created, err := st.CreateFromOrder(ctx, "order-mixed", []store.ShopAmount{
		{ShopID: "", Amount: usd(1000)},
		{ShopID: "shop-a", Amount: usd(1500)},
		{ShopID: "shop-b", Amount: usd(2500)},
	}, "e:mixed:1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("want 2 payouts (first-party skipped), got %+v", created)
	}
	byShop := map[string]int64{}
	for _, p := range created {
		byShop[p.ShopID] = p.Amount.Cents
	}
	if byShop["shop-a"] != 1500 || byShop["shop-b"] != 2500 {
		t.Fatalf("wrong amounts: %+v", byShop)
	}
}

// A partially-processed redelivery only creates the shop groups still missing.
func TestCreateFromOrder_PartialRedeliverySkipsExistingGroups(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	first, err := st.CreateFromOrder(ctx, "order-partial",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: usd(1000)}}, "e:partial:1")
	if err != nil || len(first) != 1 {
		t.Fatalf("seed shop-a: %v %+v", err, first)
	}

	second, err := st.CreateFromOrder(ctx, "order-partial", []store.ShopAmount{
		{ShopID: "shop-a", Amount: usd(1000)},
		{ShopID: "shop-b", Amount: usd(2000)},
	}, "e:partial:2")
	if err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	if len(second) != 1 || second[0].ShopID != "shop-b" {
		t.Fatalf("want only the new shop-b payout, got %+v", second)
	}

	all, _, err := st.List(ctx, "", "", 10, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("List = %v, n=%d, want 2 total payouts", err, len(all))
	}
}

func TestGet_ShopScopingAndNotFound(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	created, _ := st.CreateFromOrder(ctx, "order-2",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: usd(1000)}}, "e:0:2")
	p := created[0]

	if got, err := st.Get(ctx, p.ID, "shop-a"); err != nil || got.ID != p.ID {
		t.Fatalf("scoped get: %v %+v", err, got)
	}
	if _, err := st.Get(ctx, p.ID, "shop-other"); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("cross-shop get: want NotFound, got %v", err)
	}
	if _, err := st.Get(ctx, "00000000-0000-0000-0000-000000000000", ""); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("missing id: want NotFound, got %v", err)
	}
}

func TestMarkPaid_IdempotentAndEmitsEvent(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	created, _ := st.CreateFromOrder(ctx, "order-3",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: usd(1000)}}, "e:0:3")
	p := created[0]

	paid, err := st.MarkPaid(ctx, p.ID)
	if err != nil || paid.Status != domain.StatusPaid || paid.PaidAt == nil {
		t.Fatalf("MarkPaid: %v %+v", err, paid)
	}
	// Idempotent: a second call is a no-op, no second event.
	if _, err := st.MarkPaid(ctx, p.ID); err != nil {
		t.Fatalf("idempotent MarkPaid: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.payout.paid'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 payout_paid event, got %d", n)
	}

	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE topic='commerce.payout.paid'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var e payoutv1.PayoutPaid
	if err := proto.Unmarshal(payload, &e); err != nil || e.GetShopId() != "shop-a" {
		t.Fatalf("payout.paid payload: %v %+v", err, &e)
	}
}

func TestDuePayoutIDs(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	created, _ := st.CreateFromOrder(ctx, "order-4",
		[]store.ShopAmount{{ShopID: "shop-a", Amount: usd(1000)}}, "e:0:4")
	p := created[0]

	due, err := st.DuePayoutIDs(ctx, time.Hour, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("nothing should be due yet: %v %v", err, due)
	}
	due, err = st.DuePayoutIDs(ctx, 0, 10)
	if err != nil || len(due) != 1 || due[0] != p.ID {
		t.Fatalf("with a zero threshold the payout should be due: %v %v", err, due)
	}
}

func TestListPaginationAndStatusFilter(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	// Distinct order ids, same shop — the realistic case (one payout per
	// order per shop; UNIQUE(order_id, shop_id) would collapse same-order
	// repeats).
	for i := 0; i < 3; i++ {
		if _, err := st.CreateFromOrder(ctx, "pg-order-"+string(rune('a'+i)),
			[]store.ShopAmount{{ShopID: "shop-pager", Amount: usd(1000)}}, ""); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	page1, next, err := st.List(ctx, "shop-pager", "", 2, "")
	if err != nil || len(page1) != 2 || next == "" {
		t.Fatalf("page 1: n=%d next=%q err=%v", len(page1), next, err)
	}
	page2, next2, err := st.List(ctx, "shop-pager", "", 2, next)
	if err != nil || len(page2) != 1 || next2 != "" {
		t.Fatalf("page 2: n=%d next=%q err=%v", len(page2), next2, err)
	}

	if _, err := st.MarkPaid(ctx, page2[0].ID); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	paid, _, err := st.List(ctx, "shop-pager", "PAID", 10, "")
	if err != nil || len(paid) != 1 {
		t.Fatalf("status filter: %v n=%d", err, len(paid))
	}
	pending, _, _ := st.List(ctx, "shop-pager", "PENDING", 10, "")
	if len(pending) != 2 {
		t.Fatalf("PENDING filter = %d, want 2", len(pending))
	}
	// A different shop's list is empty.
	other, _, _ := st.List(ctx, "shop-other", "", 10, "")
	if len(other) != 0 {
		t.Fatalf("cross-shop list leaked: %+v", other)
	}
}
