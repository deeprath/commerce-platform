// Command pricing prices cart lines: catalog unit prices + coupon + tax.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	pricingv1 "github.com/deeprath/commerce-platform/gen/go/commerce/pricing/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/pricing/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/pricing/internal/store"
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

	svc := config.LoadService("pricing")
	shutdown, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName: svc.Name, ServiceVersion: svc.Version,
		Environment: svc.Environment, OTLPEndpoint: svc.OTLPEndpoint, LogLevel: svc.LogLevel,
	})
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

	dsn := config.MustString("DATABASE_URL")
	if err := pgx.Migrate(ctx, dsn, store.Migrations, "migrations"); err != nil {
		return err
	}
	pool, err := pgx.NewPool(ctx, pgx.PoolConfig{DSN: dsn})
	if err != nil {
		return err
	}
	defer pool.Close()

	catalogConn, err := grpcx.Dial(config.String("CATALOG_ADDR", "catalog:50051"))
	if err != nil {
		return err
	}
	defer func() { _ = catalogConn.Close() }()

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
		// Quoting is called by the order saga (with the shopper's token) and by
		// the BFF for a live cart total; anonymous quoting is allowed.
		grpcx.WithOptionalAuthMethods(
			"/commerce.pricing.v1.PricingService/QuotePrice",
			"/commerce.pricing.v1.PricingService/ValidateCoupon",
		),
	)
	pricingv1.RegisterPricingServiceServer(srv, grpcsvc.New(
		store.New(pool),
		catalogv1.NewCatalogServiceClient(catalogConn),
		taxRatesFromEnv(),
	))
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	if svc.Environment == "local" {
		reflection.Register(srv)
	}
	return grpcx.Serve(ctx, srv, svc.GRPCAddr)
}

// TAX_RATE_BPS is the default; TAX_RATES_BY_COUNTRY is "US=0,DE=1900,GB=2000".
func taxRatesFromEnv() grpcsvc.TaxRates {
	tr := grpcsvc.TaxRates{
		Default: int32(config.Int("TAX_RATE_BPS", 800)),
		ByCode:  map[string]int32{},
	}
	for _, pair := range strings.Split(config.String("TAX_RATES_BY_COUNTRY", ""), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			tr.ByCode[strings.ToUpper(k)] = int32(n)
		}
	}
	return tr
}
