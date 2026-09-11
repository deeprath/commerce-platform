// Package auth verifies Keycloak-issued JWTs against the realm JWKS and
// exposes the caller identity as a Principal. This is the AUTHORITATIVE check
// every service runs on every request. The ingress ext-authz service does a
// weaker structural-only check (ParseInsecure) and is never trusted for identity.
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

// Principal is the authenticated caller. Subject is the Keycloak "sub" claim and
// is used as owner_id for resource-level checks throughout the platform.
type Principal struct {
	Subject string
	Email   string
	Roles   []string // realm_access.roles
	Raw     jwt.MapClaims
}

// HasRole reports whether the principal holds role r.
func (p *Principal) HasRole(r string) bool {
	for _, have := range p.Roles {
		if have == r {
			return true
		}
	}
	return false
}

// HasAnyRole reports whether the principal holds at least one of rs.
func (p *Principal) HasAnyRole(rs ...string) bool {
	for _, r := range rs {
		if p.HasRole(r) {
			return true
		}
	}
	return false
}

// Config configures a Verifier.
type Config struct {
	// JWKSURL is the realm certs endpoint, e.g.
	// http://keycloak:8080/realms/commerce/protocol/openid-connect/certs
	JWKSURL string
	// Issuer must equal the token "iss" claim exactly (the browser-facing URL).
	Issuer string
	// Audience, if set, must appear in the token "aud".
	Audience string
	// Leeway tolerates small clock skew on exp/nbf/iat. Default 30s.
	Leeway time.Duration
}

// Verifier validates tokens against a live, self-refreshing JWKS.
type Verifier struct {
	cfg    Config
	jwks   keyfunc.Keyfunc
	parser *jwt.Parser
}

// NewVerifier builds a Verifier and starts background JWKS refresh.
func NewVerifier(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.JWKSURL == "" || cfg.Issuer == "" {
		return nil, fmt.Errorf("auth: JWKSURL and Issuer are required")
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = 30 * time.Second
	}
	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{cfg.JWKSURL})
	if err != nil {
		return nil, fmt.Errorf("auth: load JWKS: %w", err)
	}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithLeeway(cfg.Leeway),
		jwt.WithExpirationRequired(),
	}
	if cfg.Audience != "" {
		opts = append(opts, jwt.WithAudience(cfg.Audience))
	}
	return &Verifier{cfg: cfg, jwks: jwks, parser: jwt.NewParser(opts...)}, nil
}

// Verify parses and fully validates a raw bearer token (no "Bearer " prefix)
// and returns the Principal. All failures come back as errs.KindUnauthenticated.
func (v *Verifier) Verify(_ context.Context, raw string) (*Principal, error) {
	claims := jwt.MapClaims{}
	tok, err := v.parser.ParseWithClaims(raw, claims, v.jwks.Keyfunc)
	if err != nil || !tok.Valid {
		return nil, errs.Wrap(err, errs.KindUnauthenticated, "TOKEN_INVALID", "token failed verification")
	}
	p := &Principal{Raw: claims}
	if sub, _ := claims["sub"].(string); sub != "" {
		p.Subject = sub
	} else {
		return nil, errs.New(errs.KindUnauthenticated, "TOKEN_NO_SUBJECT", "token has no sub claim")
	}
	p.Email, _ = claims["email"].(string)
	p.Roles = extractRealmRoles(claims)
	return p, nil
}

func extractRealmRoles(claims jwt.MapClaims) []string {
	ra, ok := claims["realm_access"].(map[string]any)
	if !ok {
		return nil
	}
	rawRoles, ok := ra["roles"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(rawRoles))
	for _, r := range rawRoles {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// BearerFromAuthHeader strips the "Bearer " prefix (case-insensitive).
func BearerFromAuthHeader(h string) (string, bool) {
	const p = "bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):]), true
	}
	return "", false
}

// ParseInsecure decodes a token's claims WITHOUT verifying its signature and
// checks only that it is structurally a JWT (three base64url segments, a
// JSON header and a JSON claims payload) and not expired (with 60s leeway).
// This is what the ingress ext-authz uses to shed obviously-bad requests
// early. It MUST NOT be used to establish identity — services call Verify
// for that.
//
// This deliberately does not go through jwt-go's Parse/ParseUnverified: those
// names invite exactly the "looks like a verified parse" misuse this function
// exists to avoid (an option like WithValidMethods reads as if it restricts
// which algorithms are *accepted*, but ParseUnverified never checks a
// signature under any algorithm, so it does nothing here). A plain
// base64url+JSON decode makes the "no signature check happens, ever" property
// visible directly in the code instead of resting on a parser option that
// looks stronger than it is.
// notAJWTMsg is shared by every structural rejection in ParseInsecure below.
const notAJWTMsg = "not a JWT"

func ParseInsecure(raw string) (jwt.MapClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errs.New(errs.KindUnauthenticated, "TOKEN_MALFORMED", notAJWTMsg)
	}
	var header struct{}
	if err := decodeJWTSegment(parts[0], &header); err != nil {
		return nil, errs.Wrap(err, errs.KindUnauthenticated, "TOKEN_MALFORMED", notAJWTMsg)
	}
	claims := jwt.MapClaims{}
	if err := decodeJWTSegment(parts[1], &claims); err != nil {
		return nil, errs.Wrap(err, errs.KindUnauthenticated, "TOKEN_MALFORMED", notAJWTMsg)
	}
	if exp, err := claims.GetExpirationTime(); err == nil && exp != nil {
		if time.Now().Add(-60 * time.Second).After(exp.Time) {
			return nil, errs.New(errs.KindUnauthenticated, "TOKEN_EXPIRED", "token is expired")
		}
	}
	return claims, nil
}

// decodeJWTSegment base64url-decodes one dot-separated JWT segment and
// unmarshals it as JSON into v. Used only for the unverified structural check
// above — never for anything that trusts the decoded content.
func decodeJWTSegment(segment string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
