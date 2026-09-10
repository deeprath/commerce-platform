package domain_test

import (
	"errors"
	"testing"

	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/seller/internal/domain"
)

func reason(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return err.Error()
}

func TestNewShop_ValidatesAndSlugifies(t *testing.T) {
	sh, err := domain.NewShop("user-1", "  Práxis Goods & Co.  ", "Handmade things", "hi@praxis.example")
	if err != nil {
		t.Fatalf("NewShop: %v", err)
	}
	if sh.OwnerID != "user-1" || sh.Name != "Práxis Goods & Co." {
		t.Fatalf("fields not trimmed/kept: %+v", sh)
	}
	if sh.Slug != "pr-xis-goods-co" {
		t.Fatalf("slug = %q", sh.Slug)
	}
	if sh.Status != domain.StatusPendingReview {
		t.Fatalf("status = %q, want PENDING_REVIEW", sh.Status)
	}
}

func TestNewShop_Rejects(t *testing.T) {
	cases := []struct{ name, owner, shopName, email, want string }{
		{"no owner", "", "Good Name", "", "OWNER_REQUIRED"},
		{"short name", "u", "x", "", "BAD_NAME"},
		{"nameless slug", "u", "!!! ???", "", "BAD_NAME"},
		{"bad email", "u", "Good Name", "not-an-email", "BAD_EMAIL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := domain.NewShop(c.owner, c.shopName, "", c.email)
			if err == nil || reason(err) != c.want {
				t.Fatalf("got %v, want reason %s", err, c.want)
			}
		})
	}
}

func TestShop_Transitions(t *testing.T) {
	sh, _ := domain.NewShop("u", "My Shop", "", "")

	changed, err := sh.Activate()
	if err != nil || !changed || sh.Status != domain.StatusActive {
		t.Fatalf("activate: changed=%v err=%v status=%q", changed, err, sh.Status)
	}
	if changed, _ := sh.Activate(); changed {
		t.Fatal("re-activate should be a no-op")
	}
	if _, err := sh.Suspend("  "); err == nil {
		t.Fatal("suspend without reason should fail")
	}
	changed, err = sh.Suspend("counterfeit goods")
	if err != nil || !changed || sh.Status != domain.StatusSuspended || sh.SuspensionReason == "" {
		t.Fatalf("suspend: %+v err=%v", sh, err)
	}
	if _, err := sh.Activate(); err != nil || sh.Status != domain.StatusActive || sh.SuspensionReason != "" {
		t.Fatalf("reactivate: %+v err=%v", sh, err)
	}
}

func TestShop_ApplyUpdate_KeepsSlug(t *testing.T) {
	sh, _ := domain.NewShop("u", "Old Name", "", "")
	slug := sh.Slug
	if err := sh.ApplyUpdate("A Totally Different Name", "new desc", "x@y.z"); err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if sh.Name != "A Totally Different Name" || sh.Slug != slug {
		t.Fatalf("slug should be stable: name=%q slug=%q (was %q)", sh.Name, sh.Slug, slug)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Hello World":      "hello-world",
		"  --Trim--  ":     "trim",
		"Ünìcödé Shop 123": "n-c-d-shop-123",
		`a/b\c`:            "a-b-c",
	} {
		if got := domain.Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
