package domain

import (
	"time"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

// ReturnStatus is the lifecycle of an RMA.
type ReturnStatus string

const (
	ReturnRequested ReturnStatus = "REQUESTED"
	ReturnApproved  ReturnStatus = "APPROVED"
	ReturnRejected  ReturnStatus = "REJECTED"
)

// ReturnLine is one product being returned.
type ReturnLine struct {
	ProductID    string
	Quantity     int32
	RefundAmount Money // unit refund * qty, plus this line's share of order tax
}

// Return is an RMA against a fulfilled order.
type Return struct {
	ID           string
	OrderID      string
	OwnerID      string
	Status       ReturnStatus
	Reason       string
	Lines        []ReturnLine
	RefundTotal  Money
	DecidedBy    string
	DecisionNote string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Decide moves a REQUESTED return to APPROVED or REJECTED. Idempotent when the
// return is already in the target state; any other transition is rejected.
func (r *Return) Decide(approve bool, decidedBy, note string) error {
	want := ReturnRejected
	if approve {
		want = ReturnApproved
	}
	if r.Status == want {
		return nil // idempotent
	}
	if r.Status != ReturnRequested {
		return errs.New(errs.KindFailedPrecondition, "BAD_TRANSITION",
			"return is "+string(r.Status)+", not REQUESTED")
	}
	r.Status = want
	r.DecidedBy = decidedBy
	r.DecisionNote = note
	return nil
}
