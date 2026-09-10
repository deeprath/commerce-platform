package domain

import (
	"testing"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

func usd(u int64) Money { return Money{CurrencyCode: "USD", Units: u} }

func TestNewProductValidation(t *testing.T) {
	_, err := NewProduct(NewProductInput{Slug: "ab", Title: "T", Price: usd(10), CreatedBy: "u1"})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("short slug => %v", err)
	}
	_, err = NewProduct(NewProductInput{Slug: "good-slug", Title: "  ", Price: usd(10), CreatedBy: "u1"})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("blank title => %v", err)
	}
	_, err = NewProduct(NewProductInput{Slug: "good-slug", Title: "T", Price: Money{CurrencyCode: "US", Units: 1}, CreatedBy: "u1"})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("bad currency => %v", err)
	}
	_, err = NewProduct(NewProductInput{Slug: "good-slug", Title: "T", Price: Money{CurrencyCode: "USD", Units: -1}, CreatedBy: "u1"})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("negative price => %v", err)
	}

	p, err := NewProduct(NewProductInput{Slug: "My Cool Shirt", Title: "Cool Shirt", Description: "d", CategoryID: "cat1", Price: usd(2500), MediaKeys: []string{"k1"}, Attributes: map[string]string{"color": "red"}, CreatedBy: "u1"})
	if err != nil {
		t.Fatalf("valid product: %v", err)
	}
	if p.Slug != "my-cool-shirt" {
		t.Fatalf("slug not normalised: %q", p.Slug)
	}
	if p.Status != StatusDraft {
		t.Fatalf("new product should be DRAFT, got %s", p.Status)
	}
}

func TestApplyUpdate(t *testing.T) {
	p, _ := NewProduct(NewProductInput{Slug: "slug-one", Title: "One", Price: usd(10), CreatedBy: "u1"})

	if err := p.ApplyUpdate("Two", "d2", "cat2", usd(20), []string{"k"}, map[string]string{"a": "b"}, StatusActive); err != nil {
		t.Fatalf("valid update: %v", err)
	}
	if p.Title != "Two" || p.Status != StatusActive || p.ListPrice.Units != 20 {
		t.Fatalf("update not applied: %+v", p)
	}

	if err := p.ApplyUpdate("Two", "", "", usd(20), nil, nil, StatusArchived); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("update to ARCHIVED should be rejected: %v", err)
	}

	p.Archive()
	if err := p.ApplyUpdate("Three", "", "", usd(20), nil, nil, StatusActive); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("update of archived product => %v", err)
	}
}
