package saga_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"

	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/saga"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("order"),
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

// --- fakes ---------------------------------------------------------------

type fakePayment struct {
	paymentv1.PaymentServiceClient
	refunds []*paymentv1.RefundRequest
	seen    map[string]bool // idempotency keys already applied
	fail    bool
}

func (f *fakePayment) Refund(_ context.Context, in *paymentv1.RefundRequest, _ ...grpc.CallOption) (*paymentv1.Payment, error) {
	if f.fail {
		return nil, errs.New(errs.KindUnavailable, "PSP_DOWN", "psp down")
	}
	if k := in.GetIdempotencyKey(); k != "" {
		if f.seen == nil {
			f.seen = map[string]bool{}
		}
		if f.seen[k] {
			return &paymentv1.Payment{PaymentId: in.GetPaymentId()}, nil // already applied
		}
		f.seen[k] = true
	}
	f.refunds = append(f.refunds, in)
	return &paymentv1.Payment{PaymentId: in.GetPaymentId()}, nil
}

type fakeInventory struct {
	inventoryv1.InventoryServiceClient
	adjust  []*inventoryv1.AdjustStockRequest
	failFor map[string]bool // product ids that error
}

func (f *fakeInventory) AdjustStock(_ context.Context, in *inventoryv1.AdjustStockRequest, _ ...grpc.CallOption) (*inventoryv1.StockLevel, error) {
	if f.failFor[in.GetProductId()] {
		return nil, errs.New(errs.KindUnavailable, "WAREHOUSE_DOWN", "warehouse down")
	}
	f.adjust = append(f.adjust, in)
	return &inventoryv1.StockLevel{}, nil
}

// --- helpers ------------------------------------------------------------

func seedFulfilled(t *testing.T, st *store.Store) *domain.Order {
	t.Helper()
	ctx := context.Background()
	m := func(c int64) domain.Money { return domain.Money{Currency: "USD", Cents: c} }
	o := &domain.Order{
		ID: uuid.NewString(), OwnerID: "owner-1", Status: domain.StatusPendingPayment,
		Lines: []domain.Line{
			{ProductID: "p1", Title: "Desk Lamp", Quantity: 2, UnitPrice: m(3499), LineTotal: m(6998)},
			{ProductID: "p2", Title: "Notebook", Quantity: 1, UnitPrice: m(1200), LineTotal: m(1200)},
		},
		Subtotal: m(8198), Discount: m(0), Tax: m(656), Total: m(8854),
		PaymentID: "pay-1", ReservationID: "res-1",
	}
	if err := st.Insert(ctx, o); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := st.Apply(ctx, o.ID, "c", func(o *domain.Order) error { return o.Confirm() }); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := st.Apply(ctx, o.ID, "f", func(o *domain.Order) error { return o.Fulfill() }); err != nil {
		t.Fatalf("fulfill: %v", err)
	}
	return o
}

func newOrch(st *store.Store, fp *fakePayment, fi *fakeInventory) *saga.Orchestrator {
	return saga.New(st, saga.Clients{Payment: fp, Inventory: fi})
}

// --- tests ------------------------------------------------------------------

func TestRequestReturn_FullOrderRefundAndValidation(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	orch := newOrch(st, &fakePayment{}, &fakeInventory{})
	o := seedFulfilled(t, st)

	// No lines => every line, full quantity. Refund total == order total.
	r, err := orch.RequestReturn(ctx, "owner-1", o.ID, "defective", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.Status != domain.ReturnRequested || len(r.Lines) != 2 {
		t.Fatalf("bad return: %+v", r)
	}
	if r.RefundTotal.Cents != o.Total.Cents {
		t.Fatalf("full return refund = %d, want %d", r.RefundTotal.Cents, o.Total.Cents)
	}

	// A second full return has nothing left to return.
	if _, err := orch.RequestReturn(ctx, "owner-1", o.ID, "again", nil); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("double return: want FailedPrecondition, got %v", err)
	}
}

func TestRequestReturn_PartialAndGates(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	orch := newOrch(st, &fakePayment{}, &fakeInventory{})
	o := seedFulfilled(t, st)

	// Return 1 of the 2 desk lamps: refund ~= total * (3499 / 8198).
	r, err := orch.RequestReturn(ctx, "owner-1", o.ID, "one broke",
		[]saga.ReturnLineReq{{ProductID: "p1", Quantity: 1}})
	if err != nil {
		t.Fatalf("partial: %v", err)
	}
	want := o.Total.Cents * 3499 / o.Subtotal.Cents
	if r.RefundTotal.Cents != want || len(r.Lines) != 1 || r.Lines[0].Quantity != 1 {
		t.Fatalf("partial refund = %d, want %d (%+v)", r.RefundTotal.Cents, want, r)
	}

	// Now only 1 lamp is still returnable; asking for 2 fails.
	if _, err := orch.RequestReturn(ctx, "owner-1", o.ID, "x",
		[]saga.ReturnLineReq{{ProductID: "p1", Quantity: 2}}); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("over-qty: want FailedPrecondition, got %v", err)
	}
	// A product that isn't on the order.
	if _, err := orch.RequestReturn(ctx, "owner-1", o.ID, "x",
		[]saga.ReturnLineReq{{ProductID: "nope", Quantity: 1}}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("bad product: want InvalidArgument, got %v", err)
	}
}

func TestRequestReturn_OnlyFulfilledAndOwnerScoped(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	orch := newOrch(st, &fakePayment{}, &fakeInventory{})

	// A confirmed-but-not-fulfilled order can't be returned.
	m := func(c int64) domain.Money { return domain.Money{Currency: "USD", Cents: c} }
	pend := &domain.Order{
		ID: uuid.NewString(), OwnerID: "owner-1", Status: domain.StatusPendingPayment,
		Lines:    []domain.Line{{ProductID: "p1", Quantity: 1, UnitPrice: m(1000), LineTotal: m(1000)}},
		Subtotal: m(1000), Total: m(1080), Tax: m(80),
	}
	if err := st.Insert(ctx, pend); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := orch.RequestReturn(ctx, "owner-1", pend.ID, "x", nil); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("return pending order: want FailedPrecondition, got %v", err)
	}

	// A fulfilled order is not visible to another owner.
	o := seedFulfilled(t, st)
	if _, err := orch.RequestReturn(ctx, "someone-else", o.ID, "x", nil); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("cross-owner return: want NotFound, got %v", err)
	}
}

func TestDecideReturn_ApproveRefundsAndRestocks(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	fp, fi := &fakePayment{}, &fakeInventory{}
	orch := newOrch(st, fp, fi)
	o := seedFulfilled(t, st)

	r, err := orch.RequestReturn(ctx, "owner-1", o.ID, "defective", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	decided, err := orch.DecideReturn(ctx, "op-1", r.ID, true, "approved")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decided.Status != domain.ReturnApproved {
		t.Fatalf("status = %s", decided.Status)
	}
	if len(fp.refunds) != 1 || fp.refunds[0].GetPaymentId() != "pay-1" || fp.refunds[0].GetIdempotencyKey() != r.ID {
		t.Fatalf("refund call: %+v", fp.refunds)
	}
	if len(fi.adjust) != 2 {
		t.Fatalf("restock calls = %d, want 2 (one per line)", len(fi.adjust))
	}
	for _, a := range fi.adjust {
		if a.GetDelta() <= 0 || a.GetReason() != "RETURN:"+r.ID {
			t.Fatalf("bad adjust: %+v", a)
		}
	}

	// Idempotent: deciding again does not re-refund or re-restock.
	if _, err := orch.DecideReturn(ctx, "op-1", r.ID, true, "again"); err != nil {
		t.Fatalf("re-decide: %v", err)
	}
	if len(fp.refunds) != 1 || len(fi.adjust) != 2 {
		t.Fatalf("re-decide caused side effects: refunds=%d adjust=%d", len(fp.refunds), len(fi.adjust))
	}
}

func TestDecideReturn_RestockFailurePartiallyProgressesThenRetries(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	o := seedFulfilled(t, st) // lines: p1 x2, p2 x1

	fp := &fakePayment{}
	// p2 restock fails the first time.
	fi := &fakeInventory{failFor: map[string]bool{"p2": true}}
	orch := newOrch(st, fp, fi)

	r, err := orch.RequestReturn(ctx, "owner-1", o.ID, "all", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := orch.DecideReturn(ctx, "op-1", r.ID, true, "approved"); !errs.Is(err, errs.KindUnavailable) {
		t.Fatalf("expected the restock failure to surface, got %v", err)
	}
	// p1 was restocked and marked; p2 was not.
	if len(fi.adjust) != 1 || fi.adjust[0].GetProductId() != "p1" {
		t.Fatalf("partial restock progress wrong: %+v", fi.adjust)
	}
	// Retry with a healthy warehouse: only p2 is attempted, refund not repeated.
	fi.failFor = nil
	if _, err := orch.DecideReturn(ctx, "op-1", r.ID, true, "retry"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(fi.adjust) != 2 || fi.adjust[1].GetProductId() != "p2" {
		t.Fatalf("retry did not finish p2: %+v", fi.adjust)
	}
	if len(fp.refunds) != 1 {
		t.Fatalf("refund repeated on retry: %d", len(fp.refunds))
	}
}

func TestDecideReturn_RejectAndRetryAfterRestockFailure(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	o := seedFulfilled(t, st)

	// Reject: no refund, no restock.
	fp, fi := &fakePayment{}, &fakeInventory{}
	orch := newOrch(st, fp, fi)
	r, _ := orch.RequestReturn(ctx, "owner-1", o.ID, "x", []saga.ReturnLineReq{{ProductID: "p2", Quantity: 1}})
	rej, err := orch.DecideReturn(ctx, "op-1", r.ID, false, "denied")
	if err != nil || rej.Status != domain.ReturnRejected {
		t.Fatalf("reject: %v %+v", err, rej)
	}
	if len(fp.refunds) != 0 || len(fi.adjust) != 0 {
		t.Fatalf("reject had side effects")
	}

	// Approve with a refund that fails, then retry succeeds without double-refunding.
	r2, _ := orch.RequestReturn(ctx, "owner-1", o.ID, "x", []saga.ReturnLineReq{{ProductID: "p1", Quantity: 1}})
	failing := &fakePayment{fail: true}
	orch2 := newOrch(st, failing, fi)
	if _, err := orch2.DecideReturn(ctx, "op-1", r2.ID, true, "approved"); !errs.Is(err, errs.KindUnavailable) {
		t.Fatalf("expected the refund failure to surface, got %v", err)
	}
	// The return is APPROVED (status flipped, event emitted) but not settled.
	again := &fakePayment{}
	orch3 := newOrch(st, again, fi)
	if _, err := orch3.DecideReturn(ctx, "op-1", r2.ID, true, "retry"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(again.refunds) != 1 {
		t.Fatalf("retry refund calls = %d, want 1", len(again.refunds))
	}
}
