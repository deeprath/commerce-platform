// Command inventory tracks stock levels and time-bounded reservations for the
// checkout saga, and publishes commerce.inventory.* events.
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

	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/inventory/internal/consumer"
	"github.com/deeprath/commerce-platform/services/inventory/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/inventory/internal/store"
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

	svc := config.LoadService("inventory")
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

	producer, err := kafka.NewProducer(config.String("KAFKA_BROKERS", "kafka:9092"))
	if err != nil {
		return err
	}
	defer producer.Close()
	relay := kafka.NewOutboxRelay(pool, producer, 0, 0)

	brokers := config.String("KAFKA_BROKERS", "kafka:9092")
	cons, err := kafka.NewConsumer(
		"inventory-catalog",
		[]string{kafka.Topic("catalog", "product_changed")},
		consumer.Handler(st, config.Int("DEFAULT_STOCK_ON_HAND", 100)),
		brokers,
	)
	if err != nil {
		return err
	}

	srv := grpcx.NewServer(
		grpcx.WithAuth(verifier, "/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch"),
		// Reserve/Commit/Release are called by the order saga carrying the
		// shopper's token; CheckAvailability is used for anonymous PDP badges.
		grpcx.WithOptionalAuthMethods(
			"/commerce.inventory.v1.InventoryService/CheckAvailability",
			"/commerce.inventory.v1.InventoryService/Reserve",
			"/commerce.inventory.v1.InventoryService/Commit",
			"/commerce.inventory.v1.InventoryService/Release",
		),
	)
	inventoryv1.RegisterInventoryServiceServer(srv, grpcsvc.New(st))
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	if svc.Environment == "local" {
		reflection.Register(srv)
	}

	sweepEvery := config.Duration("RESERVATION_SWEEP_INTERVAL", 15*time.Second)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return relay.Run(gctx) })
	g.Go(func() error { return cons.Run(gctx) })
	g.Go(func() error { return runSweeper(gctx, st, sweepEvery) })
	g.Go(func() error { return grpcx.Serve(gctx, srv, svc.GRPCAddr) })
	return g.Wait()
}

func runSweeper(ctx context.Context, st *store.Store, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n, err := st.ExpireDue(ctx, 100); err != nil {
				slog.ErrorContext(ctx, "reservation sweep failed", slog.Any("err", err))
			} else if n > 0 {
				slog.InfoContext(ctx, "reservations expired", slog.Int("count", n))
			}
		}
	}
}
