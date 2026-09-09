package grpcx

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

func recoveryUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ctx, "panic in handler",
					slog.String("method", info.FullMethod),
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)
				err = status.Error(codes.Internal, "INTERNAL")
			}
		}()
		return handler(ctx, req)
	}
}

func recoveryStream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ss.Context(), "panic in stream handler",
					slog.String("method", info.FullMethod),
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)
				err = status.Error(codes.Internal, "INTERNAL")
			}
		}()
		return handler(srv, ss)
	}
}

// loggingUnary logs one line per RPC with latency and final code. It also
// normalises domain errors (errs.Error) onto gRPC statuses so handlers can
// just `return errs.New(...)`.
func loggingUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		if err != nil {
			origMsg := err.Error() // full detail: logged, never returned
			st := errs.ToStatus(err)
			err = st.Err()
			slog.ErrorContext(ctx, "rpc",
				slog.String("method", info.FullMethod),
				slog.String("code", st.Code().String()),
				slog.String("reason", st.Message()),
				slog.String("detail", origMsg),
				slog.Duration("took", time.Since(start)),
			)
			return resp, err
		}
		slog.InfoContext(ctx, "rpc",
			slog.String("method", info.FullMethod),
			slog.String("code", "OK"),
			slog.Duration("took", time.Since(start)),
		)
		return resp, nil
	}
}

// authUnary verifies the bearer token in the "authorization" metadata header
// and injects the Principal into the context.
//   - methods in skip bypass auth entirely (no token read)
//   - methods in optional verify+inject a token if present, but allow anonymous
//   - everything else requires a valid token
func authUnary(v *auth.Verifier, skip, optional map[string]bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if skip[info.FullMethod] {
			return handler(ctx, req)
		}
		md, _ := metadata.FromIncomingContext(ctx)
		var raw string
		if vals := md.Get("authorization"); len(vals) > 0 {
			if b, ok := auth.BearerFromAuthHeader(vals[0]); ok {
				raw = b
			}
		}
		if raw == "" {
			if optional[info.FullMethod] {
				return handler(ctx, req) // anonymous is allowed here
			}
			return nil, status.Error(codes.Unauthenticated, "NO_BEARER_TOKEN")
		}
		p, err := v.Verify(ctx, raw)
		if err != nil {
			return nil, errs.ToStatus(err).Err()
		}
		return handler(auth.WithPrincipal(ctx, p), req)
	}
}

// RequireRole returns an error unless the context principal holds role r.
// Call it at the top of a handler that needs a specific role.
func RequireRole(ctx context.Context, r string) error {
	p := auth.FromContext(ctx)
	if p == nil {
		return errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "no principal on context")
	}
	if !p.HasRole(r) {
		return errs.New(errs.KindPermissionDenied, "MISSING_ROLE", "caller lacks role "+r)
	}
	return nil
}

// RequireAnyRole returns an error unless the context principal holds one of rs.
func RequireAnyRole(ctx context.Context, rs ...string) error {
	p := auth.FromContext(ctx)
	if p == nil {
		return errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "no principal on context")
	}
	if !p.HasAnyRole(rs...) {
		return errs.New(errs.KindPermissionDenied, "MISSING_ROLE", "caller lacks a required role")
	}
	return nil
}
