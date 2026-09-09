// Command cart serves the Redis-backed shopping cart.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/cart/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/cart/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	svc := config.LoadService("cart")
	shutdown, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName: svc.Name, ServiceVersion: svc.Version,
		Environment: svc.Environment, OTLPEndpoint: svc.OTLPEndpoint, LogLevel: svc.LogLevel,
	})
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

	opt, err := redis.ParseURL(config.String("REDIS_URL", "redis://redis:6379/0"))
	if err != nil {
		return err
	}
	rdb := redis.NewClient(opt)
	defer func() { _ = rdb.Close() }()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}

	verifier, err := auth.NewVerifier(ctx, auth.Config{
		JWKSURL:  config.MustString("KEYCLOAK_JWKS_URL"),
		Issuer:   config.MustString("KEYCLOAK_ISSUER"),
		Audience: config.String("KEYCLOAK_AUDIENCE", ""),
	})
	if err != nil {
		return err
	}

	srv := grpcx.NewServer(
		grpcx.WithAuth(verifier, "/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch"),
		// A cart id is an opaque, unguessable token minted by the BFF; guest
		// carts have no principal. All cart RPCs are optional-auth.
		grpcx.WithOptionalAuthMethods(
			"/commerce.cart.v1.CartService/GetCart",
			"/commerce.cart.v1.CartService/AddItem",
			"/commerce.cart.v1.CartService/SetItemQuantity",
			"/commerce.cart.v1.CartService/RemoveItem",
			"/commerce.cart.v1.CartService/Clear",
			"/commerce.cart.v1.CartService/Merge",
		),
	)
	cartv1.RegisterCartServiceServer(srv, grpcsvc.New(store.New(rdb, config.Duration("CART_TTL", 0))))
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	if svc.Environment == "local" {
		reflection.Register(srv)
	}
	return grpcx.Serve(ctx, srv, svc.GRPCAddr)
}
