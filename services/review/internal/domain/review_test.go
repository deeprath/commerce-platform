package domain

import (
	"strings"
	"testing"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

func TestNewReview_Valid(t *testing.T) {
	r, err := NewReview("p1", "u1", "  Ada  ", 5, "  Great  ", "  Loved it.  ")
	if err != nil {
		t.Fatalf("NewReview: %v", err)
	}
	if r.ProductID != "p1" || r.AuthorID != "u1" || r.AuthorName != "Ada" ||
		r.Rating != 5 || r.Title != "Great" || r.Body != "Loved it." {
		t.Fatalf("fields not normalised: %+v", r)
	}
	if r.Status != StatusPublished || !r.VerifiedPurchase {
		t.Fatalf("want published + verified: %+v", r)
	}
}

func TestNewReview_Rejections(t *testing.T) {
	cases := []struct {
		name      string
		pid, body string
		rating    int32
		wantKind  errs.Kind
	}{
		{"no product", "", "b", 4, errs.KindInvalidArgument},
		{"rating too low", "p1", "b", 0, errs.KindInvalidArgument},
		{"rating too high", "p1", "b", 6, errs.KindInvalidArgument},
		{"empty body", "p1", "   ", 4, errs.KindInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewReview(c.pid, "u1", "n", c.rating, "t", c.body); !errs.Is(err, c.wantKind) {
				t.Fatalf("got %v, want kind %v", err, c.wantKind)
			}
		})
	}
}

func TestNewReview_TruncatesAndDefaultsName(t *testing.T) {
	longTitle := strings.Repeat("x", MaxTitleRunes+50)
	longBody := strings.Repeat("y", MaxBodyRunes+50)
	r, err := NewReview("p1", "u1", "", 3, longTitle, longBody)
	if err != nil {
		t.Fatalf("NewReview: %v", err)
	}
	if len([]rune(r.Title)) != MaxTitleRunes || len([]rune(r.Body)) != MaxBodyRunes {
		t.Fatalf("not truncated: title=%d body=%d", len([]rune(r.Title)), len([]rune(r.Body)))
	}
	if r.AuthorName != "Customer" {
		t.Fatalf("blank name should default to Customer, got %q", r.AuthorName)
	}
}

func TestValidModerationTarget(t *testing.T) {
	if !ValidModerationTarget(StatusPublished) || !ValidModerationTarget(StatusHidden) {
		t.Fatal("PUBLISHED/HIDDEN should be valid targets")
	}
	if ValidModerationTarget(Status("BOGUS")) || ValidModerationTarget(Status("")) {
		t.Fatal("unknown status should not be a valid target")
	}
}
