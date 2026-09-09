package domain

import (
	"testing"
	"time"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

func usd(c int64) Money { return Money{Currency: "USD", Cents: c} }

func lines() []Line {
	return []Line{
		{ProductID: "b", Title: "B", Quantity: 2, UnitPrice: usd(1500)}, // 3000
		{ProductID: "a", Title: "A", Quantity: 1, UnitPrice: usd(999)},  // 999
	}
}

func TestPriceNoCoupon(t *testing.T) {
	q, err := Price("USD", lines(), nil, 800, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if q.Subtotal.Cents != 3999 {
		t.Fatalf("subtotal = %d", q.Subtotal.Cents)
	}
	if q.Discount.Cents != 0 {
		t.Fatalf("discount = %d", q.Discount.Cents)
	}
	if q.Tax.Cents != 320 { // 8% of 3999 = 319.92 -> 320
		t.Fatalf("tax = %d", q.Tax.Cents)
	}
	if q.Total.Cents != 4319 {
		t.Fatalf("total = %d", q.Total.Cents)
	}
	if q.Signature == "" {
		t.Fatal("signature missing")
	}
}

func TestPricePercentCoupon(t *testing.T) {
	c := &Coupon{Code: "SAVE10", Kind: CouponPercent, PercentOff: 10}
	q, err := Price("USD", lines(), c, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if q.Discount.Cents != 400 { // 10% of 3999 = 399.9 -> 400
		t.Fatalf("discount = %d", q.Discount.Cents)
	}
	if q.Total.Cents != 3599 {
		t.Fatalf("total = %d", q.Total.Cents)
	}
	if q.Coupon != "SAVE10" {
		t.Fatalf("coupon = %q", q.Coupon)
	}
}

func TestPriceAmountCouponCappedAtSubtotal(t *testing.T) {
	c := &Coupon{Code: "BIG", Kind: CouponAmount, AmountOff: usd(999999)}
	q, err := Price("USD", lines(), c, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if q.Discount.Cents != 3999 || q.Total.Cents != 0 {
		t.Fatalf("discount=%d total=%d", q.Discount.Cents, q.Total.Cents)
	}
}

func TestPriceExpiredCoupon(t *testing.T) {
	c := &Coupon{Code: "OLD", Kind: CouponPercent, PercentOff: 50, ExpiresAt: time.Now().Add(-time.Hour)}
	_, err := Price("USD", lines(), c, 0, time.Now())
	if !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("expired coupon => %v", err)
	}
}

func TestPriceRejectsEmptyAndBadQty(t *testing.T) {
	if _, err := Price("USD", nil, nil, 0, time.Now()); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("empty => %v", err)
	}
	bad := []Line{{ProductID: "a", Quantity: 0, UnitPrice: usd(1)}}
	if _, err := Price("USD", bad, nil, 0, time.Now()); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("bad qty => %v", err)
	}
}

func TestSignatureStableAcrossLineOrder(t *testing.T) {
	a, _ := Price("USD", lines(), nil, 800, time.Now())
	rev := []Line{lines()[1], lines()[0]}
	b, _ := Price("USD", rev, nil, 800, time.Now())
	if a.Signature != b.Signature {
		t.Fatal("signature must not depend on line order")
	}
}

func TestMoneyRoundTrip(t *testing.T) {
	m := FromUnitsNanos("USD", 29, 990000000)
	if m.Cents != 2999 {
		t.Fatalf("cents = %d", m.Cents)
	}
	u, n := m.UnitsNanos()
	if u != 29 || n != 990000000 {
		t.Fatalf("round trip = %d,%d", u, n)
	}
}
