// Command analytics consumes the order.* / payment.* Kafka events and streams
// them, flattened, into ClickHouse for funnel + revenue analytics. Consumer-only
// (no gRPC API of its own); it runs a health server for the k8s probe.
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

	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/analytics/internal/clickhouse"
	"github.com/deeprath/commerce-platform/services/analytics/internal/consumer"
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

	svc := config.LoadService("analytics")
	shutdown, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName: svc.Name, ServiceVersion: svc.Version,
		Environment: svc.Environment, OTLPEndpoint: svc.OTLPEndpoint, LogLevel: svc.LogLevel,
	})
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

	sink, err := clickhouse.Open(ctx,
		config.MustString("CLICKHOUSE_DSN"),
		config.Int("ANALYTICS_BATCH_SIZE", 500),
	)
	if err != nil {
		return err
	}
	defer func() { _ = sink.Close(context.WithoutCancel(ctx)) }()

	brokers := config.String("KAFKA_BROKERS", "kafka:9092")
	cons, err := kafka.NewConsumer("analytics", consumer.Topics(), consumer.Handler(sink), brokers)
	if err != nil {
		return err
	}

	srv := grpcx.NewServer()
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	if svc.Environment == "local" {
		reflection.Register(srv)
	}

	flushEvery := config.Duration("ANALYTICS_FLUSH_INTERVAL", 5*time.Second)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return sink.RunFlusher(gctx, flushEvery) })
	g.Go(func() error { return cons.Run(gctx) })
	g.Go(func() error { return grpcx.Serve(gctx, srv, svc.GRPCAddr) })
	return g.Wait()
}
