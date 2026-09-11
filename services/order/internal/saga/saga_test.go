package saga_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	pricingv1 "github.com/deeprath/commerce-platform/gen/go/commerce/pricing/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/saga"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

// --- fakes (a saga_test.go-local set; the returns-flow fakes in
// returns_integration_test.go cover Payment/Inventory only) ----------------

type fakeCart struct {
	cartv1.CartServiceClient
	cart      *cartv1.Cart
	getErr    error
	clearErr  error
	cleared   bool
	clearCart string
}

func (f *fakeCart) GetCart(_ context.Context, in *cartv1.GetCartRequest, _ ...grpc.CallOption) (*cartv1.Cart, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.cart, nil
}

func (f *fakeCart) Clear(_ context.Context, in *cartv1.ClearRequest, _ ...grpc.CallOption) (*cartv1.Cart, error) {
	f.cleared = true
	f.clearCart = in.GetCartId()
	if f.clearErr != nil {
		return nil, f.clearErr
	}
	return &cartv1.Cart{Id: in.GetCartId()}, nil
}

type fakePricing struct {
	pricingv1.PricingServiceClient
	quote *pricingv1.Quote
	err   error
}

func (f *fakePricing) QuotePrice(_ context.Context, _ *pricingv1.QuoteRequest, _ ...grpc.CallOption) (*pricingv1.Quote, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.quote, nil
}

type fakeInventoryFull struct {
	inventoryv1.InventoryServiceClient
	reserveErr error
	commitErr  error
	reservedID string
	commits    []string
	releases   []string
}

func (f *fakeInventoryFull) Reserve(_ context.Context, in *inventoryv1.ReserveRequest, _ ...grpc.CallOption) (*inventoryv1.Reservation, error) {
	if f.reserveErr != nil {
		return nil, f.reserveErr
	}
	id := f.reservedID
	if id == "" {
		id = "res-" + in.GetOrderRef()
	}
	return &inventoryv1.Reservation{ReservationId: id}, nil
}

func (f *fakeInventoryFull) Commit(_ context.Context, in *inventoryv1.CommitRequest, _ ...grpc.CallOption) (*inventoryv1.CommitResponse, error) {
	f.commits = append(f.commits, in.GetReservationId())
	if f.commitErr != nil {
		return nil, f.commitErr
	}
	return &inventoryv1.CommitResponse{}, nil
}

func (f *fakeInventoryFull) Release(_ context.Context, in *inventoryv1.ReleaseRequest, _ ...grpc.CallOption) (*inventoryv1.ReleaseResponse, error) {
	f.releases = append(f.releases, in.GetReservationId())
	return &inventoryv1.ReleaseResponse{}, nil
}

type fakePaymentFull struct {
	paymentv1.PaymentServiceClient
	createErr  error
	paymentID  string
	clientSecr string
	voids      []string
}

func (f *fakePaymentFull) CreatePayment(_ context.Context, in *paymentv1.CreatePaymentRequest, _ ...grpc.CallOption) (*paymentv1.Payment, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	id := f.paymentID
	if id == "" {
		id = "pay-" + in.GetOrderId()
	}
	return &paymentv1.Payment{PaymentId: id, ClientSecret: f.clientSecr}, nil
}

func (f *fakePaymentFull) Void(_ context.Context, in *paymentv1.VoidRequest, _ ...grpc.CallOption) (*paymentv1.Payment, error) {
	f.voids = append(f.voids, in.GetPaymentId())
	return &paymentv1.Payment{PaymentId: in.GetPaymentId()}, nil
}

func quote() *pricingv1.Quote {
	return &pricingv1.Quote{
		Lines: []*pricingv1.QuoteLine{
			{ProductId: "p1", Title: "Trail Cap", Quantity: 2, ShopId: "shop-1",
				UnitPrice: &commonv1.Money{CurrencyCode: "USD", Units: 10}, LineTotal: &commonv1.Money{CurrencyCode: "USD", Units: 20}},
		},
		Subtotal:         &commonv1.Money{CurrencyCode: "USD", Units: 20},
		Discount:         &commonv1.Money{CurrencyCode: "USD", Units: 0},
		Tax:              &commonv1.Money{CurrencyCode: "USD", Units: 2},
		Total:            &commonv1.Money{CurrencyCode: "USD", Units: 22},
		CouponCode:       "SAVE",
		PricingSignature: "sig-1",
	}
}

func cartWithOneItem() *cartv1.Cart {
	return &cartv1.Cart{Id: "cart-1", Items: []*cartv1.CartItem{{ProductId: "p1", Quantity: 2}}}
}

func validInput() saga.CreateInput {
	return saga.CreateInput{OwnerID: "owner-1", CartID: "cart-1", Currency: "USD", MethodToken: "pm_ok"}
}

// --- CreateOrder -------------------------------------------------------

func TestCreateOrder_HappyPath(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	cart := &fakeCart{cart: cartWithOneItem()}
	inv := &fakeInventoryFull{}
	pay := &fakePaymentFull{clientSecr: "secret-1"}
	orch := saga.New(st, saga.Clients{Cart: cart, Pricing: &fakePricing{quote: quote()}, Inventory: inv, Payment: pay})

	res, err := orch.CreateOrder(ctx, validInput())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if res.Order.Status != domain.StatusPendingPayment {
		t.Fatalf("status = %s", res.Order.Status)
	}
	if res.Order.CartID != "cart-1" || res.Order.CouponCode != "SAVE" || res.Order.PricingSignature != "sig-1" {
		t.Fatalf("order mismapped: %+v", res.Order)
	}
	if len(res.Order.Lines) != 1 || res.Order.Lines[0].ShopID != "shop-1" || res.Order.Lines[0].Quantity != 2 {
		t.Fatalf("lines mismapped: %+v", res.Order.Lines)
	}
	if res.Order.Total.Cents != 2200 {
		t.Fatalf("total = %+v, want 2200 cents", res.Order.Total)
	}
	if res.PaymentSecret != "secret-1" {
		t.Fatalf("payment secret = %q", res.PaymentSecret)
	}
	if res.Order.PaymentID == "" || res.Order.ReservationID == "" {
		t.Fatalf("payment/reservation id not set: %+v", res.Order)
	}
	if !cart.cleared || cart.clearCart != "cart-1" {
		t.Fatal("cart was not cleared after a successful order")
	}

	// The order actually landed in the store.
	stored, err := st.Get(ctx, res.Order.ID, "owner-1")
	if err != nil {
		t.Fatalf("Get after CreateOrder: %v", err)
	}
	if stored.Status != domain.StatusPendingPayment {
		t.Fatalf("stored status = %s", stored.Status)
	}
}

func TestCreateOrder_ValidationAndUpstreamFailures(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	for _, tc := range []struct {
		name       string
		in         func() saga.CreateInput
		clients    func() saga.Clients
		wantKind   errs.Kind
		wantReason string
	}{
		{
			name:       "no owner id",
			in:         func() saga.CreateInput { in := validInput(); in.OwnerID = ""; return in },
			clients:    func() saga.Clients { return saga.Clients{} },
			wantKind:   errs.KindUnauthenticated,
			wantReason: "NOT_AUTHENTICATED",
		},
		{
			name:       "no cart id",
			in:         func() saga.CreateInput { in := validInput(); in.CartID = ""; return in },
			clients:    func() saga.Clients { return saga.Clients{} },
			wantKind:   errs.KindInvalidArgument,
			wantReason: "CART_ID_REQUIRED",
		},
		{
			name: "cart unavailable",
			in:   validInput,
			clients: func() saga.Clients {
				return saga.Clients{Cart: &fakeCart{getErr: errs.New(errs.KindUnavailable, "CART_DOWN", "down")}}
			},
			wantKind:   errs.KindUnavailable,
			wantReason: "CART_UNAVAILABLE",
		},
		{
			name: "empty cart",
			in:   validInput,
			clients: func() saga.Clients {
				return saga.Clients{Cart: &fakeCart{cart: &cartv1.Cart{Id: "cart-1"}}}
			},
			wantKind:   errs.KindFailedPrecondition,
			wantReason: "CART_EMPTY",
		},
		{
			name: "quote failed",
			in:   validInput,
			clients: func() saga.Clients {
				return saga.Clients{
					Cart:    &fakeCart{cart: cartWithOneItem()},
					Pricing: &fakePricing{err: errs.New(errs.KindFailedPrecondition, "COUPON_INVALID", "bad coupon")},
				}
			},
			wantKind:   errs.KindFailedPrecondition,
			wantReason: "QUOTE_FAILED",
		},
		{
			name: "reserve failed",
			in:   validInput,
			clients: func() saga.Clients {
				return saga.Clients{
					Cart:      &fakeCart{cart: cartWithOneItem()},
					Pricing:   &fakePricing{quote: quote()},
					Inventory: &fakeInventoryFull{reserveErr: errs.New(errs.KindFailedPrecondition, "OUT_OF_STOCK", "no stock")},
				}
			},
			wantKind:   errs.KindFailedPrecondition,
			wantReason: "RESERVE_FAILED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orch := saga.New(st, tc.clients())
			_, err := orch.CreateOrder(ctx, tc.in())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errs.Is(err, tc.wantKind) {
				t.Fatalf("kind = %v, want %v (err: %v)", err, tc.wantKind, err)
			}
		})
	}
}

func TestCreateOrder_PaymentInitFailureCompensatesRelease(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	inv := &fakeInventoryFull{reservedID: "res-1"}
	orch := saga.New(st, saga.Clients{
		Cart: &fakeCart{cart: cartWithOneItem()}, Pricing: &fakePricing{quote: quote()},
		Inventory: inv, Payment: &fakePaymentFull{createErr: errs.New(errs.KindUnavailable, "PSP_DOWN", "psp down")},
	})

	_, err := orch.CreateOrder(ctx, validInput())
	if !errs.Is(err, errs.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if len(inv.releases) != 1 || inv.releases[0] != "res-1" {
		t.Fatalf("releases = %v, want [res-1]", inv.releases)
	}
}

func TestCreateOrder_InsertFailureCompensatesReleaseAndVoid(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	pool.Close() // force the subsequent Insert to fail with a real DB error

	inv := &fakeInventoryFull{reservedID: "res-1"}
	pay := &fakePaymentFull{paymentID: "pay-1"}
	orch := saga.New(st, saga.Clients{
		Cart: &fakeCart{cart: cartWithOneItem()}, Pricing: &fakePricing{quote: quote()},
		Inventory: inv, Payment: pay,
	})

	_, err := orch.CreateOrder(ctx, validInput())
	if err == nil {
		t.Fatal("expected the closed-pool Insert to fail")
	}
	if len(inv.releases) != 1 || inv.releases[0] != "res-1" {
		t.Fatalf("releases = %v, want [res-1]", inv.releases)
	}
	if len(pay.voids) != 1 || pay.voids[0] != "pay-1" {
		t.Fatalf("voids = %v, want [pay-1]", pay.voids)
	}
}

func TestCreateOrder_CartClearFailureIsNonFatal(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	cart := &fakeCart{cart: cartWithOneItem(), clearErr: errs.New(errs.KindUnavailable, "CART_DOWN", "down")}
	orch := saga.New(st, saga.Clients{
		Cart: cart, Pricing: &fakePricing{quote: quote()},
		Inventory: &fakeInventoryFull{}, Payment: &fakePaymentFull{},
	})

	res, err := orch.CreateOrder(ctx, validInput())
	if err != nil {
		t.Fatalf("a cart-clear failure should not fail checkout: %v", err)
	}
	if res.Order == nil {
		t.Fatal("expected an order despite the cart-clear failure")
	}
	if !cart.cleared {
		t.Fatal("Clear was never even attempted")
	}
}

// --- event handlers ------------------------------------------------------

func seedPending(t *testing.T, st *store.Store, resID string, shopIDs ...string) *domain.Order {
	t.Helper()
	if len(shopIDs) == 0 {
		shopIDs = []string{""}
	}
	m := domain.Money{Currency: "USD", Cents: 2200}
	var lines []domain.Line
	for _, sid := range shopIDs {
		lines = append(lines, domain.Line{
			ProductID: uuid.NewString(), Title: "Trail Cap", Quantity: 1, ShopID: sid, UnitPrice: m, LineTotal: m,
		})
	}
	o := &domain.Order{
		ID: uuid.NewString(), OwnerID: "owner-1", Status: domain.StatusPendingPayment,
		Lines: lines, Subtotal: m, Total: m, Tax: domain.Money{Currency: "USD"},
		PaymentID: "pay-1", ReservationID: resID,
	}
	if err := st.Insert(context.Background(), o); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	return o
}

func TestOnPaymentAuthorized(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	t.Run("confirms and commits the reservation", func(t *testing.T) {
		o := seedPending(t, st, "res-1")
		inv := &fakeInventoryFull{}
		orch := saga.New(st, saga.Clients{Inventory: inv})

		if err := orch.OnPaymentAuthorized(ctx, "evt-"+o.ID, o.ID); err != nil {
			t.Fatalf("OnPaymentAuthorized: %v", err)
		}
		got, err := st.Get(ctx, o.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != domain.StatusConfirmed {
			t.Fatalf("status = %s", got.Status)
		}
		if len(inv.commits) != 1 || inv.commits[0] != "res-1" {
			t.Fatalf("commits = %v", inv.commits)
		}

		// Re-delivery of the same event: no-op, no second Commit.
		if err := orch.OnPaymentAuthorized(ctx, "evt-"+o.ID, o.ID); err != nil {
			t.Fatalf("re-delivery: %v", err)
		}
		if len(inv.commits) != 1 {
			t.Fatalf("commits after re-delivery = %v, want still 1", inv.commits)
		}
	})

	t.Run("a commit failure surfaces as Unavailable", func(t *testing.T) {
		o := seedPending(t, st, "res-2")
		inv := &fakeInventoryFull{commitErr: errs.New(errs.KindUnavailable, "WMS_DOWN", "down")}
		orch := saga.New(st, saga.Clients{Inventory: inv})

		err := orch.OnPaymentAuthorized(ctx, "evt-"+o.ID, o.ID)
		if !errs.Is(err, errs.KindUnavailable) {
			t.Fatalf("err = %v, want KindUnavailable", err)
		}
	})

	t.Run("no reservation id skips Commit entirely", func(t *testing.T) {
		o := seedPending(t, st, "")
		inv := &fakeInventoryFull{}
		orch := saga.New(st, saga.Clients{Inventory: inv})

		if err := orch.OnPaymentAuthorized(ctx, "evt-"+o.ID, o.ID); err != nil {
			t.Fatalf("OnPaymentAuthorized: %v", err)
		}
		if len(inv.commits) != 0 {
			t.Fatalf("commits = %v, want none", inv.commits)
		}
	})
}

func TestOnPaymentFailed_CancelsAndReleases(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	o := seedPending(t, st, "res-1")
	inv := &fakeInventoryFull{}
	orch := saga.New(st, saga.Clients{Inventory: inv})

	if err := orch.OnPaymentFailed(ctx, "evt-1", o.ID, "card_declined"); err != nil {
		t.Fatalf("OnPaymentFailed: %v", err)
	}
	got, err := st.Get(ctx, o.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusCancelled || got.CancelReason != "PAYMENT_FAILED:card_declined" {
		t.Fatalf("order = %+v", got)
	}
	if len(inv.releases) != 1 || inv.releases[0] != "res-1" {
		t.Fatalf("releases = %v", inv.releases)
	}

	// Re-delivery of the same event: no-op, no second Release.
	if err := orch.OnPaymentFailed(ctx, "evt-1", o.ID, "card_declined"); err != nil {
		t.Fatalf("re-delivery: %v", err)
	}
	if len(inv.releases) != 1 {
		t.Fatalf("releases after re-delivery = %v, want still 1", inv.releases)
	}
}

func TestOnPaymentFailed_NoReservationIDSkipsRelease(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	o := seedPending(t, st, "")
	inv := &fakeInventoryFull{}
	orch := saga.New(st, saga.Clients{Inventory: inv})

	if err := orch.OnPaymentFailed(ctx, "evt-1", o.ID, "card_declined"); err != nil {
		t.Fatalf("OnPaymentFailed: %v", err)
	}
	if len(inv.releases) != 0 {
		t.Fatalf("releases = %v, want none for an empty reservation id", inv.releases)
	}
}

func TestOnShipmentDelivered(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	t.Run("single shop group fulfills immediately", func(t *testing.T) {
		o := seedPending(t, st, "res-1", "")
		if _, err := st.Apply(ctx, o.ID, "confirm-"+o.ID, func(o *domain.Order) error { return o.Confirm() }); err != nil {
			t.Fatal(err)
		}
		orch := saga.New(st, saga.Clients{})
		if err := orch.OnShipmentDelivered(ctx, "evt-1", o.ID, ""); err != nil {
			t.Fatalf("OnShipmentDelivered: %v", err)
		}
		got, err := st.Get(ctx, o.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != domain.StatusFulfilled {
			t.Fatalf("status = %s, want FULFILLED", got.Status)
		}
	})

	t.Run("waits for every shop group before fulfilling", func(t *testing.T) {
		o := seedPending(t, st, "res-2", "shop-a", "shop-b")
		if _, err := st.Apply(ctx, o.ID, "confirm-"+o.ID, func(o *domain.Order) error { return o.Confirm() }); err != nil {
			t.Fatal(err)
		}
		orch := saga.New(st, saga.Clients{})

		if err := orch.OnShipmentDelivered(ctx, "evt-a", o.ID, "shop-a"); err != nil {
			t.Fatalf("shop-a delivered: %v", err)
		}
		mid, err := st.Get(ctx, o.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if mid.Status != domain.StatusConfirmed {
			t.Fatalf("status after 1 of 2 shops delivered = %s, want still CONFIRMED", mid.Status)
		}

		if err := orch.OnShipmentDelivered(ctx, "evt-b", o.ID, "shop-b"); err != nil {
			t.Fatalf("shop-b delivered: %v", err)
		}
		final, err := st.Get(ctx, o.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if final.Status != domain.StatusFulfilled {
			t.Fatalf("status after both shops delivered = %s, want FULFILLED", final.Status)
		}
	})

	t.Run("a cancelled order is left untouched", func(t *testing.T) {
		o := seedPending(t, st, "res-3", "")
		if _, err := st.Apply(ctx, o.ID, "cancel-"+o.ID, func(o *domain.Order) error { return o.Cancel("x") }); err != nil {
			t.Fatal(err)
		}
		orch := saga.New(st, saga.Clients{})
		if err := orch.OnShipmentDelivered(ctx, "evt-1", o.ID, ""); err != nil {
			t.Fatalf("OnShipmentDelivered: %v", err)
		}
		got, err := st.Get(ctx, o.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != domain.StatusCancelled {
			t.Fatalf("status = %s, want still CANCELLED", got.Status)
		}
	})
}

func TestOnReservationExpired(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	t.Run("found by reservation id: cancels and voids", func(t *testing.T) {
		o := seedPending(t, st, "res-1")
		pay := &fakePaymentFull{}
		orch := saga.New(st, saga.Clients{Payment: pay})

		if err := orch.OnReservationExpired(ctx, "evt-1", "res-1"); err != nil {
			t.Fatalf("OnReservationExpired: %v", err)
		}
		got, err := st.Get(ctx, o.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != domain.StatusCancelled || got.CancelReason != "RESERVATION_EXPIRED" {
			t.Fatalf("order = %+v", got)
		}
		if len(pay.voids) != 1 || pay.voids[0] != "pay-1" {
			t.Fatalf("voids = %v", pay.voids)
		}

		// Re-delivery of the same event: no-op, no second Void.
		if err := orch.OnReservationExpired(ctx, "evt-1", "res-1"); err != nil {
			t.Fatalf("re-delivery: %v", err)
		}
		if len(pay.voids) != 1 {
			t.Fatalf("voids after re-delivery = %v, want still 1", pay.voids)
		}
	})

	t.Run("falls back to treating the ref as an order id", func(t *testing.T) {
		o := seedPending(t, st, "") // no reservation id at all
		pay := &fakePaymentFull{}
		orch := saga.New(st, saga.Clients{Payment: pay})

		if err := orch.OnReservationExpired(ctx, "evt-2", o.ID); err != nil {
			t.Fatalf("OnReservationExpired: %v", err)
		}
		got, err := st.Get(ctx, o.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != domain.StatusCancelled {
			t.Fatalf("status = %s", got.Status)
		}
	})

	t.Run("an already-resolved order is left alone, no compensation", func(t *testing.T) {
		o := seedPending(t, st, "res-3")
		if _, err := st.Apply(ctx, o.ID, "confirm-"+o.ID, func(o *domain.Order) error { return o.Confirm() }); err != nil {
			t.Fatal(err)
		}
		pay := &fakePaymentFull{}
		orch := saga.New(st, saga.Clients{Payment: pay})

		if err := orch.OnReservationExpired(ctx, "evt-3", "res-3"); err != nil {
			t.Fatalf("OnReservationExpired: %v", err)
		}
		got, err := st.Get(ctx, o.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != domain.StatusConfirmed {
			t.Fatalf("status = %s, want still CONFIRMED", got.Status)
		}
		if len(pay.voids) != 0 {
			t.Fatalf("voids = %v, want none for an already-confirmed order", pay.voids)
		}
	})

	t.Run("no such order or reservation: silent no-op", func(t *testing.T) {
		orch := saga.New(st, saga.Clients{})
		if err := orch.OnReservationExpired(ctx, "evt-4", "does-not-exist"); err != nil {
			t.Fatalf("OnReservationExpired: %v", err)
		}
	})
}

// --- CompensateCancelled: doesn't touch the store, no Postgres needed ----

func TestCompensateCancelled(t *testing.T) {
	pay := &fakePaymentFull{}
	inv := &fakeInventoryFull{}
	orch := saga.New(nil, saga.Clients{Payment: pay, Inventory: inv})
	ctx := context.Background()

	orch.CompensateCancelled(ctx, nil) // must not panic
	if len(pay.voids)+len(inv.releases) != 0 {
		t.Fatal("nil order should not trigger any compensation")
	}

	orch.CompensateCancelled(ctx, &domain.Order{Status: domain.StatusConfirmed, ReservationID: "res-x", PaymentID: "pay-x"})
	if len(pay.voids)+len(inv.releases) != 0 {
		t.Fatal("a non-cancelled order should not trigger any compensation")
	}

	orch.CompensateCancelled(ctx, &domain.Order{Status: domain.StatusCancelled, ReservationID: "res-1", PaymentID: "pay-1"})
	if len(inv.releases) != 1 || inv.releases[0] != "res-1" {
		t.Fatalf("releases = %v", inv.releases)
	}
	if len(pay.voids) != 1 || pay.voids[0] != "pay-1" {
		t.Fatalf("voids = %v", pay.voids)
	}
}
