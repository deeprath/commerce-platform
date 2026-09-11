// Package domain is the payout aggregate and its state machine. No proto/SQL.
package domain

import (
	"time"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type Status string

const (
	StatusPending Status = "PENDING"
	StatusPaid    Status = "PAID"
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
func (m Money) Add(o Money) Money {
	if m.Currency == "" {
		m.Currency = o.Currency
	}
	return Money{m.Currency, m.Cents + o.Cents}
}

type Payout struct {
	ID        string
	OrderID   string
	ShopID    string
	Amount    Money
	Status    Status
	CreatedAt time.Time
	PaidAt    *time.Time
}

// CanTransitionTo reports whether the payout may move to `to`.
func (p *Payout) CanTransitionTo(to Status) bool {
	return p.Status == StatusPending && to == StatusPaid
}

// MarkPaid moves PENDING -> PAID.
func (p *Payout) MarkPaid() error {
	if p.Status == StatusPaid {
		return nil // idempotent
	}
	if !p.CanTransitionTo(StatusPaid) {
		return errs.New(errs.KindFailedPrecondition, "BAD_TRANSITION", "payout cannot be paid from "+string(p.Status))
	}
	p.Status = StatusPaid
	return nil
}
