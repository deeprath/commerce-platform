package domain

import (
	"testing"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

func TestConfirmTransitions(t *testing.T) {
	o := &Order{Status: StatusPendingPayment}
	if err := o.Confirm(); err != nil || o.Status != StatusConfirmed {
		t.Fatalf("confirm: %v %s", err, o.Status)
	}
	if err := o.Confirm(); err != nil {
		t.Fatalf("confirm is idempotent: %v", err)
	}

	c := &Order{Status: StatusCancelled}
	if err := c.Confirm(); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("confirm cancelled => %v", err)
	}
}

func TestCancelTransitions(t *testing.T) {
	o := &Order{Status: StatusPendingPayment}
	if err := o.Cancel("PAYMENT_FAILED"); err != nil || o.Status != StatusCancelled || o.CancelReason != "PAYMENT_FAILED" {
		t.Fatalf("cancel: %v %s %s", err, o.Status, o.CancelReason)
	}
	if err := o.Cancel("again"); err != nil {
		t.Fatalf("cancel is idempotent: %v", err)
	}

	ful := &Order{Status: StatusFulfilled}
	if err := ful.Cancel("x"); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("cancel fulfilled => %v", err)
	}
}

func TestFulfillTransitions(t *testing.T) {
	o := &Order{Status: StatusConfirmed}
	if err := o.Fulfill(); err != nil || o.Status != StatusFulfilled {
		t.Fatalf("fulfill: %v %s", err, o.Status)
	}
	if err := o.Fulfill(); err != nil {
		t.Fatalf("fulfill is idempotent: %v", err)
	}

	for _, from := range []Status{StatusPendingPayment, StatusCancelled} {
		bad := &Order{Status: from}
		if err := bad.Fulfill(); !errs.Is(err, errs.KindFailedPrecondition) {
			t.Fatalf("fulfill from %s => %v", from, err)
		}
	}
}

func TestMoneyRoundTrip(t *testing.T) {
	m := FromUnitsNanos("USD", 43, 190000000)
	if m.Cents != 4319 {
		t.Fatalf("cents=%d", m.Cents)
	}
	u, n := m.UnitsNanos()
	if u != 43 || n != 190000000 {
		t.Fatalf("roundtrip %d,%d", u, n)
	}
}
