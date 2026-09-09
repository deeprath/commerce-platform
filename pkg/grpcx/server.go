// Package grpcx builds gRPC servers and clients with the platform's standard
// interceptor stack: panic recovery, OpenTelemetry, structured logging, and
// authoritative JWT auth. Transport concerns (retries, timeouts, outlier
// detection, mTLS) are the service mesh's job and are deliberately absent here.
package grpcx

import (
	"context"
	"log/slog"
	"net"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/deeprath/commerce-platform/pkg/auth"
)

// ServerOption configures NewServer.
type ServerOption func(*serverConfig)

type serverConfig struct {
	verifier    *auth.Verifier
	authSkip    map[string]bool
	extraUnary  []grpc.UnaryServerInterceptor
	extraStream []grpc.StreamServerInterceptor
}

// WithAuth enables authoritative JWT verification on every RPC except those in
// skipFullMethods (e.g. "/grpc.health.v1.Health/Check").
func WithAuth(v *auth.Verifier, skipFullMethods ...string) ServerOption {
	return func(c *serverConfig) {
		c.verifier = v
		c.authSkip = map[string]bool{}
		for _, m := range skipFullMethods {
			c.authSkip[m] = true
		}
	}
}

// WithUnaryInterceptor appends a custom unary interceptor after the standard stack.
func WithUnaryInterceptor(i grpc.UnaryServerInterceptor) ServerOption {
	return func(c *serverConfig) { c.extraUnary = append(c.extraUnary, i) }
}

// NewServer returns a *grpc.Server with the standard interceptor chain applied.
func NewServer(opts ...ServerOption) *grpc.Server {
	cfg := &serverConfig{}
	for _, o := range opts {
		o(cfg)
	}

	unary := []grpc.UnaryServerInterceptor{
		recoveryUnary(),
		loggingUnary(),
	}
	stream := []grpc.StreamServerInterceptor{
		recoveryStream(),
	}
	if cfg.verifier != nil {
		unary = append(unary, authUnary(cfg.verifier, cfg.authSkip))
	}
	unary = append(unary, cfg.extraUnary...)
	stream = append(stream, cfg.extraStream...)

	return grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
	)
}

// Serve listens on addr and blocks until the server stops or ctx is cancelled.
func Serve(ctx context.Context, s *grpc.Server, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		slog.Info("grpc server: graceful stop")
		s.GracefulStop()
	}()
	slog.Info("grpc server: listening", slog.String("addr", addr))
	return s.Serve(lis)
}

// Dial opens a client connection. The mesh provides mTLS, so the transport is
// "insecure" from gRPC's point of view. OpenTelemetry is always on.
func Dial(target string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	base := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	}
	return grpc.NewClient(target, append(base, opts...)...)
}
