package domain

import "testing"

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
