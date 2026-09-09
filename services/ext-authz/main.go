// Command ext-authz is the Envoy external-authorization service that runs on
// the ingress gateway. It performs the shallow pre-check described in
// docs/ARCHITECTURE.md §8.3 — token present, structurally valid, unexpired,
// admin routes gated — and nothing more. Identity is established downstream by
// each service via pkg/auth.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/ext-authz/internal/checker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	svc := config.LoadService("ext-authz")
	shutdown, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName:    svc.Name,
		ServiceVersion: svc.Version,
		Environment:    svc.Environment,
		OTLPEndpoint:   svc.OTLPEndpoint,
		LogLevel:       svc.LogLevel,
	})
	if err != nil {
		slog.Error("telemetry setup", slog.Any("err", err))
		os.Exit(1)
	}
	defer func() { _ = shutdown(context.Background()) }()

	srv := grpcx.NewServer()
	authv3.RegisterAuthorizationServer(srv, &server{chk: checker.New(checker.DefaultConfig())})
	healthgrpc.RegisterHealthServer(srv, health.NewServer())

	if err := grpcx.Serve(ctx, srv, svc.GRPCAddr); err != nil {
		slog.Error("server exited", slog.Any("err", err))
		os.Exit(1)
	}
}

type server struct {
	authv3.UnimplementedAuthorizationServer
	chk *checker.Checker
}

func (s *server) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	http := req.GetAttributes().GetRequest().GetHttp()
	d := s.chk.Check(http.GetPath(), http.GetHeaders())

	if d.Allow {
		slog.DebugContext(ctx, "allow", slog.String("path", http.GetPath()), slog.String("reason", d.Reason))
		return &authv3.CheckResponse{
			Status:       &rpcstatus.Status{Code: int32(codes.OK)},
			HttpResponse: &authv3.CheckResponse_OkResponse{OkResponse: &authv3.OkHttpResponse{}},
		}, nil
	}

	slog.InfoContext(ctx, "deny",
		slog.String("path", http.GetPath()),
		slog.Int("status", d.Status),
		slog.String("reason", d.Reason),
	)
	code := typev3.StatusCode_Unauthorized
	if d.Status == 403 {
		code = typev3.StatusCode_Forbidden
	}
	return &authv3.CheckResponse{
		Status: &rpcstatus.Status{Code: int32(codes.PermissionDenied)},
		HttpResponse: &authv3.CheckResponse_DeniedResponse{
			DeniedResponse: &authv3.DeniedHttpResponse{
				Status: &typev3.HttpStatus{Code: code},
				Headers: []*corev3.HeaderValueOption{{
					Header: &corev3.HeaderValue{Key: "x-ext-authz-reason", Value: d.Reason},
				}},
				Body: d.Reason,
			},
		},
	}, nil
}
