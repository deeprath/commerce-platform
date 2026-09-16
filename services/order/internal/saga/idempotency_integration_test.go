package saga_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/order/internal/saga"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

func idempotentInput(key string) saga.CreateInput {
	in := validInput()
	in.IdempotencyKey = key
	return in
}

// The bug this closes: without a key, a retried checkout placed a second order,
// reserved stock again and authorised the card again.
func TestCreateOrder_RetryWithTheSameKeyReplaysTheFirstOrder(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	cart := &fakeCart{cart: cartWithOneItem()}
	inv := &fakeInventoryFull{}
	pay := &fakePaymentFull{clientSecr: "secret-1"}
	orch := saga.New(st, saga.Clients{Cart: cart, Pricing: &fakePricing{quote: quote()}, Inventory: inv, Payment: pay})

	key := uuid.NewString()
	first, err := orch.CreateOrder(ctx, idempotentInput(key))
	if err != nil {
		t.Fatalf("first checkout: %v", err)
	}
	if first.Replayed {
		t.Fatal("the first checkout reported itself as a replay")
	}

	// The cart is refilled, so the accidental CART_EMPTY protection cannot be
	// what makes this pass — the key has to be doing the work.
	cart.cart = cartWithOneItem()
	cart.cleared = false

	second, err := orch.CreateOrder(ctx, idempotentInput(key))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !second.Replayed {
		t.Fatal("the retry was not reported as a replay")
	}
	if second.Order.ID != first.Order.ID {
		t.Fatalf("retry produced order %s, want the original %s", second.Order.ID, first.Order.ID)
	}

	// And nothing downstream ran a second time — this is the part that costs
	// money when it goes wrong.
	if len(pay.payments) != 1 {
		t.Fatalf("payment authorised %d times, want once", len(pay.payments))
	}
	if len(inv.reserves) != 1 {
		t.Fatalf("stock reserved %d times, want once", len(inv.reserves))
	}
}

// Two submits racing each other — the double-click case the cart clear never
// protected against, because both read a full cart before either cleared it.
func TestCreateOrder_ConcurrentSubmitsPlaceOneOrder(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	inv := &fakeInventoryFull{}
	pay := &fakePaymentFull{clientSecr: "secret-1"}
	orch := saga.New(st, saga.Clients{
		Cart: &fakeCart{cart: cartWithOneItem()}, Pricing: &fakePricing{quote: quote()},
		Inventory: inv, Payment: pay,
	})

	key := uuid.NewString()
	const racers = 8
	var (
		start     = make(chan struct{})
		wg        sync.WaitGroup
		mu        sync.Mutex
		orders    = map[string]bool{}
		succeeded int // includes replays, which legitimately return the same order
		busy      int
		others    []error
	)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start
			res, err := orch.CreateOrder(ctx, idempotentInput(key))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				orders[res.Order.ID] = true
				succeeded++
			case errs.Is(err, errs.KindConflict):
				busy++ // another attempt holds the claim; the client retries
			default:
				others = append(others, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(others) > 0 {
		t.Fatalf("unexpected errors: %v", others)
	}
	if len(orders) != 1 {
		t.Fatalf("%d distinct orders from %d concurrent submits, want exactly 1", len(orders), racers)
	}
	if len(pay.payments) != 1 {
		t.Fatalf("payment authorised %d times across concurrent submits, want once", len(pay.payments))
	}
	if len(inv.reserves) != 1 {
		t.Fatalf("stock reserved %d times across concurrent submits, want once", len(inv.reserves))
	}
	// Every attempt either placed the order, replayed it, or was told another
	// attempt held the claim. None may fail for any other reason.
	if succeeded+busy != racers {
		t.Fatalf("accounted for %d of %d attempts (%d succeeded, %d busy)", succeeded+busy, racers, succeeded, busy)
	}
}

// A failed checkout must free its key, or the shopper is locked out of retrying
// the thing that just failed them.
func TestCreateOrder_FailureReleasesTheKey(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	inv := &fakeInventoryFull{reserveErr: errs.New(errs.KindUnavailable, "WAREHOUSE_DOWN", "down")}
	pay := &fakePaymentFull{}
	orch := saga.New(st, saga.Clients{
		Cart: &fakeCart{cart: cartWithOneItem()}, Pricing: &fakePricing{quote: quote()},
		Inventory: inv, Payment: pay,
	})

	key := uuid.NewString()
	if _, err := orch.CreateOrder(ctx, idempotentInput(key)); err == nil {
		t.Fatal("checkout succeeded despite inventory being down")
	}

	// Inventory recovers; the same key must now be usable rather than reporting
	// a checkout still in progress.
	inv.reserveErr = nil
	res, err := orch.CreateOrder(ctx, idempotentInput(key))
	if err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if res.Replayed {
		t.Fatal("the retry replayed a failed attempt instead of running a real checkout")
	}
}

// Reusing a key for a different cart is a client bug, and must be rejected
// rather than answered with the earlier order.
func TestCreateOrder_RejectsAKeyReusedForADifferentCheckout(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	orch := saga.New(st, saga.Clients{
		Cart: &fakeCart{cart: cartWithOneItem()}, Pricing: &fakePricing{quote: quote()},
		Inventory: &fakeInventoryFull{}, Payment: &fakePaymentFull{},
	})

	key := uuid.NewString()
	if _, err := orch.CreateOrder(ctx, idempotentInput(key)); err != nil {
		t.Fatalf("first checkout: %v", err)
	}

	different := idempotentInput(key)
	different.CartID = "a-different-cart"
	_, err := orch.CreateOrder(ctx, different)
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("err = %v, want the reused key rejected", err)
	}
}

// Without a key the old behaviour is untouched, so existing clients keep working
// through the rollout.
func TestCreateOrder_NoKeyStillWorks(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	orch := saga.New(st, saga.Clients{
		Cart: &fakeCart{cart: cartWithOneItem()}, Pricing: &fakePricing{quote: quote()},
		Inventory: &fakeInventoryFull{}, Payment: &fakePaymentFull{},
	})

	res, err := orch.CreateOrder(ctx, validInput()) // no IdempotencyKey
	if err != nil {
		t.Fatalf("CreateOrder without a key: %v", err)
	}
	if res.Order.ID == "" || res.Replayed {
		t.Fatalf("unexpected result: %+v", res)
	}
}
