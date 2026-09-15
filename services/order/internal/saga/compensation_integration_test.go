package saga_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/saga"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

// compInventory / compPayment fail on demand, so a compensation can be made to
// fail the way a real outage would.
type compInventory struct {
	inventoryv1.InventoryServiceClient
	releases []string
	fail     bool
}

func (f *compInventory) Release(_ context.Context, in *inventoryv1.ReleaseRequest, _ ...grpc.CallOption) (*inventoryv1.ReleaseResponse, error) {
	if f.fail {
		return nil, errs.New(errs.KindUnavailable, "WAREHOUSE_DOWN", "warehouse down")
	}
	f.releases = append(f.releases, in.GetReservationId())
	return &inventoryv1.ReleaseResponse{}, nil
}

type compPayment struct {
	paymentv1.PaymentServiceClient
	voids []string
	fail  bool
}

func (f *compPayment) Void(_ context.Context, in *paymentv1.VoidRequest, _ ...grpc.CallOption) (*paymentv1.Payment, error) {
	if f.fail {
		return nil, errs.New(errs.KindUnavailable, "PSP_DOWN", "psp down")
	}
	f.voids = append(f.voids, in.GetPaymentId())
	return &paymentv1.Payment{PaymentId: in.GetPaymentId()}, nil
}

func seedCancellable(t *testing.T, st *store.Store) *domain.Order {
	t.Helper()
	m := func(c int64) domain.Money { return domain.Money{Currency: "USD", Cents: c} }
	o := &domain.Order{
		ID: uuid.NewString(), OwnerID: "owner-1", Status: domain.StatusPendingPayment,
		Lines:    []domain.Line{{ProductID: "p1", Title: "Lamp", Quantity: 1, UnitPrice: m(3499), LineTotal: m(3499)}},
		Subtotal: m(3499), Total: m(3499),
		PaymentID: "pay-" + uuid.NewString(), ReservationID: "res-" + uuid.NewString(),
	}
	if err := st.Insert(context.Background(), o); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return o
}

// A compensation that succeeds is recorded, so the reconciler leaves it alone.
func TestCompensation_SuccessIsRecordedAndNotSweptAgain(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	o := seedCancellable(t, st)

	inv, pay := &compInventory{}, &compPayment{}
	orch := saga.New(st, saga.Clients{Inventory: inv, Payment: pay})

	if err := orch.OnPaymentFailed(ctx, "evt-1", o.ID, "card_declined"); err != nil {
		t.Fatalf("OnPaymentFailed: %v", err)
	}
	if len(inv.releases) != 1 {
		t.Fatalf("Release not attempted: %+v", inv.releases)
	}

	pending, err := st.PendingCompensations(ctx, 0, 100)
	if err != nil {
		t.Fatalf("PendingCompensations: %v", err)
	}
	for _, p := range pending {
		if p.OrderID == o.ID && p.ReservationID != "" {
			t.Fatal("a released reservation is still listed as outstanding")
		}
	}
}

// The point of the change: a compensation that fails is left discoverable
// instead of vanishing into a log line.
func TestCompensation_FailureIsLeftForTheReconciler(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	o := seedCancellable(t, st)

	inv := &compInventory{fail: true} // warehouse down during compensation
	orch := saga.New(st, saga.Clients{Inventory: inv, Payment: &compPayment{}})

	// Cancellation still succeeds — compensation failing must not undo it.
	if err := orch.OnPaymentFailed(ctx, "evt-1", o.ID, "card_declined"); err != nil {
		t.Fatalf("OnPaymentFailed: %v", err)
	}
	got, err := st.Get(ctx, o.ID, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != domain.StatusCancelled {
		t.Fatalf("order status = %s, want CANCELLED despite the failed compensation", got.Status)
	}

	pending, err := st.PendingCompensations(ctx, 0, 100)
	if err != nil {
		t.Fatalf("PendingCompensations: %v", err)
	}
	var found bool
	for _, p := range pending {
		if p.OrderID == o.ID && p.ReservationID == o.ReservationID {
			found = true
		}
	}
	if !found {
		t.Fatal("a failed Release left no trace — this is exactly what used to be lost")
	}

	// And retrying through the same path, once inventory is back, clears it.
	inv.fail = false
	if err := orch.ReleaseReservation(ctx, o.ID, o.ReservationID); err != nil {
		t.Fatalf("retry Release: %v", err)
	}
	pending, err = st.PendingCompensations(ctx, 0, 100)
	if err != nil {
		t.Fatalf("PendingCompensations: %v", err)
	}
	for _, p := range pending {
		if p.OrderID == o.ID && p.ReservationID != "" {
			t.Fatal("the retry did not clear the outstanding release")
		}
	}
}

// A failed Void is the serious one: it leaves the shopper's authorisation live.
func TestCompensation_AFailedVoidStaysOutstanding(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	o := seedCancellable(t, st)

	pay := &compPayment{fail: true}
	orch := saga.New(st, saga.Clients{Inventory: &compInventory{}, Payment: pay})
	orch.CompensateCancelled(ctx, mustCancel(t, st, o.ID))

	pending, err := st.PendingCompensations(ctx, 0, 100)
	if err != nil {
		t.Fatalf("PendingCompensations: %v", err)
	}
	var found bool
	for _, p := range pending {
		if p.OrderID == o.ID && p.PaymentID == o.PaymentID {
			found = true
		}
	}
	if !found {
		t.Fatal("a live authorisation was not recorded as outstanding")
	}
}

func mustCancel(t *testing.T, st *store.Store, id string) *domain.Order {
	t.Helper()
	o, err := st.Apply(context.Background(), id, "cancel-evt", func(o *domain.Order) error {
		return o.Cancel("CUSTOMER_CANCELLED")
	})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	return o
}
