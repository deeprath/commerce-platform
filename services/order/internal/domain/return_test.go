package domain

import (
	"testing"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

func TestReturnDecide(t *testing.T) {
	// approve
	r := &Return{Status: ReturnRequested}
	if err := r.Decide(true, "op-1", "ok"); err != nil || r.Status != ReturnApproved {
		t.Fatalf("approve: %v %s", err, r.Status)
	}
	if r.DecidedBy != "op-1" || r.DecisionNote != "ok" {
		t.Fatalf("decision metadata not set: %+v", r)
	}
	// idempotent same decision
	if err := r.Decide(true, "op-2", "again"); err != nil {
		t.Fatalf("re-approve should be a no-op: %v", err)
	}
	// can't flip an approved return to rejected
	if err := r.Decide(false, "op-1", ""); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("approve->reject: want FailedPrecondition, got %v", err)
	}

	// reject from REQUESTED
	rej := &Return{Status: ReturnRequested}
	if err := rej.Decide(false, "op-1", "damaged on arrival"); err != nil || rej.Status != ReturnRejected {
		t.Fatalf("reject: %v %s", err, rej.Status)
	}
}
