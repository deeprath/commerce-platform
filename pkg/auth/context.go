package auth

import "context"

type ctxKey struct{}

// WithPrincipal returns a child context carrying p.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the Principal placed by the auth interceptor/middleware,
// or nil if the request is unauthenticated.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}
