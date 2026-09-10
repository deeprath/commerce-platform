package checker

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func mkToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	if claims["exp"] == nil {
		claims["exp"] = time.Now().Add(time.Hour).Unix()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte("unverified"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPublicRouteNeedsNoToken(t *testing.T) {
	c := New(DefaultConfig())
	for _, p := range []string{"/api/v1/catalog/products", "/api/v1/events"} {
		if d := c.Check(p, nil); !d.Allow {
			t.Fatalf("public route %s should allow: %+v", p, d)
		}
	}
}

func TestProtectedRouteRequiresToken(t *testing.T) {
	c := New(DefaultConfig())
	d := c.Check("/api/v1/orders", map[string]string{})
	if d.Allow || d.Status != 401 || d.Reason != "NO_TOKEN" {
		t.Fatalf("want 401 NO_TOKEN, got %+v", d)
	}
}

func TestProtectedRouteAcceptsBearer(t *testing.T) {
	c := New(DefaultConfig())
	tok := mkToken(t, jwt.MapClaims{"sub": "u1"})
	d := c.Check("/api/v1/orders", map[string]string{"authorization": "Bearer " + tok})
	if !d.Allow {
		t.Fatalf("valid bearer should allow: %+v", d)
	}
}

func TestProtectedRouteAcceptsCookie(t *testing.T) {
	c := New(DefaultConfig())
	tok := mkToken(t, jwt.MapClaims{"sub": "u1"})
	d := c.Check("/api/v1/cart", map[string]string{"cookie": "theme=dark; access_token=" + tok + "; x=y"})
	if !d.Allow {
		t.Fatalf("cookie token should allow: %+v", d)
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	c := New(DefaultConfig())
	tok := mkToken(t, jwt.MapClaims{"sub": "u1", "exp": time.Now().Add(-2 * time.Hour).Unix()})
	d := c.Check("/api/v1/orders", map[string]string{"authorization": "Bearer " + tok})
	if d.Allow || d.Reason != "TOKEN_REJECTED" {
		t.Fatalf("expired token: %+v", d)
	}
}

func TestAdminRouteRequiresAdminRole(t *testing.T) {
	c := New(DefaultConfig())

	plain := mkToken(t, jwt.MapClaims{"sub": "u1",
		"realm_access": map[string]any{"roles": []any{"customer"}}})
	if d := c.Check("/api/v1/admin/products", map[string]string{"authorization": "Bearer " + plain}); d.Allow || d.Status != 403 {
		t.Fatalf("customer on admin route: %+v", d)
	}

	admin := mkToken(t, jwt.MapClaims{"sub": "u2",
		"realm_access": map[string]any{"roles": []any{"platform_admin"}}})
	if d := c.Check("/api/v1/admin/products", map[string]string{"authorization": "Bearer " + admin}); !d.Allow {
		t.Fatalf("admin on admin route should allow: %+v", d)
	}
}
