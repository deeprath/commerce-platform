package grpcx

import (
	"testing"

	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

func TestRequireRole(t *testing.T) {
	ctx := auth.WithPrincipal(t.Context(), &auth.Principal{Roles: []string{"order_manager"}})

	if err := RequireRole(ctx, "order_manager"); err != nil {
		t.Fatalf("holder should pass: %v", err)
	}
	err := RequireRole(ctx, "admin")
	if !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("non-holder => %v, want PermissionDenied", err)
	}
	if err := RequireRole(t.Context(), "any"); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("no principal => %v, want Unauthenticated", err)
	}
}

func TestRequireAnyRole(t *testing.T) {
	ctx := auth.WithPrincipal(t.Context(), &auth.Principal{Roles: []string{"csr"}})
	if err := RequireAnyRole(ctx, "admin", "csr"); err != nil {
		t.Fatalf("holder of one role should pass: %v", err)
	}
	if err := RequireAnyRole(ctx, "admin", "finance"); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("=> %v, want PermissionDenied", err)
	}
}
