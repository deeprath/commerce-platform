// Package domain is the shipment aggregate and its state machine. No proto/SQL.
package domain

import (
	"time"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type Status string

const (
	StatusPending   Status = "PENDING"   // created, awaiting carrier pickup
	StatusShipped   Status = "SHIPPED"   // in transit
	StatusDelivered Status = "DELIVERED" // terminal, happy path
	StatusCancelled Status = "CANCELLED" // terminal
)

type Item struct {
	ProductID string
	Title     string
	Quantity  int32
}

type Address struct {
	FullName, Line1, Line2, City, Region, PostalCode, CountryCode, Phone string
}

type Shipment struct {
	ID             string
	OrderID        string
	OwnerID        string
	Status         Status
	Carrier        string
	TrackingNumber string
	ShipTo         Address
	Items          []Item
	CreatedAt      time.Time
	ShippedAt      *time.Time
	DeliveredAt    *time.Time
	CancelReason   string
}

// CanTransitionTo reports whether the shipment may move to `to`.
func (s *Shipment) CanTransitionTo(to Status) bool {
	switch s.Status {
	case StatusPending:
		return to == StatusShipped || to == StatusCancelled
	case StatusShipped:
		return to == StatusDelivered || to == StatusCancelled
	default:
		return false
	}
}

// MarkShipped moves PENDING -> SHIPPED, recording carrier + tracking.
func (s *Shipment) MarkShipped(carrier, tracking string) error {
	if s.Status == StatusShipped {
		return nil // idempotent
	}
	if !s.CanTransitionTo(StatusShipped) {
		return errs.New(errs.KindFailedPrecondition, "BAD_TRANSITION",
			"shipment cannot ship from "+string(s.Status))
	}
	s.Status = StatusShipped
	if carrier != "" {
		s.Carrier = carrier
	}
	if tracking != "" {
		s.TrackingNumber = tracking
	}
	return nil
}

// MarkDelivered moves SHIPPED -> DELIVERED.
func (s *Shipment) MarkDelivered() error {
	if s.Status == StatusDelivered {
		return nil // idempotent
	}
	if !s.CanTransitionTo(StatusDelivered) {
		return errs.New(errs.KindFailedPrecondition, "BAD_TRANSITION",
			"shipment cannot be delivered from "+string(s.Status))
	}
	s.Status = StatusDelivered
	return nil
}

// Cancel moves a not-yet-delivered shipment -> CANCELLED with a reason.
func (s *Shipment) Cancel(reason string) error {
	if s.Status == StatusCancelled {
		return nil // idempotent
	}
	if !s.CanTransitionTo(StatusCancelled) {
		return errs.New(errs.KindFailedPrecondition, "BAD_TRANSITION",
			"shipment cannot be cancelled from "+string(s.Status))
	}
	s.Status = StatusCancelled
	s.CancelReason = reason
	return nil
}
