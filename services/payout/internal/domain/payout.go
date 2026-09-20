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
	// StatusReversed is terminal and means fully reversed. A payout reversed
	// in part keeps PENDING or PAID and reports the amount via Reversed —
	// status alone never tells you what is owed, Outstanding does.
	StatusReversed Status = "REVERSED"
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

func (m Money) Sub(o Money) Money {
	if m.Currency == "" {
		m.Currency = o.Currency
	}
	return Money{m.Currency, m.Cents - o.Cents}
}

type Payout struct {
	ID         string
	OrderID    string
	ShopID     string
	Amount     Money
	Status     Status
	CreatedAt  time.Time
	PaidAt     *time.Time
	Reversed   Money // cumulative across every reversal; never exceeds Amount
	ReversedAt *time.Time
}

// Outstanding is what the shop is still owed — or, once PaidAt is set, what
// was overpaid and has to be recovered.
func (p *Payout) Outstanding() Money {
	return p.Amount.Sub(p.Reversed)
}

// WasPaid reports whether money has already left for this payout. Status
// cannot answer this once a paid payout is fully reversed, but paid_at
// survives the transition, which is why this reads the timestamp.
func (p *Payout) WasPaid() bool { return p.PaidAt != nil }

// CanTransitionTo reports whether the payout may move to `to`. A fully
// reversed payout is terminal: there is nothing left to pay.
func (p *Payout) CanTransitionTo(to Status) bool {
	switch to {
	case StatusPaid:
		return p.Status == StatusPending
	case StatusReversed:
		return p.Status == StatusPending || p.Status == StatusPaid
	default:
		return false
	}
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

// Reverse applies an approved return's share against this payout and reports
// how much was actually reversed.
//
// It clamps at Outstanding rather than erroring, because a refund total can
// legitimately exceed a shop's line total (shipping, goodwill) and the payout
// is only ever the line total — reversing more than was owed would invent
// money. A payout already fully reversed absorbs a repeat as a zero-amount
// no-op, so a redelivered event is harmless on top of the store's
// processed_events dedup.
//
// Reversing a PAID payout is legal and is the case that matters: the money has
// already gone, so the reversal is a debt to recover rather than a smaller
// settlement.
func (p *Payout) Reverse(amount Money) (Money, error) {
	if amount.Cents <= 0 {
		return Money{}, errs.New(errs.KindInvalidArgument, "BAD_REVERSAL",
			"reversal amount must be positive")
	}
	if amount.Currency != "" && p.Amount.Currency != "" && amount.Currency != p.Amount.Currency {
		return Money{}, errs.New(errs.KindInvalidArgument, "CURRENCY_MISMATCH",
			"reversal is "+amount.Currency+", payout is "+p.Amount.Currency)
	}

	applied := Money{Currency: p.Amount.Currency, Cents: amount.Cents}
	if out := p.Outstanding(); applied.Cents > out.Cents {
		applied.Cents = out.Cents
	}
	if applied.Cents <= 0 {
		return Money{Currency: p.Amount.Currency}, nil // nothing left to reverse
	}

	p.Reversed = p.Reversed.Add(applied)
	if p.Outstanding().Cents == 0 {
		p.Status = StatusReversed
	}
	return applied, nil
}
