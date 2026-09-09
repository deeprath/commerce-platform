// Command fulfillment turns confirmed orders into shipments and runs a SANDBOX
// carrier that advances them PENDING -> SHIPPED -> DELIVERED. It exposes
// FulfillmentService (gRPC), consumes commerce.order.confirmed, and relays its
// own commerce.fulfillment.* events via the transactional outbox.
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

	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/carrier"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/consumer"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/store"
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

	svc := config.LoadService("fulfillment")
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

	cons, err := kafka.NewConsumer("fulfillment", consumer.Topics(), consumer.Handler(st), brokers)
	if err != nil {
		return err
	}

	adv := carrier.New(st,
		config.Duration("CARRIER_SWEEP_INTERVAL", 10*time.Second),
		config.Duration("CARRIER_PENDING_AFTER", 20*time.Second),
		config.Duration("CARRIER_SHIPPED_AFTER", 40*time.Second),
	)

	srv := grpcx.NewServer(grpcx.WithAuth(verifier,
		"/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch"))
	fulfillmentv1.RegisterFulfillmentServiceServer(srv, grpcsvc.New(st))
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	if svc.Environment == "local" {
		reflection.Register(srv)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return relay.Run(gctx) })
	g.Go(func() error { return cons.Run(gctx) })
	g.Go(func() error { return adv.Run(gctx) })
	g.Go(func() error { return grpcx.Serve(gctx, srv, svc.GRPCAddr) })
	return g.Wait()
}
