// Package checker holds the ingress pre-authentication decision. It is
// deliberately shallow: it proves a request carries a structurally valid,
// unexpired token and blocks obvious admin-route abuse. It NEVER verifies the
// token signature and is NEVER trusted for identity — every domain service
// re-verifies with pkg/auth. See docs/ARCHITECTURE.md §8.3.
package checker

import (
	"strings"

	"github.com/deeprath/commerce-platform/pkg/auth"
)

// Config lists the route prefixes that skip the token check and the ones that
// additionally require an admin role claim.
type Config struct {
	// PublicPrefixes are reachable with no token at all.
	PublicPrefixes []string
	// AdminPrefixes require an admin / platform_admin role claim (shallow check).
	AdminPrefixes []string
	// AdminRoles satisfy an AdminPrefix route.
	AdminRoles []string
}

// DefaultConfig is the Phase 0 policy.
func DefaultConfig() Config {
	return Config{
		PublicPrefixes: []string{
			"/healthz", "/readyz", "/metrics",
			"/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/register",
			"/api/v1/catalog", "/api/v1/search", // anonymous browse
		},
		AdminPrefixes: []string{"/api/v1/admin"},
		AdminRoles:    []string{"admin", "platform_admin"},
	}
}

// Decision is the outcome of a check.
type Decision struct {
	Allow  bool
	Status int    // HTTP status to return when Allow is false
	Reason string // short machine reason, surfaced as a response header
}

// Checker makes decisions from HTTP request attributes.
type Checker struct{ cfg Config }

// New returns a Checker that evaluates requests against cfg.
func New(cfg Config) *Checker { return &Checker{cfg: cfg} }

// Check evaluates one request. headers keys are expected lower-cased (Envoy does this).
func (c *Checker) Check(path string, headers map[string]string) Decision {
	if hasPrefix(path, c.cfg.PublicPrefixes) {
		return Decision{Allow: true, Reason: "PUBLIC_ROUTE"}
	}

	raw := bearerFromHeaders(headers)
	if raw == "" {
		return Decision{Allow: false, Status: 401, Reason: "NO_TOKEN"}
	}

	claims, err := auth.ParseInsecure(raw)
	if err != nil {
		return Decision{Allow: false, Status: 401, Reason: "TOKEN_REJECTED"}
	}

	if hasPrefix(path, c.cfg.AdminPrefixes) {
		roles := rolesFromClaims(claims)
		if !anyIn(roles, c.cfg.AdminRoles) {
			return Decision{Allow: false, Status: 403, Reason: "ADMIN_ROLE_REQUIRED"}
		}
	}
	return Decision{Allow: true, Reason: "TOKEN_PRESENT"}
}

func bearerFromHeaders(h map[string]string) string {
	if v := h["authorization"]; v != "" {
		if b, ok := auth.BearerFromAuthHeader(v); ok {
			return b
		}
	}
	// Fall back to the BFF's httpOnly cookie.
	if c := h["cookie"]; c != "" {
		for _, part := range strings.Split(c, ";") {
			part = strings.TrimSpace(part)
			if v, ok := strings.CutPrefix(part, "access_token="); ok {
				return v
			}
		}
	}
	return ""
}

func rolesFromClaims(claims map[string]any) []string {
	ra, ok := claims["realm_access"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := ra["roles"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func hasPrefix(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func anyIn(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}
