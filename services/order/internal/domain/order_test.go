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

func TestShopGroups(t *testing.T) {
	o := &Order{Lines: []Line{
		{ProductID: "p1", ShopID: "shop-b"},
		{ProductID: "p2", ShopID: ""}, // first-party
		{ProductID: "p3", ShopID: "shop-a"},
		{ProductID: "p4", ShopID: "shop-b"}, // duplicate group, deduped
	}}
	got := o.ShopGroups()
	want := []string{"", "shop-a", "shop-b"}
	if len(got) != len(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("groups = %v, want %v", got, want)
		}
	}
}

func TestShopGroups_AllFirstParty(t *testing.T) {
	o := &Order{Lines: []Line{{ProductID: "p1"}, {ProductID: "p2"}}}
	got := o.ShopGroups()
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("groups = %v, want a single empty (first-party) group", got)
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
