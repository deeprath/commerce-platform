// Command media issues presigned upload URLs for the object store and, on
// confirmation, publishes commerce.media.asset_ready via the outbox.
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

	mediav1 "github.com/deeprath/commerce-platform/gen/go/commerce/media/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/media/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/media/internal/objstore"
	"github.com/deeprath/commerce-platform/services/media/internal/store"
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

	svc := config.LoadService("media")
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

	obj, err := objstore.New(objstore.Config{
		InternalEndpoint: config.MustString("MINIO_INTERNAL_ENDPOINT"),
		PublicEndpoint:   config.String("MINIO_PUBLIC_ENDPOINT", ""),
		AccessKey:        config.MustString("MINIO_ACCESS_KEY"),
		SecretKey:        config.MustString("MINIO_SECRET_KEY"),
		UseSSL:           config.Bool("MINIO_USE_SSL", false),
		PublicBaseURL:    config.String("MEDIA_PUBLIC_BASE_URL", ""),
	})
	if err != nil {
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

	producer, err := kafka.NewProducer(config.String("KAFKA_BROKERS", "kafka:9092"))
	if err != nil {
		return err
	}
	defer producer.Close()
	relay := kafka.NewOutboxRelay(pool, producer, 0, 0)

	srv := grpcx.NewServer(
		grpcx.WithAuth(verifier,
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
		),
		grpcx.WithOptionalAuthMethods("/commerce.media.v1.MediaService/GetAsset"),
	)
	mediav1.RegisterMediaServiceServer(srv, grpcsvc.New(store.New(pool), obj))
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	if svc.Environment == "local" {
		reflection.Register(srv)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return relay.Run(gctx) })
	g.Go(func() error { return grpcx.Serve(gctx, srv, svc.GRPCAddr) })
	return g.Wait()
}
