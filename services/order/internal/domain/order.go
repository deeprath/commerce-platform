// Package domain is the order aggregate and its state machine. No proto/SQL.
package domain

import (
	"sort"
	"time"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type Status string

const (
	StatusPendingPayment Status = "PENDING_PAYMENT"
	StatusConfirmed      Status = "CONFIRMED"
	StatusCancelled      Status = "CANCELLED"
	StatusFulfilled      Status = "FULFILLED"
)

// Money is a currency amount in minor units.
type Money struct {
	Currency string
	Cents    int64
}

func FromUnitsNanos(cur string, units int64, nanos int32) Money {
	return Money{cur, units*100 + int64(nanos)/10_000_000}
}
func (m Money) UnitsNanos() (int64, int32) {
	return m.Cents / 100, int32(m.Cents%100) * 10_000_000
}

type Line struct {
	ProductID string
	Title     string
	Quantity  int32
	UnitPrice Money
	LineTotal Money
	// ShopID is the owning marketplace shop, copied from pricing at checkout
	// time. Empty => a first-party line.
	ShopID string
}

type Address struct {
	FullName, Line1, Line2, City, Region, PostalCode, CountryCode, Phone string
}

type Order struct {
	ID               string
	OwnerID          string
	Status           Status
	Lines            []Line
	Subtotal         Money
	Discount         Money
	Tax              Money
	Total            Money
	ShipTo           Address
	CartID           string
	CouponCode       string
	PaymentID        string
	ReservationID    string
	PricingSignature string
	CancelReason     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ShopGroups returns the distinct shop_ids across the order's lines, sorted,
// with "" (first-party) included as its own group when present. This is the
// set of shipments fulfillment will create for the order — one per group —
// and what the order waits on before it can be marked FULFILLED.
func (o *Order) ShopGroups() []string {
	seen := map[string]bool{}
	var groups []string
	for _, l := range o.Lines {
		if !seen[l.ShopID] {
			seen[l.ShopID] = true
			groups = append(groups, l.ShopID)
		}
	}
	sort.Strings(groups)
	return groups
}

// CanTransitionTo reports whether the order may move to `to`.
func (o *Order) CanTransitionTo(to Status) bool {
	switch o.Status {
	case StatusPendingPayment:
		return to == StatusConfirmed || to == StatusCancelled
	case StatusConfirmed:
		return to == StatusFulfilled || to == StatusCancelled
	default:
		return false
	}
}

// Confirm moves PENDING_PAYMENT -> CONFIRMED.
func (o *Order) Confirm() error {
	if o.Status == StatusConfirmed {
		return nil // idempotent
	}
	if !o.CanTransitionTo(StatusConfirmed) {
		return errs.New(errs.KindFailedPrecondition, "BAD_TRANSITION", "order cannot be confirmed from "+string(o.Status))
	}
	o.Status = StatusConfirmed
	return nil
}

// Cancel moves an open order -> CANCELLED with a reason.
func (o *Order) Cancel(reason string) error {
	if o.Status == StatusCancelled {
		return nil // idempotent
	}
	if !o.CanTransitionTo(StatusCancelled) {
		return errs.New(errs.KindFailedPrecondition, "BAD_TRANSITION", "order cannot be cancelled from "+string(o.Status))
	}
	o.Status = StatusCancelled
	o.CancelReason = reason
	return nil
}

// Fulfill moves CONFIRMED -> FULFILLED (all shipments delivered).
func (o *Order) Fulfill() error {
	if o.Status == StatusFulfilled {
		return nil // idempotent
	}
	if !o.CanTransitionTo(StatusFulfilled) {
		return errs.New(errs.KindFailedPrecondition, "BAD_TRANSITION", "order cannot be fulfilled from "+string(o.Status))
	}
	o.Status = StatusFulfilled
	return nil
}
