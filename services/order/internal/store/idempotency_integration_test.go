package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

const fp = "fingerprint-1"

func TestClaimIdempotencyKey_FirstCallerWins(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	key := uuid.NewString()

	first, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !first.Claimed {
		t.Fatalf("first claim = %+v, want Claimed", first)
	}

	second, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second.Claimed {
		t.Fatal("a second caller also claimed the key — both would place an order")
	}
	if !second.InFlight {
		t.Fatalf("second claim = %+v, want InFlight", second)
	}
}

// The claim is what serialises concurrent checkouts, so it has to hold when the
// requests genuinely race rather than arriving one after another.
func TestClaimIdempotencyKey_ExactlyOneWinnerUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	key := uuid.NewString()

	const racers = 16
	var (
		start      = make(chan struct{})
		wg         sync.WaitGroup
		mu         sync.Mutex
		claimed    int
		inFlight   int
		unexpected []error
	)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start // release them all at once
			c, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				unexpected = append(unexpected, err)
			case c.Claimed:
				claimed++
			case c.InFlight:
				inFlight++
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(unexpected) > 0 {
		t.Fatalf("unexpected errors: %v", unexpected)
	}
	if claimed != 1 {
		t.Fatalf("%d of %d concurrent attempts claimed the key, want exactly 1 — the rest would each place an order", claimed, racers)
	}
	if inFlight != racers-1 {
		t.Fatalf("in-flight = %d, want %d", inFlight, racers-1)
	}
}

// A completed key replays its order rather than running checkout again.
func TestClaimIdempotencyKey_CompletedKeyReplaysTheOrder(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	key := uuid.NewString()

	if c, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp); err != nil || !c.Claimed {
		t.Fatalf("claim: %+v %v", c, err)
	}
	o := newSeedOrder("owner-1")
	if err := st.InsertWithKey(ctx, o, key); err != nil {
		t.Fatalf("InsertWithKey: %v", err)
	}

	replay, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp)
	if err != nil {
		t.Fatalf("replay claim: %v", err)
	}
	if replay.OrderID != o.ID {
		t.Fatalf("replay = %+v, want the original order %s", replay, o.ID)
	}

	got, err := st.OrderForKey(ctx, key)
	if err != nil {
		t.Fatalf("OrderForKey: %v", err)
	}
	if got.ID != o.ID {
		t.Fatalf("OrderForKey = %s, want %s", got.ID, o.ID)
	}
}

// Reusing a key for a different checkout is a client bug. Answering it with the
// first order would silently swallow the second purchase.
func TestClaimIdempotencyKey_RejectsAReusedKey(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	key := uuid.NewString()

	if _, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp); err != nil {
		t.Fatalf("claim: %v", err)
	}
	_, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", "a-different-request")
	if !errors.Is(err, store.ErrIdempotencyKeyReused) {
		t.Fatalf("err = %v, want ErrIdempotencyKeyReused", err)
	}
}

// A key belongs to the shopper who created it; another shopper presenting it
// must never be handed their order.
func TestClaimIdempotencyKey_IsScopedToItsOwner(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	key := uuid.NewString()

	if _, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp); err != nil {
		t.Fatalf("claim: %v", err)
	}
	_, err := st.ClaimIdempotencyKey(ctx, key, "owner-2", fp)
	if !errors.Is(err, store.ErrIdempotencyKeyReused) {
		t.Fatalf("err = %v, want the other owner rejected", err)
	}
}

// A failed checkout releases its claim, so the shopper can genuinely retry
// instead of being told their order is still in flight.
func TestReleaseIdempotencyKey_FreesTheKeyForARetry(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	key := uuid.NewString()

	if _, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := st.ReleaseIdempotencyKey(ctx, key); err != nil {
		t.Fatalf("release: %v", err)
	}
	again, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp)
	if err != nil || !again.Claimed {
		t.Fatalf("reclaim after release = %+v %v, want Claimed", again, err)
	}
}

// Releasing must not undo a completed checkout — that would let a retry place a
// second order.
func TestReleaseIdempotencyKey_LeavesACompletedKeyAlone(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	key := uuid.NewString()

	if _, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp); err != nil {
		t.Fatalf("claim: %v", err)
	}
	o := newSeedOrder("owner-1")
	if err := st.InsertWithKey(ctx, o, key); err != nil {
		t.Fatalf("InsertWithKey: %v", err)
	}
	if err := st.ReleaseIdempotencyKey(ctx, key); err != nil {
		t.Fatalf("release: %v", err)
	}

	c, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp)
	if err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	if c.OrderID != o.ID {
		t.Fatalf("claim = %+v, want the completed key still replaying %s", c, o.ID)
	}
}

// The order and the key close together or not at all.
func TestInsertWithKey_IsAtomicWithTheOrder(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	o := newSeedOrder("owner-1")

	// No claim exists, so the key cannot be completed — and the order must not
	// be written either.
	err := st.InsertWithKey(ctx, o, uuid.NewString())
	if err == nil {
		t.Fatal("InsertWithKey succeeded without a claim to close")
	}
	if _, gerr := st.Get(ctx, o.ID, ""); gerr == nil {
		t.Fatal("the order was committed despite its key never being closed")
	}
}

func TestReapIdempotencyKeys_RemovesOnlyExpiredKeys(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	fresh, oldDone, abandoned := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, k := range []string{fresh, oldDone, abandoned} {
		if _, err := st.ClaimIdempotencyKey(ctx, k, "owner-1", fp); err != nil {
			t.Fatalf("claim %s: %v", k, err)
		}
	}
	// oldDone completed a day ago; abandoned was claimed and never closed.
	if _, err := pool.Exec(ctx,
		`UPDATE idempotency_keys SET status='COMPLETED', created_at = now() - interval '48 hours' WHERE key=$1`,
		oldDone); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE idempotency_keys SET created_at = now() - interval '2 hours' WHERE key=$1`, abandoned); err != nil {
		t.Fatal(err)
	}

	n, err := st.ReapIdempotencyKeys(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if n != 2 {
		t.Fatalf("reaped %d, want the completed-and-old plus the abandoned one", n)
	}
	// The fresh claim survives.
	c, err := st.ClaimIdempotencyKey(ctx, fresh, "owner-1", fp)
	if err != nil {
		t.Fatalf("claim fresh: %v", err)
	}
	if !c.InFlight {
		t.Fatalf("fresh claim = %+v, want it still held", c)
	}
}

func newSeedOrder(owner string) *domain.Order {
	m := func(c int64) domain.Money { return domain.Money{Currency: "USD", Cents: c} }
	return &domain.Order{
		ID: uuid.NewString(), OwnerID: owner, Status: domain.StatusPendingPayment,
		Lines:    []domain.Line{{ProductID: "p1", Title: "Lamp", Quantity: 1, UnitPrice: m(3499), LineTotal: m(3499)}},
		Subtotal: m(3499), Total: m(3499),
		PaymentID: "pay-" + uuid.NewString(), ReservationID: "res-" + uuid.NewString(),
	}
}

// A process that dies between claiming and committing must not lock the key
// forever. After the stale window another attempt takes it over, so the shopper
// can check out instead of being told indefinitely that one is in progress.
func TestClaimIdempotencyKey_TakesOverAnAbandonedClaim(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	key := uuid.NewString()

	if _, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Still inside the window: nobody else may touch it.
	if c, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp); err != nil || !c.InFlight {
		t.Fatalf("claim inside the window = %+v %v, want InFlight", c, err)
	}

	// Age it past the window, as a crashed attempt would look.
	if _, err := pool.Exec(ctx,
		`UPDATE idempotency_keys SET created_at = now() - interval '30 minutes' WHERE key = $1`,
		key); err != nil {
		t.Fatal(err)
	}

	c, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp)
	if err != nil {
		t.Fatalf("claim after the window: %v", err)
	}
	if !c.Claimed {
		t.Fatalf("claim after the window = %+v, want it taken over", c)
	}
	// And the takeover resets the clock, so the new holder gets a full window.
	if again, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp); err != nil || !again.InFlight {
		t.Fatalf("claim after takeover = %+v %v, want InFlight", again, err)
	}
}

// A completed key is never taken over, however old it gets — that would let a
// retry place a second order for an already-placed checkout.
func TestClaimIdempotencyKey_NeverTakesOverACompletedKey(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	key := uuid.NewString()

	if _, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp); err != nil {
		t.Fatalf("claim: %v", err)
	}
	o := newSeedOrder("owner-1")
	if err := st.InsertWithKey(ctx, o, key); err != nil {
		t.Fatalf("InsertWithKey: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE idempotency_keys SET created_at = now() - interval '30 minutes' WHERE key = $1`,
		key); err != nil {
		t.Fatal(err)
	}

	c, err := st.ClaimIdempotencyKey(ctx, key, "owner-1", fp)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if c.Claimed {
		t.Fatal("an old completed key was taken over — the shopper would be charged twice")
	}
	if c.OrderID != o.ID {
		t.Fatalf("claim = %+v, want a replay of %s", c, o.ID)
	}
}

func TestOrderForKey_UnknownKeyIsNotFound(t *testing.T) {
	st := store.New(spinUp(t))
	if _, err := st.OrderForKey(context.Background(), uuid.NewString()); err == nil {
		t.Fatal("OrderForKey returned an order for a key that was never used")
	}
}
