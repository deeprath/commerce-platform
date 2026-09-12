package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/proto"

	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/inventory/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("inventory"),
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

// seed puts `qty` on-hand for one product and returns the store.
func seed(t *testing.T, pool *pgxpool.Pool, productID string, qty int) *store.Store {
	t.Helper()
	st := store.New(pool)
	if _, err := st.AdjustStock(context.Background(), productID, qty); err != nil {
		t.Fatalf("seed %s=%d: %v", productID, qty, err)
	}
	return st
}

func level(t *testing.T, st *store.Store, productID string) store.Level {
	t.Helper()
	m, err := st.Levels(context.Background(), []string{productID})
	if err != nil {
		t.Fatalf("levels: %v", err)
	}
	return m[productID]
}

// outboxPayloads returns the raw payloads for a topic, oldest first.
func outboxPayloads(t *testing.T, pool *pgxpool.Pool, topic string) [][]byte {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT payload FROM outbox WHERE topic = $1 ORDER BY id`, topic)
	if err != nil {
		t.Fatalf("outbox query: %v", err)
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, b)
	}
	return out
}

func TestReserve_HoldsStockAndWritesStockChanged(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := seed(t, pool, "p1", 10)

	resID, expires, err := st.Reserve(ctx, "order-1", []store.Line{{ProductID: "p1", Quantity: 3}}, 15*time.Minute)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if resID == "" {
		t.Fatal("empty reservation id")
	}
	if time.Until(expires) < 10*time.Minute {
		t.Fatalf("expires_at too soon: %v", expires)
	}

	if l := level(t, st, "p1"); l.OnHand != 10 || l.Reserved != 3 || l.Available() != 7 {
		t.Fatalf("after reserve: %+v, want on_hand=10 reserved=3 available=7", l)
	}

	// One stock_changed from the seed AdjustStock, one from the reserve.
	got := outboxPayloads(t, pool, "commerce.inventory.stock_changed")
	if len(got) != 2 {
		t.Fatalf("stock_changed rows = %d, want 2", len(got))
	}
	var last inventoryv1.StockChanged
	if err := proto.Unmarshal(got[len(got)-1], &last); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if last.GetProductId() != "p1" || last.GetReserved() != 3 || last.GetAvailable() != 7 {
		t.Fatalf("last stock_changed = %+v", &last)
	}
}

func TestReserve_InsufficientStockIsFailedPrecondition(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := seed(t, pool, "p1", 2)

	_, _, err := st.Reserve(ctx, "order-1", []store.Line{{ProductID: "p1", Quantity: 5}}, time.Minute)
	assertReason(t, err, errs.KindFailedPrecondition, "INSUFFICIENT_STOCK")

	// Unknown product (no stock row) is the same failure, not a panic.
	_, _, err = st.Reserve(ctx, "order-2", []store.Line{{ProductID: "ghost", Quantity: 1}}, time.Minute)
	assertReason(t, err, errs.KindFailedPrecondition, "INSUFFICIENT_STOCK")

	// Nothing was held.
	if l := level(t, st, "p1"); l.Reserved != 0 {
		t.Fatalf("reserved = %d after failed reserves, want 0", l.Reserved)
	}
}

func TestCommit_DecrementsOnHandAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := seed(t, pool, "p1", 10)

	resID, _, err := st.Reserve(ctx, "order-1", []store.Line{{ProductID: "p1", Quantity: 4}}, time.Minute)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := st.Commit(ctx, resID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if l := level(t, st, "p1"); l.OnHand != 6 || l.Reserved != 0 {
		t.Fatalf("after commit: %+v, want on_hand=6 reserved=0", l)
	}

	// Second commit and a late release are both no-ops.
	if err := st.Commit(ctx, resID); err != nil {
		t.Fatalf("commit again: %v", err)
	}
	if err := st.Release(ctx, resID); err != nil {
		t.Fatalf("release after commit: %v", err)
	}
	if l := level(t, st, "p1"); l.OnHand != 6 || l.Reserved != 0 {
		t.Fatalf("after idempotent calls: %+v, want on_hand=6 reserved=0", l)
	}
}

func TestRelease_ReturnsHeldStockWithoutTouchingOnHand(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := seed(t, pool, "p1", 10)

	resID, _, err := st.Reserve(ctx, "order-1", []store.Line{{ProductID: "p1", Quantity: 4}}, time.Minute)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := st.Release(ctx, resID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if l := level(t, st, "p1"); l.OnHand != 10 || l.Reserved != 0 {
		t.Fatalf("after release: %+v, want on_hand=10 reserved=0", l)
	}
	// Commit after release is a no-op (already terminal).
	if err := st.Commit(ctx, resID); err != nil {
		t.Fatalf("commit after release: %v", err)
	}
	if l := level(t, st, "p1"); l.OnHand != 10 {
		t.Fatalf("on_hand moved after commit-post-release: %+v", l)
	}
}

func TestCommitAndRelease_UnknownOrMalformedIDIsANoOp(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := seed(t, pool, "p1", 10)

	// A well-formed but nonexistent UUID: falls through to the "no rows"
	// branch of reservationIsHeld.
	if err := st.Commit(ctx, "00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatalf("commit on an unknown (but valid-shaped) id: %v", err)
	}
	if err := st.Release(ctx, "00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatalf("release on an unknown (but valid-shaped) id: %v", err)
	}

	// A syntactically invalid id: reservations.id is a uuid column, so this
	// must be rejected before the query (as the same "not held" no-op), not
	// leak a raw Postgres type-mismatch error.
	if err := st.Commit(ctx, "not-a-uuid-at-all"); err != nil {
		t.Fatalf("commit on a malformed id should be a no-op, not an error: %v", err)
	}
	if err := st.Release(ctx, "not-a-uuid-at-all"); err != nil {
		t.Fatalf("release on a malformed id should be a no-op, not an error: %v", err)
	}

	// Neither call touched p1's stock.
	if l := level(t, st, "p1"); l.OnHand != 10 || l.Reserved != 0 {
		t.Fatalf("stock changed by a no-op call: %+v", l)
	}
}

func TestExpireDue_SweepsExpiredHoldsEmitsEventLeavesLiveHolds(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := seed(t, pool, "p1", 10)

	// One already-expired hold (negative TTL) and one that is still live.
	expiredID, _, err := st.Reserve(ctx, "order-expired",
		[]store.Line{{ProductID: "p1", Quantity: 3}}, -time.Minute)
	if err != nil {
		t.Fatalf("reserve expired: %v", err)
	}
	liveID, _, err := st.Reserve(ctx, "order-live",
		[]store.Line{{ProductID: "p1", Quantity: 2}}, time.Hour)
	if err != nil {
		t.Fatalf("reserve live: %v", err)
	}
	if l := level(t, st, "p1"); l.Reserved != 5 {
		t.Fatalf("reserved = %d before sweep, want 5", l.Reserved)
	}

	n, err := st.ExpireDue(ctx, 100)
	if err != nil {
		t.Fatalf("expire due: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}

	// Expired hold released, live hold untouched.
	if l := level(t, st, "p1"); l.OnHand != 10 || l.Reserved != 2 {
		t.Fatalf("after sweep: %+v, want on_hand=10 reserved=2", l)
	}
	assertStatus(t, pool, expiredID, "RELEASED")
	assertStatus(t, pool, liveID, "HELD")

	// Exactly one reservation_expired event, for the expired order and its lines.
	evs := outboxPayloads(t, pool, "commerce.inventory.reservation_expired")
	if len(evs) != 1 {
		t.Fatalf("reservation_expired rows = %d, want 1", len(evs))
	}
	var ev inventoryv1.ReservationExpired
	if err := proto.Unmarshal(evs[0], &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.GetOrderRef() != "order-expired" || ev.GetReservationId() != expiredID {
		t.Fatalf("event = %+v", &ev)
	}
	if len(ev.GetItems()) != 1 || ev.GetItems()[0].GetProductId() != "p1" || ev.GetItems()[0].GetQuantity() != 3 {
		t.Fatalf("event items = %+v", ev.GetItems())
	}

	// Re-running the sweep now finds nothing due.
	if n, err := st.ExpireDue(ctx, 100); err != nil || n != 0 {
		t.Fatalf("second sweep: n=%d err=%v, want 0, nil", n, err)
	}
}

func TestExpireDue_RespectsLimit(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := seed(t, pool, "p1", 100)

	for i := 0; i < 3; i++ {
		if _, _, err := st.Reserve(ctx, "order", []store.Line{{ProductID: "p1", Quantity: 1}}, -time.Minute); err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}

	first, err := st.ExpireDue(ctx, 2)
	if err != nil || first != 2 {
		t.Fatalf("first sweep: n=%d err=%v, want 2", first, err)
	}
	second, err := st.ExpireDue(ctx, 2)
	if err != nil || second != 1 {
		t.Fatalf("second sweep: n=%d err=%v, want 1", second, err)
	}
	if l := level(t, st, "p1"); l.Reserved != 0 {
		t.Fatalf("reserved = %d after full sweep, want 0", l.Reserved)
	}
}

func TestAdjustStock_UpsertsAndClampsAtZero(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	if l, err := st.AdjustStock(ctx, "p1", 5); err != nil || l.OnHand != 5 {
		t.Fatalf("first adjust: %+v err=%v, want on_hand=5", l, err)
	}
	if l, err := st.AdjustStock(ctx, "p1", 3); err != nil || l.OnHand != 8 {
		t.Fatalf("increment: %+v err=%v, want on_hand=8", l, err)
	}
	// Over-decrement clamps to zero rather than violating the CHECK (on_hand >= 0).
	if l, err := st.AdjustStock(ctx, "p1", -100); err != nil || l.OnHand != 0 {
		t.Fatalf("over-decrement: %+v err=%v, want on_hand=0", l, err)
	}
	if got := len(outboxPayloads(t, pool, "commerce.inventory.stock_changed")); got != 3 {
		t.Fatalf("stock_changed rows = %d, want 3", got)
	}
}

func assertReason(t *testing.T, err error, kind errs.Kind, reason string) {
	t.Helper()
	if !errs.Is(err, kind) {
		t.Fatalf("error kind: got %v, want %v", err, kind)
	}
	var e *errs.Error
	if !errors.As(err, &e) || e.Reason != reason {
		t.Fatalf("error reason: got %v, want %s", err, reason)
	}
}

func assertStatus(t *testing.T, pool *pgxpool.Pool, resID, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM reservations WHERE id = $1`, resID).Scan(&got); err != nil {
		t.Fatalf("status query: %v", err)
	}
	if got != want {
		t.Fatalf("reservation %s status = %s, want %s", resID, got, want)
	}
}
