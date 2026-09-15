package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

func TestPendingCompensations_FindsOnlyWhatIsStillOwed(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	owed := seedPending(t, st)         // cancelled, nothing compensated
	releasedOnly := seedPending(t, st) // reservation done, payment outstanding
	bothDone := seedPending(t, st)     // fully compensated
	stillPending := seedPending(t, st) // never cancelled

	for _, o := range []*domain.Order{owed, releasedOnly, bothDone} {
		if _, err := pool.Exec(ctx,
			`UPDATE orders SET status = 'CANCELLED', updated_at = now() - interval '1 hour' WHERE id = $1`,
			o.ID); err != nil {
			t.Fatalf("cancel %s: %v", o.ID, err)
		}
	}
	if err := st.MarkReservationReleased(ctx, releasedOnly.ID); err != nil {
		t.Fatalf("mark released: %v", err)
	}
	if err := st.MarkReservationReleased(ctx, bothDone.ID); err != nil {
		t.Fatalf("mark released: %v", err)
	}
	if err := st.MarkPaymentVoided(ctx, bothDone.ID); err != nil {
		t.Fatalf("mark voided: %v", err)
	}

	got, err := st.PendingCompensations(ctx, time.Minute, 100)
	if err != nil {
		t.Fatalf("PendingCompensations: %v", err)
	}

	byID := map[string]store.PendingCompensation{}
	for _, p := range got {
		byID[p.OrderID] = p
	}
	if len(got) != 2 {
		t.Fatalf("found %d orders, want 2: %+v", len(got), got)
	}
	if _, ok := byID[bothDone.ID]; ok {
		t.Fatal("a fully compensated order was returned")
	}
	if _, ok := byID[stillPending.ID]; ok {
		t.Fatal("an order that was never cancelled was returned")
	}

	// The owed order needs both; only the outstanding halves come back set.
	if p := byID[owed.ID]; p.ReservationID == "" || p.PaymentID == "" {
		t.Fatalf("owed order = %+v, want both ids populated", p)
	}
	if p := byID[releasedOnly.ID]; p.ReservationID != "" || p.PaymentID == "" {
		t.Fatalf("partly compensated order = %+v, want only the payment outstanding", p)
	}
}

// Compensation runs inline on cancellation, so a just-cancelled order must not
// be swept — the sweep would race the attempt already in flight.
func TestPendingCompensations_IgnoresRecentlyCancelledOrders(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	o := seedPending(t, st)
	if _, err := pool.Exec(ctx,
		`UPDATE orders SET status = 'CANCELLED', updated_at = now() WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}

	got, err := st.PendingCompensations(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("PendingCompensations: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("swept an order cancelled moments ago: %+v", got)
	}
}

// An order cancelled before it ever reserved or paid owes nothing, and must not
// sit in the result set forever.
func TestPendingCompensations_SkipsOrdersThatOweNothing(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	o := seedPending(t, st)
	if _, err := pool.Exec(ctx, `
		UPDATE orders
		   SET status = 'CANCELLED', reservation_id = '', payment_id = '',
		       updated_at = now() - interval '1 hour'
		 WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}

	got, err := st.PendingCompensations(ctx, time.Minute, 100)
	if err != nil {
		t.Fatalf("PendingCompensations: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("returned an order with nothing to compensate: %+v", got)
	}
}

func TestMarkCompensations_AreIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	o := seedPending(t, st)
	if _, err := pool.Exec(ctx,
		`UPDATE orders SET status = 'CANCELLED', updated_at = now() - interval '1 hour' WHERE id = $1`,
		o.ID); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := st.MarkReservationReleased(ctx, o.ID); err != nil {
			t.Fatalf("release mark %d: %v", i, err)
		}
		if err := st.MarkPaymentVoided(ctx, o.ID); err != nil {
			t.Fatalf("void mark %d: %v", i, err)
		}
	}

	got, err := st.PendingCompensations(ctx, time.Minute, 100)
	if err != nil {
		t.Fatalf("PendingCompensations: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("still outstanding after marking: %+v", got)
	}
}

// A malformed id must be rejected before it reaches a uuid column, the same way
// the rest of the store handles it.
func TestMarkCompensations_RejectAMalformedID(t *testing.T) {
	st := store.New(spinUp(t))
	if err := st.MarkReservationReleased(context.Background(), "not-a-uuid"); err == nil {
		t.Fatal("malformed id accepted by MarkReservationReleased")
	}
	if err := st.MarkPaymentVoided(context.Background(), "not-a-uuid"); err == nil {
		t.Fatal("malformed id accepted by MarkPaymentVoided")
	}
}
