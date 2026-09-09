package domain

import (
	"strings"
	"testing"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

func TestNewUpload(t *testing.T) {
	if _, err := NewUpload("nope", "a.jpg", "image/jpeg", 100, "u1"); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("bad bucket => %v", err)
	}
	if _, err := NewUpload("product-media", "a.exe", "application/x-msdownload", 100, "u1"); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("bad content type => %v", err)
	}
	if _, err := NewUpload("product-media", "a.jpg", "image/jpeg", MaxUploadBytes+1, "u1"); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("oversize => %v", err)
	}

	a, err := NewUpload("product-media", "My Photo.PNG", "image/png", 2048, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.Key, "product-media/") || !strings.HasSuffix(a.Key, ".png") {
		t.Fatalf("unexpected key: %s", a.Key)
	}
	if a.Status != StatusPending {
		t.Fatalf("status: %s", a.Status)
	}

	b, _ := NewUpload("product-media", "x.png", "image/png", 2048, "u1")
	if a.Key == b.Key {
		t.Fatal("keys must be unique")
	}
}

func TestMarkReady(t *testing.T) {
	a, _ := NewUpload("product-media", "x.jpg", "image/jpeg", 1000, "u1")

	if err := a.MarkReady(1000, "image/jpeg"); err != nil {
		t.Fatalf("valid confirm: %v", err)
	}
	if a.Status != StatusReady {
		t.Fatalf("status: %s", a.Status)
	}
	if err := a.MarkReady(1000, "image/jpeg"); err != nil {
		t.Fatalf("second confirm should be idempotent: %v", err)
	}

	c, _ := NewUpload("product-media", "y.jpg", "image/jpeg", 1000, "u1")
	if err := c.MarkReady(1000, "image/png"); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("content-type mismatch => %v", err)
	}
}
