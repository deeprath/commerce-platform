// Command notification renders customer-facing lifecycle events (order.* and
// fulfillment.*) into notifications, dispatches them through a channel (sandbox:
// log only), and relays commerce.notification.sent via the transactional outbox.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	notificationv1 "github.com/deeprath/commerce-platform/gen/go/commerce/notification/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/notification/internal/channel"
	"github.com/deeprath/commerce-platform/services/notification/internal/consumer"
	"github.com/deeprath/commerce-platform/services/notification/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/notification/internal/store"
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

	svc := config.LoadService("notification")
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
	ch := channel.LogChannel{}

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

	cons, err := kafka.NewConsumer("notification", consumer.Topics(),
		consumer.Handler(consumer.Deps{Store: st, Channel: ch}), brokers)
	if err != nil {
		return err
	}

	srv := grpcx.NewServer(grpcx.WithAuth(verifier,
		"/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch"))
	notificationv1.RegisterNotificationServiceServer(srv, grpcsvc.New(st, ch))
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	if svc.Environment == "local" {
		reflection.Register(srv)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return relay.Run(gctx) })
	g.Go(func() error { return cons.Run(gctx) })
	g.Go(func() error { return grpcx.Serve(gctx, srv, svc.GRPCAddr) })
	return g.Wait()
}
