package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

const testKID = "test-kid-1"

// jwksServer starts an httptest server serving a JWKS document for the given
// RSA public key, and returns its URL. keyfunc/jwkset fetches this
// synchronously inside NewStorageFromHTTP, before NewVerifier returns — so a
// token signed by the matching private key verifies immediately, no polling
// or sleep needed.
func jwksServer(t *testing.T, pub *rsa.PublicKey) string {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(bigEndianBytes(pub.E))
	jwk := map[string]any{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKID,
		"n": n, "e": e,
	}
	body, err := json.Marshal(map[string]any{"keys": []any{jwk}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

// bigEndianBytes encodes a small positive int (the RSA public exponent, e.g.
// 65537) as the minimal big-endian byte string a JWK "e" expects.
func bigEndianBytes(v int) []byte {
	var b []byte
	for v > 0 {
		b = append([]byte{byte(v & 0xff)}, b...)
		v >>= 8
	}
	return b
}

func genRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNewVerifier_RequiresJWKSURLAndIssuer(t *testing.T) {
	ctx := t.Context()
	if _, err := NewVerifier(ctx, Config{Issuer: "https://issuer.test"}); err == nil {
		t.Fatal("expected an error with no JWKSURL")
	}
	if _, err := NewVerifier(ctx, Config{JWKSURL: "http://example.invalid/certs"}); err == nil {
		t.Fatal("expected an error with no Issuer")
	}
}

func TestNewVerifier_UnreachableJWKSSucceedsWithAnEmptyKeySet(t *testing.T) {
	// Documents real, verified behavior, not the intuitive one: keyfunc's
	// underlying jwkset client hardcodes NoErrorReturnFirstHTTPReq: true, so a
	// JWKS endpoint that 500s on the initial fetch does NOT fail NewVerifier —
	// it logs the failure and returns a Verifier with an empty key set. The
	// practical effect is fail-closed (every subsequent Verify rejects with
	// "unknown kid", same as TestVerify_UnknownKidRejected below), but a
	// startup health check that only checks NewVerifier's error return will
	// NOT catch "Keycloak was unreachable at boot" — the service reports
	// healthy while rejecting every token.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	v, err := NewVerifier(t.Context(), Config{JWKSURL: ts.URL, Issuer: "https://issuer.test"})
	if err != nil {
		t.Fatalf("NewVerifier returned an error — this test's own doc comment is now wrong: %v", err)
	}

	key := genRSAKey(t)
	raw := signRS256(t, key, testKID, jwt.MapClaims{
		"sub": "user-1", "iss": "https://issuer.test", "exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(t.Context(), raw); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("Verify against an empty key set: err = %v, want KindUnauthenticated", err)
	}
}

func newTestVerifier(t *testing.T, issuer string) (*Verifier, *rsa.PrivateKey) {
	t.Helper()
	key := genRSAKey(t)
	url := jwksServer(t, &key.PublicKey)
	v, err := NewVerifier(t.Context(), Config{JWKSURL: url, Issuer: issuer})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v, key
}

func TestVerify_ValidTokenPopulatesPrincipal(t *testing.T) {
	v, key := newTestVerifier(t, "https://issuer.test")
	raw := signRS256(t, key, testKID, jwt.MapClaims{
		"sub":   "user-123",
		"email": "a@example.com",
		"iss":   "https://issuer.test",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"realm_access": map[string]any{
			"roles": []any{"customer", "order_manager"},
		},
	})

	p, err := v.Verify(t.Context(), raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Subject != "user-123" || p.Email != "a@example.com" {
		t.Fatalf("principal = %+v", p)
	}
	if !p.HasRole("customer") || !p.HasRole("order_manager") || p.HasRole("admin") {
		t.Fatalf("roles = %v", p.Roles)
	}
	if p.Raw["sub"] != "user-123" {
		t.Fatalf("Raw claims not attached: %v", p.Raw)
	}
}

func TestVerify_MissingSubjectRejected(t *testing.T) {
	v, key := newTestVerifier(t, "https://issuer.test")
	raw := signRS256(t, key, testKID, jwt.MapClaims{
		"iss": "https://issuer.test",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := v.Verify(t.Context(), raw)
	if !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("err = %v, want KindUnauthenticated", err)
	}
}

func TestVerify_WrongIssuerRejected(t *testing.T) {
	v, key := newTestVerifier(t, "https://issuer.test")
	raw := signRS256(t, key, testKID, jwt.MapClaims{
		"sub": "user-1",
		"iss": "https://someone-else.test",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := v.Verify(t.Context(), raw)
	if !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("err = %v, want KindUnauthenticated", err)
	}
}

func TestVerify_ExpiredTokenRejected(t *testing.T) {
	v, key := newTestVerifier(t, "https://issuer.test")
	raw := signRS256(t, key, testKID, jwt.MapClaims{
		"sub": "user-1",
		"iss": "https://issuer.test",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	_, err := v.Verify(t.Context(), raw)
	if !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("err = %v, want KindUnauthenticated", err)
	}
}

func TestVerify_MissingExpirationRejected(t *testing.T) {
	// jwt.WithExpirationRequired() means a token with no exp claim at all
	// must fail, not be treated as "never expires".
	v, key := newTestVerifier(t, "https://issuer.test")
	raw := signRS256(t, key, testKID, jwt.MapClaims{
		"sub": "user-1",
		"iss": "https://issuer.test",
	})
	if _, err := v.Verify(t.Context(), raw); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("err = %v, want KindUnauthenticated", err)
	}
}

func TestVerify_WrongSigningKeyRejected(t *testing.T) {
	v, _ := newTestVerifier(t, "https://issuer.test")
	other := genRSAKey(t) // never published to the JWKS server
	raw := signRS256(t, other, testKID, jwt.MapClaims{
		"sub": "user-1",
		"iss": "https://issuer.test",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(t.Context(), raw); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatal("expected a signature mismatch to be rejected")
	}
}

func TestVerify_UnknownKidRejected(t *testing.T) {
	v, key := newTestVerifier(t, "https://issuer.test")
	raw := signRS256(t, key, "some-other-kid", jwt.MapClaims{
		"sub": "user-1",
		"iss": "https://issuer.test",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(t.Context(), raw); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatal("expected an unknown kid to be rejected")
	}
}

func TestVerify_AlgNoneRejected(t *testing.T) {
	// The classic algorithm-confusion attack: a token claiming alg:none (or
	// any algorithm outside WithValidMethods) must never verify, regardless
	// of what's in the JWKS.
	v, _ := newTestVerifier(t, "https://issuer.test")
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": "user-1",
		"iss": "https://issuer.test",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	raw, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(t.Context(), raw); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatal("expected alg:none to be rejected")
	}
}

func TestVerify_MalformedTokenRejected(t *testing.T) {
	v, _ := newTestVerifier(t, "https://issuer.test")
	if _, err := v.Verify(t.Context(), "not-a-jwt-at-all"); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatal("expected a malformed token to be rejected")
	}
}

func TestVerify_AudienceEnforcedWhenConfigured(t *testing.T) {
	key := genRSAKey(t)
	url := jwksServer(t, &key.PublicKey)
	v, err := NewVerifier(t.Context(), Config{JWKSURL: url, Issuer: "https://issuer.test", Audience: "storefront-web"})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	wrongAud := signRS256(t, key, testKID, jwt.MapClaims{
		"sub": "user-1", "iss": "https://issuer.test", "aud": "some-other-client",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(t.Context(), wrongAud); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatal("expected a mismatched audience to be rejected")
	}

	rightAud := signRS256(t, key, testKID, jwt.MapClaims{
		"sub": "user-1", "iss": "https://issuer.test", "aud": "storefront-web",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(t.Context(), rightAud); err != nil {
		t.Fatalf("expected the matching audience to pass: %v", err)
	}
}
