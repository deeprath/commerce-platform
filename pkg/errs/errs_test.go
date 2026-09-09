package errs

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
)

func TestKindMapping(t *testing.T) {
	cases := []struct {
		kind Kind
		code codes.Code
		http int
	}{
		{KindInternal, codes.Internal, 500},
		{KindInvalidArgument, codes.InvalidArgument, 400},
		{KindNotFound, codes.NotFound, 404},
		{KindUnauthenticated, codes.Unauthenticated, 401},
		{KindPermissionDenied, codes.PermissionDenied, 403},
		{KindConflict, codes.Aborted, 409},
		{KindResourceExhausted, codes.ResourceExhausted, 429},
		{KindUnavailable, codes.Unavailable, 503},
	}
	for _, c := range cases {
		if got := c.kind.GRPCCode(); got != c.code {
			t.Errorf("kind %d: gRPC code = %v, want %v", c.kind, got, c.code)
		}
		if got := c.kind.HTTPStatus(); got != c.http {
			t.Errorf("kind %d: HTTP = %d, want %d", c.kind, got, c.http)
		}
	}
}

func TestWrapUnwrapAndIs(t *testing.T) {
	sentinel := errors.New("boom")
	e := Wrap(sentinel, KindUnavailable, "DB_DOWN", "cannot reach primary").WithMeta("host", "pg-0")

	if !errors.Is(e, sentinel) {
		t.Fatal("errors.Is should find the wrapped cause")
	}
	if !Is(e, KindUnavailable) {
		t.Fatal("Is(KindUnavailable) should be true")
	}
	if Is(e, KindNotFound) {
		t.Fatal("Is(KindNotFound) should be false")
	}
	if e.Meta["host"] != "pg-0" {
		t.Fatalf("meta not attached: %v", e.Meta)
	}
}

func TestToStatus(t *testing.T) {
	if ToStatus(nil).Code() != codes.OK {
		t.Fatal("nil => OK")
	}
	if ToStatus(errors.New("x")).Code() != codes.Internal {
		t.Fatal("plain error => Internal")
	}
	if got := ToStatus(New(KindNotFound, "GONE", "nope")); got.Code() != codes.NotFound || got.Message() != "GONE" {
		t.Fatalf("domain error => %v/%q", got.Code(), got.Message())
	}
	wrapped := fmt.Errorf("layer: %w", New(KindPermissionDenied, "NOPE", "denied"))
	if ToStatus(wrapped).Code() != codes.PermissionDenied {
		t.Fatal("ToStatus should see through fmt.Errorf wrapping")
	}
}
