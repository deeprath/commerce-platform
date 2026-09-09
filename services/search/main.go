// Command search maintains the OpenSearch product index from
// commerce.catalog.product_changed and serves SearchService.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	searchv1 "github.com/deeprath/commerce-platform/gen/go/commerce/search/v1"
	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/search/internal/consumer"
	"github.com/deeprath/commerce-platform/services/search/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/search/internal/index"
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

	svc := config.LoadService("search")
	shutdown, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName: svc.Name, ServiceVersion: svc.Version,
		Environment: svc.Environment, OTLPEndpoint: svc.OTLPEndpoint, LogLevel: svc.LogLevel,
	})
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

	idx, err := index.New(strings.Split(config.MustString("OPENSEARCH_ADDRESSES"), ","))
	if err != nil {
		return err
	}
	if err := idx.EnsureIndex(ctx); err != nil {
		return err
	}

	brokers := config.String("KAFKA_BROKERS", "kafka:9092")
	cons, err := kafka.NewConsumer(
		"search-indexer",
		[]string{kafka.Topic("catalog", "product_changed")},
		consumer.Handler(idx),
		brokers,
	)
	if err != nil {
		return err
	}

	srv := grpcx.NewServer() // browse is fully anonymous — no auth interceptor
	searchv1.RegisterSearchServiceServer(srv, grpcsvc.New(idx))
	healthgrpc.RegisterHealthServer(srv, health.NewServer())
	if svc.Environment == "local" {
		reflection.Register(srv)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return cons.Run(gctx) })
	g.Go(func() error { return grpcx.Serve(gctx, srv, svc.GRPCAddr) })
	return g.Wait()
}
