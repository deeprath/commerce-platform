package domain

import (
	"testing"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

func TestCanTransitionTo(t *testing.T) {
	cases := []struct {
		from Status
		to   Status
		ok   bool
	}{
		{StatusPending, StatusShipped, true},
		{StatusPending, StatusCancelled, true},
		{StatusPending, StatusDelivered, false},
		{StatusShipped, StatusDelivered, true},
		{StatusShipped, StatusCancelled, true},
		{StatusShipped, StatusPending, false},
		{StatusDelivered, StatusShipped, false},
		{StatusDelivered, StatusCancelled, false},
		{StatusCancelled, StatusShipped, false},
	}
	for _, c := range cases {
		got := (&Shipment{Status: c.from}).CanTransitionTo(c.to)
		if got != c.ok {
			t.Errorf("%s -> %s: got %v want %v", c.from, c.to, got, c.ok)
		}
	}
}

func TestMarkShipped(t *testing.T) {
	s := &Shipment{Status: StatusPending}
	if err := s.MarkShipped("UPS", "1Z999"); err != nil {
		t.Fatalf("MarkShipped: %v", err)
	}
	if s.Status != StatusShipped || s.Carrier != "UPS" || s.TrackingNumber != "1Z999" {
		t.Fatalf("unexpected shipment after ship: %+v", s)
	}

	// Idempotent: a second call is a no-op and does not clobber carrier/tracking.
	if err := s.MarkShipped("", ""); err != nil {
		t.Fatalf("second MarkShipped: %v", err)
	}
	if s.Carrier != "UPS" || s.TrackingNumber != "1Z999" {
		t.Fatalf("idempotent ship clobbered fields: %+v", s)
	}

	// Cannot ship a delivered shipment.
	d := &Shipment{Status: StatusDelivered}
	if err := d.MarkShipped("UPS", "x"); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("ship from DELIVERED: want FailedPrecondition, got %v", err)
	}
}

func TestMarkDelivered(t *testing.T) {
	s := &Shipment{Status: StatusShipped}
	if err := s.MarkDelivered(); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	if s.Status != StatusDelivered {
		t.Fatalf("status = %s, want DELIVERED", s.Status)
	}
	if err := s.MarkDelivered(); err != nil {
		t.Fatalf("idempotent MarkDelivered: %v", err)
	}

	p := &Shipment{Status: StatusPending}
	if err := p.MarkDelivered(); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("deliver from PENDING: want FailedPrecondition, got %v", err)
	}
}

func TestCancel(t *testing.T) {
	for _, from := range []Status{StatusPending, StatusShipped} {
		s := &Shipment{Status: from}
		if err := s.Cancel("LOST_IN_TRANSIT"); err != nil {
			t.Fatalf("cancel from %s: %v", from, err)
		}
		if s.Status != StatusCancelled || s.CancelReason != "LOST_IN_TRANSIT" {
			t.Fatalf("unexpected after cancel from %s: %+v", from, s)
		}
	}

	d := &Shipment{Status: StatusDelivered}
	if err := d.Cancel("x"); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("cancel from DELIVERED: want FailedPrecondition, got %v", err)
	}

	// Idempotent.
	c := &Shipment{Status: StatusCancelled, CancelReason: "FIRST"}
	if err := c.Cancel("SECOND"); err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
	if c.CancelReason != "FIRST" {
		t.Fatalf("idempotent cancel changed reason: %+v", c)
	}
}
