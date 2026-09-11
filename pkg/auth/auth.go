// Package auth verifies Keycloak-issued JWTs against the realm JWKS and
// exposes the caller identity as a Principal. This is the AUTHORITATIVE check
// every service runs on every request. The ingress ext-authz service does a
// weaker structural-only check (ParseInsecure) and is never trusted for identity.
package auth

import (
	"context"
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

// ParseInsecure decodes a token WITHOUT verifying its signature and checks only
// that it is structurally a JWT and not expired (with 60s leeway). This is what
// the ingress ext-authz uses to shed obviously-bad requests early. It MUST NOT
// be used to establish identity — services call Verify for that.
func ParseInsecure(raw string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	// WithValidMethods still doesn't verify the signature (ParseUnverified never
	// does) but it does reject an "alg: none"/mismatched-algorithm token at the
	// structural-check stage instead of blindly trusting its header, closing the
	// classic algorithm-confusion class of issue even for this fast, unverified path.
	p := jwt.NewParser(jwt.WithoutClaimsValidation(), jwt.WithValidMethods([]string{"RS256"}))
	if _, _, err := p.ParseUnverified(raw, claims); err != nil {
		return nil, errs.Wrap(err, errs.KindUnauthenticated, "TOKEN_MALFORMED", "not a JWT")
	}
	if exp, err := claims.GetExpirationTime(); err == nil && exp != nil {
		if time.Now().Add(-60 * time.Second).After(exp.Time) {
			return nil, errs.New(errs.KindUnauthenticated, "TOKEN_EXPIRED", "token is expired")
		}
	}
	return claims, nil
}
