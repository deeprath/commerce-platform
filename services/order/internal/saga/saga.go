// Package saga is the checkout orchestrator. CreateOrder runs the forward path
// (quote -> reserve -> open payment -> persist); event handlers advance or
// compensate the order on payment.* / inventory.* events. Every step is
// idempotent and compensations are explicit — no distributed transactions.
package saga

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	pricingv1 "github.com/deeprath/commerce-platform/gen/go/commerce/pricing/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

const reservationTTL = 15 * time.Minute

// Clients bundles the downstream services the saga drives.
type Clients struct {
	Cart      cartv1.CartServiceClient
	Pricing   pricingv1.PricingServiceClient
	Inventory inventoryv1.InventoryServiceClient
	Payment   paymentv1.PaymentServiceClient
}

type Orchestrator struct {
	store *store.Store
	cl    Clients
}

func New(s *store.Store, cl Clients) *Orchestrator { return &Orchestrator{store: s, cl: cl} }

// CreateInput is the checkout request.
type CreateInput struct {
	OwnerID     string
	CartID      string
	Currency    string
	CouponCode  string
	MethodToken string
	ShipTo      domain.Address
}

// CreateResult is what CreateOrder returns.
type CreateResult struct {
	Order         *domain.Order
	PaymentSecret string
}

// CreateOrder runs the forward path. `ctx` carries the shopper's bearer token,
// forwarded to pricing/inventory/payment.
func (o *Orchestrator) CreateOrder(ctx context.Context, in CreateInput) (*CreateResult, error) {
	if in.OwnerID == "" {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required to check out")
	}
	if in.CartID == "" {
		return nil, errs.New(errs.KindInvalidArgument, "CART_ID_REQUIRED", "cart_id is required")
	}
	if len(in.Currency) != 3 {
		in.Currency = "USD"
	}

	// 1. cart
	cart, err := o.cl.Cart.GetCart(ctx, &cartv1.GetCartRequest{CartId: in.CartID})
	if err != nil {
		return nil, errs.Wrap(err, errs.KindUnavailable, "CART_UNAVAILABLE", "cannot read the cart")
	}
	if len(cart.GetItems()) == 0 {
		return nil, errs.New(errs.KindFailedPrecondition, "CART_EMPTY", "the cart has no items")
	}

	// 2. price
	quoteItems := make([]*pricingv1.QuoteLineInput, 0, len(cart.GetItems()))
	reserveItems := make([]*inventoryv1.LineItem, 0, len(cart.GetItems()))
	for _, it := range cart.GetItems() {
		quoteItems = append(quoteItems, &pricingv1.QuoteLineInput{ProductId: it.GetProductId(), Quantity: it.GetQuantity()})
		reserveItems = append(reserveItems, &inventoryv1.LineItem{ProductId: it.GetProductId(), Quantity: it.GetQuantity()})
	}
	quote, err := o.cl.Pricing.QuotePrice(ctx, &pricingv1.QuoteRequest{
		Items: quoteItems, CurrencyCode: in.Currency, CouponCode: in.CouponCode,
		ShipTo: addrMsg(in.ShipTo),
	})
	if err != nil {
		return nil, errs.Wrap(err, errs.KindFailedPrecondition, "QUOTE_FAILED", "could not price the cart")
	}

	orderID := uuid.NewString()

	// 3. reserve stock (compensation on later failure: Release)
	res, err := o.cl.Inventory.Reserve(ctx, &inventoryv1.ReserveRequest{
		OrderRef: orderID, Items: reserveItems, TtlSeconds: int32(reservationTTL.Seconds()),
	})
	if err != nil {
		return nil, errs.Wrap(err, errs.KindFailedPrecondition, "RESERVE_FAILED", "could not reserve stock")
	}

	// 4. open payment intent (compensation on failure: Release the reservation)
	pay, err := o.cl.Payment.CreatePayment(ctx, &paymentv1.CreatePaymentRequest{
		OrderId: orderID, Amount: quote.GetTotal(), MethodToken: in.MethodToken,
	})
	if err != nil {
		o.compensateRelease(ctx, res.GetReservationId())
		return nil, errs.Wrap(err, errs.KindUnavailable, "PAYMENT_INIT_FAILED", "could not start payment")
	}

	// 5. persist the order (compensation on failure: Release + Void)
	ord := &domain.Order{
		ID: orderID, OwnerID: in.OwnerID, Status: domain.StatusPendingPayment,
		Lines:            linesFromQuote(quote),
		Subtotal:         moneyFrom(quote.GetSubtotal()),
		Discount:         moneyFrom(quote.GetDiscount()),
		Tax:              moneyFrom(quote.GetTax()),
		Total:            moneyFrom(quote.GetTotal()),
		ShipTo:           in.ShipTo,
		CartID:           in.CartID,
		CouponCode:       quote.GetCouponCode(),
		PaymentID:        pay.GetPaymentId(),
		ReservationID:    res.GetReservationId(),
		PricingSignature: quote.GetPricingSignature(),
	}
	if err := o.store.Insert(ctx, ord); err != nil {
		o.compensateRelease(ctx, res.GetReservationId())
		o.compensateVoid(ctx, pay.GetPaymentId())
		return nil, err
	}

	// 6. best-effort: clear the cart
	if _, err := o.cl.Cart.Clear(ctx, &cartv1.ClearRequest{CartId: in.CartID}); err != nil {
		slog.WarnContext(ctx, "could not clear cart after order", slog.String("cart_id", in.CartID), slog.Any("err", err))
	}

	return &CreateResult{Order: ord, PaymentSecret: pay.GetClientSecret()}, nil
}

// OnPaymentAuthorized: commit the reservation and confirm the order.
func (o *Orchestrator) OnPaymentAuthorized(ctx context.Context, eventID, orderID string) error {
	ord, err := o.store.Apply(ctx, orderID, eventID, func(o *domain.Order) error { return o.Confirm() })
	if err != nil || ord == nil {
		return err
	}
	if ord.Status == domain.StatusConfirmed && ord.ReservationID != "" {
		if _, err := o.cl.Inventory.Commit(ctx, &inventoryv1.CommitRequest{ReservationId: ord.ReservationID}); err != nil {
			// Commit is idempotent; a transient failure is retried by the consumer.
			return errs.Wrap(err, errs.KindUnavailable, "COMMIT_FAILED", "inventory commit failed")
		}
	}
	return nil
}

// OnPaymentFailed: release the reservation and cancel the order.
func (o *Orchestrator) OnPaymentFailed(ctx context.Context, eventID, orderID, reason string) error {
	ord, err := o.store.Apply(ctx, orderID, eventID, func(o *domain.Order) error {
		return o.Cancel("PAYMENT_FAILED:" + reason)
	})
	if err != nil || ord == nil {
		return err
	}
	o.compensateRelease(ctx, ord.ReservationID)
	return nil
}

// OnReservationExpired: void the payment and cancel the order if still pending.
func (o *Orchestrator) OnReservationExpired(ctx context.Context, eventID, orderRef string) error {
	ord, err := o.store.FindByReservationID(ctx, orderRef)
	if errs.Is(err, errs.KindNotFound) {
		// orderRef is the order id in ReservationExpired.OrderRef
		ord, err = o.store.Get(ctx, orderRef, "")
	}
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return nil
		}
		return err
	}
	applied, err := o.store.Apply(ctx, ord.ID, eventID, func(o *domain.Order) error {
		if o.Status != domain.StatusPendingPayment {
			return nil // already resolved
		}
		return o.Cancel("RESERVATION_EXPIRED")
	})
	if err != nil || applied == nil {
		return err
	}
	if applied.Status == domain.StatusCancelled {
		o.compensateVoid(ctx, applied.PaymentID)
	}
	return nil
}

// CompensateCancelled releases the reservation and voids the payment for an
// order that was just cancelled (e.g. by the customer). Safe on a nil/already-
// resolved order.
func (o *Orchestrator) CompensateCancelled(ctx context.Context, ord *domain.Order) {
	if ord == nil || ord.Status != domain.StatusCancelled {
		return
	}
	o.compensateRelease(ctx, ord.ReservationID)
	o.compensateVoid(ctx, ord.PaymentID)
}

func (o *Orchestrator) compensateRelease(ctx context.Context, resID string) {
	if resID == "" {
		return
	}
	if _, err := o.cl.Inventory.Release(ctx, &inventoryv1.ReleaseRequest{ReservationId: resID}); err != nil {
		slog.WarnContext(ctx, "compensation Release failed", slog.String("reservation_id", resID), slog.Any("err", err))
	}
}

func (o *Orchestrator) compensateVoid(ctx context.Context, paymentID string) {
	if paymentID == "" {
		return
	}
	if _, err := o.cl.Payment.Void(ctx, &paymentv1.VoidRequest{PaymentId: paymentID}); err != nil {
		slog.WarnContext(ctx, "compensation Void failed", slog.String("payment_id", paymentID), slog.Any("err", err))
	}
}

// --- mapping helpers ---

func moneyFrom(m *commonv1.Money) domain.Money {
	if m == nil {
		return domain.Money{}
	}
	return domain.FromUnitsNanos(m.GetCurrencyCode(), m.GetUnits(), m.GetNanos())
}

func linesFromQuote(q *pricingv1.Quote) []domain.Line {
	out := make([]domain.Line, 0, len(q.GetLines()))
	for _, l := range q.GetLines() {
		out = append(out, domain.Line{
			ProductID: l.GetProductId(), Title: l.GetTitle(), Quantity: l.GetQuantity(),
			UnitPrice: moneyFrom(l.GetUnitPrice()), LineTotal: moneyFrom(l.GetLineTotal()),
		})
	}
	return out
}

func addrMsg(a domain.Address) *commonv1.Address {
	return &commonv1.Address{
		FullName: a.FullName, Line1: a.Line1, Line2: a.Line2, City: a.City,
		Region: a.Region, PostalCode: a.PostalCode, CountryCode: a.CountryCode, Phone: a.Phone,
	}
}
