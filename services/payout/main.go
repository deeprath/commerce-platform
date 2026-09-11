// Command payout turns confirmed marketplace orders into per-shop payouts and
// runs a SANDBOX settlement sweep that advances them PENDING -> PAID. It
// exposes PayoutService (gRPC), consumes commerce.order.confirmed, and relays
// its own commerce.payout.* events via the transactional outbox.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	payoutv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payout/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/fga"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/payout/internal/consumer"
	"github.com/deeprath/commerce-platform/services/payout/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/payout/internal/settlement"
	"github.com/deeprath/commerce-platform/services/payout/internal/store"
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

	svc := config.LoadService("payout")
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
	st := store.New(pool)

	verifier, err := auth.NewVerifier(ctx, auth.Config{
		JWKSURL:  config.MustString("KEYCLOAK_JWKS_URL"),
		Issuer:   config.MustString("KEYCLOAK_ISSUER"),
		Audience: config.String("KEYCLOAK_AUDIENCE", ""),
	})
	if err != nil {
		return err
	}

	brokers := config.String("KAFKA_BROKERS", "kafka:9092")
	producer, err := kafka.NewProducer(brokers)
	if err != nil {
		return err
	}
	defer producer.Close()
	relay := kafka.NewOutboxRelay(pool, producer, 0, 0)

	cons, err := kafka.NewConsumer("payout", consumer.Topics(), consumer.Handler(st), brokers)
	if err != nil {
		return err
	}

	sweep := settlement.New(st,
		config.Duration("SETTLEMENT_SWEEP_INTERVAL", 10*time.Second),
		config.Duration("SETTLEMENT_PENDING_AFTER", 20*time.Second),
	)

	// A seller reading their own shop's payouts (OpenFGA). Optional: without an
	// endpoint, only the finance role can read payouts. See ADR-043.
	var fgaClient fga.API
	if apiURL := config.String("OPENFGA_API_URL", ""); apiURL != "" {
		c, ferr := fga.New(ctx, fga.Config{
			APIURL:    apiURL,
			StoreName: config.String("OPENFGA_STORE_NAME", fga.StoreName),
			Model:     fga.CommerceModel,
		})
		if ferr != nil {
			return ferr
		}
		slog.Info("openfga ready", slog.String("store", c.StoreID()), slog.String("model", c.ModelID()))
		fgaClient = c
	}

	srv := grpcx.NewServer(grpcx.WithAuth(verifier,
		"/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch"))
	payoutv1.RegisterPayoutServiceServer(srv, grpcsvc.New(st, fgaClient))
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	if svc.Environment == "local" {
		reflection.Register(srv)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return relay.Run(gctx) })
	g.Go(func() error { return cons.Run(gctx) })
	g.Go(func() error { return sweep.Run(gctx) })
	g.Go(func() error { return grpcx.Serve(gctx, srv, svc.GRPCAddr) })
	return g.Wait()
}
