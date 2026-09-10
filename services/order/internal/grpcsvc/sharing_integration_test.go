package grpcsvc_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/fga"
	"github.com/deeprath/commerce-platform/services/order/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/order/internal/saga"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

// fakeSharer is an in-memory fga.API.
type fakeSharer struct {
	mu     sync.Mutex
	tuples map[string]bool // "user|relation|object"
	failOn string          // method name to force an error on ("check"/"write"/...)
}

func newFakeSharer() *fakeSharer { return &fakeSharer{tuples: map[string]bool{}} }

func key(u, r, o string) string { return u + "|" + r + "|" + o }

func (f *fakeSharer) Check(_ context.Context, u, r, o string) (bool, error) {
	if f.failOn == "check" {
		return false, errs.New(errs.KindUnavailable, "FGA_DOWN", "boom")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tuples[key(u, r, o)], nil
}

func (f *fakeSharer) Write(_ context.Context, u, r, o string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tuples[key(u, r, o)] {
		return &fga.Error{Status: 400, Message: "tuple already exists"}
	}
	f.tuples[key(u, r, o)] = true
	return nil
}

func (f *fakeSharer) Delete(_ context.Context, u, r, o string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.tuples[key(u, r, o)] {
		return &fga.Error{Status: 400, Message: "tuple not found"}
	}
	delete(f.tuples, key(u, r, o))
	return nil
}

func (f *fakeSharer) Read(_ context.Context, o string) ([]fga.Tuple, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fga.Tuple
	for k := range f.tuples {
		p := strings.Split(k, "|")
		if p[2] == o {
			out = append(out, fga.Tuple{User: p[0], Relation: p[1], Object: p[2]})
		}
	}
	return out, nil
}

func newServerFGA(st *store.Store, sh *fakeSharer) *grpcsvc.Server {
	return grpcsvc.New(saga.New(st, saga.Clients{Payment: fakePayment{}, Inventory: fakeInventory{}}), st, sh)
}

func TestOrderSharing_EndToEnd(t *testing.T) {
	st := store.New(spinUp(t))
	sh := newFakeSharer()
	srv := newServerFGA(st, sh)

	o := seedFulfilled(t, st, "owner-1")

	// A stranger cannot see it.
	if _, err := srv.GetOrder(customer("stranger"), &orderv1.GetOrderRequest{Id: o.ID}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("stranger GetOrder err = %v, want NotFound", err)
	}

	// Non-owner cannot share it.
	if _, err := srv.ShareOrder(customer("stranger"), &orderv1.ShareOrderRequest{OrderId: o.ID, GranteeSubject: "friend"}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("non-owner ShareOrder err = %v, want NotFound", err)
	}

	// Owner shares with "friend".
	if _, err := srv.ShareOrder(customer("owner-1"), &orderv1.ShareOrderRequest{OrderId: o.ID, GranteeSubject: "friend"}); err != nil {
		t.Fatalf("ShareOrder: %v", err)
	}
	// Idempotent.
	if _, err := srv.ShareOrder(customer("owner-1"), &orderv1.ShareOrderRequest{OrderId: o.ID, GranteeSubject: "friend"}); err != nil {
		t.Fatalf("second ShareOrder should be a no-op: %v", err)
	}

	// friend can now GetOrder via the FGA path.
	got, err := srv.GetOrder(customer("friend"), &orderv1.GetOrderRequest{Id: o.ID})
	if err != nil {
		t.Fatalf("friend GetOrder after share: %v", err)
	}
	if got.GetId() != o.ID || got.GetOwnerId() != "owner-1" {
		t.Fatalf("friend got wrong order: %+v", got)
	}

	// ListOrderShares shows the grantee.
	shares, err := srv.ListOrderShares(customer("owner-1"), &orderv1.ListOrderSharesRequest{OrderId: o.ID})
	if err != nil {
		t.Fatalf("ListOrderShares: %v", err)
	}
	if len(shares.GetGranteeSubjects()) != 1 || shares.GetGranteeSubjects()[0] != "friend" {
		t.Fatalf("shares = %v, want [friend]", shares.GetGranteeSubjects())
	}

	// Revoke, then friend is locked out again.
	if _, err := srv.RevokeOrderShare(customer("owner-1"), &orderv1.RevokeOrderShareRequest{OrderId: o.ID, GranteeSubject: "friend"}); err != nil {
		t.Fatalf("RevokeOrderShare: %v", err)
	}
	if _, err := srv.RevokeOrderShare(customer("owner-1"), &orderv1.RevokeOrderShareRequest{OrderId: o.ID, GranteeSubject: "friend"}); err != nil {
		t.Fatalf("second Revoke should be a no-op: %v", err)
	}
	if _, err := srv.GetOrder(customer("friend"), &orderv1.GetOrderRequest{Id: o.ID}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("friend GetOrder after revoke err = %v, want NotFound", err)
	}
}

func TestOrderSharing_Guards(t *testing.T) {
	st := store.New(spinUp(t))
	o := seedFulfilled(t, st, "owner-2")

	// Sharing disabled (nil FGA) -> Unavailable.
	plain := newServer(st)
	if _, err := plain.ShareOrder(customer("owner-2"), &orderv1.ShareOrderRequest{OrderId: o.ID, GranteeSubject: "x"}); !errs.Is(err, errs.KindUnavailable) {
		t.Fatalf("nil-FGA ShareOrder err = %v, want Unavailable", err)
	}

	srv := newServerFGA(st, newFakeSharer())

	// Empty grantee.
	if _, err := srv.ShareOrder(customer("owner-2"), &orderv1.ShareOrderRequest{OrderId: o.ID, GranteeSubject: "  "}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("empty grantee err = %v, want InvalidArgument", err)
	}
	// Granting to yourself.
	if _, err := srv.ShareOrder(customer("owner-2"), &orderv1.ShareOrderRequest{OrderId: o.ID, GranteeSubject: "owner-2"}); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("self-grant err = %v, want InvalidArgument", err)
	}
}

func TestGetOrder_FGAErrorFailsClosed(t *testing.T) {
	st := store.New(spinUp(t))
	o := seedFulfilled(t, st, "owner-3")
	sh := newFakeSharer()
	sh.failOn = "check"
	srv := newServerFGA(st, sh)

	// FGA Check errors -> the original NotFound stands (no accidental grant).
	if _, err := srv.GetOrder(customer("stranger"), &orderv1.GetOrderRequest{Id: o.ID}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("GetOrder with FGA down err = %v, want NotFound", err)
	}
}
