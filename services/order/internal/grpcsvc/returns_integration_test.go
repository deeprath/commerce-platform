package grpcsvc_test

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
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/grpcsvc"
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

type fakePayment struct {
	paymentv1.PaymentServiceClient
}

func (fakePayment) Refund(_ context.Context, in *paymentv1.RefundRequest, _ ...grpc.CallOption) (*paymentv1.Payment, error) {
	return &paymentv1.Payment{PaymentId: in.GetPaymentId()}, nil
}

type fakeInventory struct {
	inventoryv1.InventoryServiceClient
}

func (fakeInventory) AdjustStock(_ context.Context, _ *inventoryv1.AdjustStockRequest, _ ...grpc.CallOption) (*inventoryv1.StockLevel, error) {
	return &inventoryv1.StockLevel{}, nil
}

func customer(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer"}})
}

func manager(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer", "order_manager"}})
}

func seedFulfilled(t *testing.T, st *store.Store, owner string) *domain.Order {
	t.Helper()
	ctx := context.Background()
	m := func(c int64) domain.Money { return domain.Money{Currency: "USD", Cents: c} }
	o := &domain.Order{
		ID: uuid.NewString(), OwnerID: owner, Status: domain.StatusPendingPayment,
		Lines:    []domain.Line{{ProductID: "p1", Title: "Lamp", Quantity: 2, UnitPrice: m(3499), LineTotal: m(6998)}},
		Subtotal: m(6998), Total: m(7558), Tax: m(560), PaymentID: "pay-1",
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

func newServer(st *store.Store) *grpcsvc.Server {
	return grpcsvc.New(saga.New(st, saga.Clients{Payment: fakePayment{}, Inventory: fakeInventory{}}), st)
}

func TestReturnRPCs_EndToEnd(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	s := newServer(st)
	o := seedFulfilled(t, st, "owner-1")

	// Anonymous can't request.
	if _, err := s.RequestReturn(ctx, &orderv1.RequestReturnRequest{OrderId: o.ID}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("anon request: want Unauthenticated, got %v", err)
	}

	r, err := s.RequestReturn(customer("owner-1"), &orderv1.RequestReturnRequest{
		OrderId: o.ID, Reason: "defective",
		Lines: []*orderv1.RequestReturnRequest_Line{{ProductId: "p1", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.GetStatus() != orderv1.ReturnStatus_RETURN_STATUS_REQUESTED || len(r.GetLines()) != 1 || r.GetRefundTotal().GetUnits() == 0 {
		t.Fatalf("bad return proto: %+v", r)
	}

	// Owner can Get; a stranger cannot; an operator can.
	if _, err := s.GetReturn(customer("owner-1"), &orderv1.GetReturnRequest{Id: r.GetId()}); err != nil {
		t.Fatalf("owner get: %v", err)
	}
	if _, err := s.GetReturn(customer("stranger"), &orderv1.GetReturnRequest{Id: r.GetId()}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("stranger get: want NotFound, got %v", err)
	}
	if _, err := s.GetReturn(manager("op"), &orderv1.GetReturnRequest{Id: r.GetId()}); err != nil {
		t.Fatalf("operator get: %v", err)
	}

	// List is owner-scoped.
	list, err := s.ListReturns(customer("owner-1"), &orderv1.ListReturnsRequest{})
	if err != nil || len(list.GetReturns()) != 1 {
		t.Fatalf("list: %v n=%d", err, len(list.GetReturns()))
	}
	empty, _ := s.ListReturns(customer("stranger"), &orderv1.ListReturnsRequest{})
	if len(empty.GetReturns()) != 0 {
		t.Fatalf("stranger list should be empty")
	}

	// DecideReturn is role-gated.
	if _, err := s.DecideReturn(customer("owner-1"), &orderv1.DecideReturnRequest{Id: r.GetId(), Approve: true}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("customer decide: want PermissionDenied, got %v", err)
	}
	decided, err := s.DecideReturn(manager("op"), &orderv1.DecideReturnRequest{Id: r.GetId(), Approve: true, Note: "ok"})
	if err != nil {
		t.Fatalf("operator decide: %v", err)
	}
	if decided.GetStatus() != orderv1.ReturnStatus_RETURN_STATUS_APPROVED || decided.GetDecidedBy() != "op" {
		t.Fatalf("bad decided proto: %+v", decided)
	}
}

func TestRequestReturn_Rejections(t *testing.T) {
	st := store.New(spinUp(t))
	s := newServer(st)

	// Missing order.
	if _, err := s.RequestReturn(customer("owner-1"), &orderv1.RequestReturnRequest{OrderId: uuid.NewString()}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("missing order: want NotFound, got %v", err)
	}

	// Not fulfilled.
	m := func(c int64) domain.Money { return domain.Money{Currency: "USD", Cents: c} }
	pend := &domain.Order{
		ID: uuid.NewString(), OwnerID: "owner-1", Status: domain.StatusPendingPayment,
		Lines:    []domain.Line{{ProductID: "p1", Quantity: 1, UnitPrice: m(1000), LineTotal: m(1000)}},
		Subtotal: m(1000), Total: m(1080), Tax: m(80),
	}
	if err := st.Insert(context.Background(), pend); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.RequestReturn(customer("owner-1"), &orderv1.RequestReturnRequest{OrderId: pend.ID}); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("pending order: want FailedPrecondition, got %v", err)
	}
}
