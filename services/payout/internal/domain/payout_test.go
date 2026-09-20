package domain

import (
	"testing"
	"time"
)

func TestMarkPaid(t *testing.T) {
	p := &Payout{Status: StatusPending}
	if err := p.MarkPaid(); err != nil || p.Status != StatusPaid {
		t.Fatalf("MarkPaid: %v %s", err, p.Status)
	}
	// Idempotent.
	if err := p.MarkPaid(); err != nil {
		t.Fatalf("idempotent MarkPaid: %v", err)
	}
}

func TestCanTransitionTo(t *testing.T) {
	if (&Payout{Status: StatusPending}).CanTransitionTo(StatusPaid) != true {
		t.Fatal("PENDING -> PAID should be allowed")
	}
	if (&Payout{Status: StatusPaid}).CanTransitionTo(StatusPaid) != false {
		t.Fatal("PAID -> PAID via CanTransitionTo should be false (MarkPaid handles idempotency itself)")
	}
}

func TestMoneyRoundTripAndAdd(t *testing.T) {
	m := FromUnitsNanos("USD", 43, 190000000)
	if m.Cents != 4319 {
		t.Fatalf("cents=%d", m.Cents)
	}
	u, n := m.UnitsNanos()
	if u != 43 || n != 190000000 {
		t.Fatalf("roundtrip %d,%d", u, n)
	}
	sum := FromUnitsNanos("USD", 10, 0).Add(FromUnitsNanos("USD", 5, 50000000))
	if sum.Cents != 1505 {
		t.Fatalf("sum cents = %d, want 1505", sum.Cents)
	}
	// Add onto a zero-value Money picks up the other side's currency.
	var zero Money
	got := zero.Add(FromUnitsNanos("EUR", 1, 0))
	if got.Currency != "EUR" || got.Cents != 100 {
		t.Fatalf("Add onto zero value: %+v", got)
	}
}

func usd(cents int64) Money { return Money{Currency: "USD", Cents: cents} }

func TestReversePartialThenFull(t *testing.T) {
	p := &Payout{Status: StatusPending, Amount: usd(1000)}

	applied, err := p.Reverse(usd(400))
	if err != nil || applied.Cents != 400 {
		t.Fatalf("first reversal: applied=%d err=%v", applied.Cents, err)
	}
	// Partly reversed keeps its status — only Outstanding tells the truth.
	if p.Status != StatusPending {
		t.Fatalf("status after partial reversal = %s, want PENDING", p.Status)
	}
	if p.Outstanding().Cents != 600 {
		t.Fatalf("outstanding = %d, want 600", p.Outstanding().Cents)
	}

	// Reversals accumulate rather than replace.
	if applied, err = p.Reverse(usd(600)); err != nil || applied.Cents != 600 {
		t.Fatalf("second reversal: applied=%d err=%v", applied.Cents, err)
	}
	if p.Reversed.Cents != 1000 || p.Outstanding().Cents != 0 {
		t.Fatalf("reversed=%d outstanding=%d", p.Reversed.Cents, p.Outstanding().Cents)
	}
	if p.Status != StatusReversed {
		t.Fatalf("status after full reversal = %s, want REVERSED", p.Status)
	}
}

func TestReverseClampsAtOutstanding(t *testing.T) {
	// A refund can exceed a shop's line total (shipping, goodwill). Reversing
	// more than was owed would invent money, so it clamps.
	p := &Payout{Status: StatusPending, Amount: usd(500)}
	applied, err := p.Reverse(usd(900))
	if err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if applied.Cents != 500 {
		t.Fatalf("applied = %d, want 500 (clamped)", applied.Cents)
	}
	if p.Reversed.Cents != 500 || p.Status != StatusReversed {
		t.Fatalf("reversed=%d status=%s", p.Reversed.Cents, p.Status)
	}

	// A repeat against a fully reversed payout is a zero-amount no-op, so a
	// redelivered event cannot drive the total past the amount.
	if applied, err = p.Reverse(usd(100)); err != nil || applied.Cents != 0 {
		t.Fatalf("repeat reversal: applied=%d err=%v", applied.Cents, err)
	}
	if p.Reversed.Cents != 500 {
		t.Fatalf("reversed drifted to %d", p.Reversed.Cents)
	}
}

func TestReversePaidPayoutIsADebt(t *testing.T) {
	paid := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	p := &Payout{Status: StatusPaid, Amount: usd(1000), PaidAt: &paid}

	if _, err := p.Reverse(usd(1000)); err != nil {
		t.Fatalf("reversing a PAID payout should be allowed: %v", err)
	}
	if p.Status != StatusReversed {
		t.Fatalf("status = %s, want REVERSED", p.Status)
	}
	// Status no longer says PAID, so paid_at is what tells a consumer the
	// money already left and has to be recovered.
	if !p.WasPaid() {
		t.Fatal("WasPaid() should stay true after a paid payout is reversed")
	}
}

func TestReverseRejectsBadInput(t *testing.T) {
	p := &Payout{Status: StatusPending, Amount: usd(1000)}
	if _, err := p.Reverse(usd(0)); err == nil {
		t.Fatal("zero reversal should be rejected")
	}
	if _, err := p.Reverse(usd(-5)); err == nil {
		t.Fatal("negative reversal should be rejected")
	}
	if _, err := p.Reverse(Money{Currency: "EUR", Cents: 100}); err == nil {
		t.Fatal("currency mismatch should be rejected")
	}
	if p.Reversed.Cents != 0 {
		t.Fatalf("a rejected reversal must not mutate: reversed=%d", p.Reversed.Cents)
	}
}

func TestFullyReversedCannotBePaid(t *testing.T) {
	p := &Payout{Status: StatusPending, Amount: usd(1000)}
	if _, err := p.Reverse(usd(1000)); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if p.CanTransitionTo(StatusPaid) {
		t.Fatal("a fully reversed payout has nothing left to pay")
	}
	if err := p.MarkPaid(); err == nil {
		t.Fatal("MarkPaid on a REVERSED payout should fail")
	}
}
