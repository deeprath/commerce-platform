package domain

import (
	"strings"
	"testing"
)

func TestRender_AllKindsHaveTemplates(t *testing.T) {
	for _, k := range Kinds() {
		r, ok := Render(k, map[string]string{
			"order_id": "abcd1234", "total": "USD 12.00", "reason": "PAYMENT_FAILED",
			"shipment_id": "ship5678", "carrier": "SANDBOX", "tracking_number": "TRK-1",
		})
		if !ok {
			t.Fatalf("kind %q has no template", k)
		}
		if r.Subject == "" || r.Body == "" {
			t.Fatalf("kind %q rendered empty: %+v", k, r)
		}
		if strings.Contains(r.Subject+r.Body, "{") {
			t.Fatalf("kind %q left an unsubstituted placeholder: %+v", k, r)
		}
	}
}

func TestRender_UnknownKind(t *testing.T) {
	if _, ok := Render("nope", nil); ok {
		t.Fatal("unknown kind should not render")
	}
}

func TestSubst_MissingAndEmptyValues(t *testing.T) {
	// Empty value -> "-"
	r, _ := Render("order_cancelled", map[string]string{"order_id": "o1", "reason": ""})
	if !strings.Contains(r.Body, "(-)") {
		t.Fatalf("empty reason should render as '-': %q", r.Body)
	}
	// Missing key -> placeholder is left as-is only if no data at all; with a
	// partial map the unknown placeholder stays literal, which the all-kinds
	// test guards against for the real call sites.
	r2, _ := Render("order_created", map[string]string{"order_id": "o1"})
	if !strings.Contains(r2.Subject, "o1") {
		t.Fatalf("known key not substituted: %q", r2.Subject)
	}
}
