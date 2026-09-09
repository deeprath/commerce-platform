package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestBearerFromAuthHeader(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
		ok   bool
	}{
		"lower":     {"bearer abc", "abc", true},
		"canonical": {"Bearer abc.def.ghi", "abc.def.ghi", true},
		"mixed":     {"BeArEr   spaced  ", "spaced", true},
		"empty":     {"", "", false},
		"no prefix": {"abc", "", false},
		"just word": {"Bearer", "", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := BearerFromAuthHeader(c.in)
			if ok != c.ok || got != c.want {
				t.Fatalf("BearerFromAuthHeader(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestExtractRealmRoles(t *testing.T) {
	claims := jwt.MapClaims{
		"realm_access": map[string]any{"roles": []any{"customer", "order_manager", 42}},
	}
	got := extractRealmRoles(claims)
	if len(got) != 2 || got[0] != "customer" || got[1] != "order_manager" {
		t.Fatalf("extractRealmRoles = %v", got)
	}
	if extractRealmRoles(jwt.MapClaims{}) != nil {
		t.Fatal("no realm_access => nil")
	}
}

func TestPrincipalRoleHelpers(t *testing.T) {
	p := &Principal{Roles: []string{"customer", "csr"}}
	if !p.HasRole("csr") || p.HasRole("admin") {
		t.Fatal("HasRole")
	}
	if !p.HasAnyRole("admin", "customer") || p.HasAnyRole("admin", "finance") {
		t.Fatal("HasAnyRole")
	}
}

func TestParseInsecure(t *testing.T) {
	// Unsigned-but-structural token, not expired.
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	raw, err := tok.SignedString([]byte("irrelevant-we-do-not-verify"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseInsecure(raw)
	if err != nil {
		t.Fatalf("ParseInsecure valid token: %v", err)
	}
	if claims["sub"] != "user-1" {
		t.Fatalf("claims = %v", claims)
	}

	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "user-1",
		"exp": time.Now().Add(-2 * time.Hour).Unix(),
	})
	rawExp, _ := expired.SignedString([]byte("x"))
	if _, err := ParseInsecure(rawExp); err == nil {
		t.Fatal("ParseInsecure should reject an expired token")
	}

	if _, err := ParseInsecure("not-a-jwt"); err == nil {
		t.Fatal("ParseInsecure should reject a non-JWT")
	}
}

func TestContextRoundTrip(t *testing.T) {
	ctx := WithPrincipal(t.Context(), &Principal{Subject: "s"})
	if FromContext(ctx).Subject != "s" {
		t.Fatal("principal did not round-trip through context")
	}
	if FromContext(t.Context()) != nil {
		t.Fatal("empty context => nil principal")
	}
}
