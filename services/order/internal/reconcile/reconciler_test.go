package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

type call struct{ orderID, id string }

type fakeSaga struct {
	releases   []call
	voids      []call
	releaseErr error
	voidErr    error
}

func (f *fakeSaga) ReleaseReservation(_ context.Context, orderID, resID string) error {
	f.releases = append(f.releases, call{orderID, resID})
	return f.releaseErr
}

func (f *fakeSaga) VoidPayment(_ context.Context, orderID, payID string) error {
	f.voids = append(f.voids, call{orderID, payID})
	return f.voidErr
}

func TestNew_AppliesDefaults(t *testing.T) {
	sw := New(nil, &fakeSaga{}, 0, 0, 0)
	if sw.sweepEvery != time.Minute || sw.settledFor != 5*time.Minute || sw.batch != 100 {
		t.Fatalf("defaults = %v / %v / %d", sw.sweepEvery, sw.settledFor, sw.batch)
	}
}

// Run must return on cancellation rather than hang the errgroup on shutdown.
func TestRun_StopsOnCancel(t *testing.T) {
	sw := New(nil, &fakeSaga{}, 10*time.Millisecond, time.Minute, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- sw.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

type fakePending struct {
	rows []store.PendingCompensation
	err  error
	// captured args, to prove the sweep passes its own settings through
	settledFor time.Duration
	limit      int
}

func (f *fakePending) PendingCompensations(_ context.Context, settledFor time.Duration, limit int) ([]store.PendingCompensation, error) {
	f.settledFor, f.limit = settledFor, limit
	return f.rows, f.err
}

func TestSweep_RetriesBothCompensations(t *testing.T) {
	src := &fakePending{rows: []store.PendingCompensation{
		{OrderID: "o1", ReservationID: "r1", PaymentID: "p1"},
	}}
	saga := &fakeSaga{}
	New(src, saga, time.Minute, 7*time.Minute, 50).Sweep(context.Background())

	if len(saga.releases) != 1 || saga.releases[0] != (call{"o1", "r1"}) {
		t.Fatalf("releases = %+v", saga.releases)
	}
	if len(saga.voids) != 1 || saga.voids[0] != (call{"o1", "p1"}) {
		t.Fatalf("voids = %+v", saga.voids)
	}
	if src.settledFor != 7*time.Minute || src.limit != 50 {
		t.Fatalf("query args = %v / %d, want the sweeper's own settings", src.settledFor, src.limit)
	}
}

// Only the outstanding half is retried. An order whose reservation already
// released must not have it released again.
func TestSweep_OnlyRetriesWhatIsStillOutstanding(t *testing.T) {
	src := &fakePending{rows: []store.PendingCompensation{
		{OrderID: "o1", PaymentID: "p1"},     // reservation already done
		{OrderID: "o2", ReservationID: "r2"}, // payment already done
	}}
	saga := &fakeSaga{}
	New(src, saga, time.Minute, time.Minute, 10).Sweep(context.Background())

	if len(saga.releases) != 1 || saga.releases[0].orderID != "o2" {
		t.Fatalf("releases = %+v, want only o2", saga.releases)
	}
	if len(saga.voids) != 1 || saga.voids[0].orderID != "o1" {
		t.Fatalf("voids = %+v, want only o1", saga.voids)
	}
}

// The payment is the one holding the shopper's money: a reservation that still
// will not release must not stop it being voided.
func TestSweep_AReleaseFailureDoesNotBlockTheVoid(t *testing.T) {
	src := &fakePending{rows: []store.PendingCompensation{
		{OrderID: "o1", ReservationID: "r1", PaymentID: "p1"},
	}}
	saga := &fakeSaga{releaseErr: errors.New("inventory still down")}
	New(src, saga, time.Minute, time.Minute, 10).Sweep(context.Background())

	if len(saga.voids) != 1 {
		t.Fatal("the authorisation was left live because the reservation failed first")
	}
}

// One order's failure must not abandon the rest of the batch.
func TestSweep_ContinuesPastAFailingOrder(t *testing.T) {
	src := &fakePending{rows: []store.PendingCompensation{
		{OrderID: "o1", PaymentID: "p1"},
		{OrderID: "o2", PaymentID: "p2"},
		{OrderID: "o3", PaymentID: "p3"},
	}}
	saga := &fakeSaga{voidErr: errors.New("payment provider down")}
	New(src, saga, time.Minute, time.Minute, 10).Sweep(context.Background())

	if len(saga.voids) != 3 {
		t.Fatalf("attempted %d voids, want all 3 despite each failing", len(saga.voids))
	}
}

func TestSweep_NothingPendingDoesNothing(t *testing.T) {
	saga := &fakeSaga{}
	New(&fakePending{}, saga, time.Minute, time.Minute, 10).Sweep(context.Background())
	if len(saga.releases) != 0 || len(saga.voids) != 0 {
		t.Fatal("compensated something with nothing pending")
	}
}

func TestSweep_QueryFailureIsNotFatal(t *testing.T) {
	saga := &fakeSaga{}
	src := &fakePending{err: errors.New("db down")}
	New(src, saga, time.Minute, time.Minute, 10).Sweep(context.Background())
	if len(saga.voids) != 0 {
		t.Fatal("compensated despite not knowing what was pending")
	}
}
