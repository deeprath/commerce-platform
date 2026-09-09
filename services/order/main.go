// Command order is the checkout saga orchestrator: OrderService (gRPC) plus a
// Kafka consumer that advances/compensates orders on payment.* / inventory.*.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	pricingv1 "github.com/deeprath/commerce-platform/gen/go/commerce/pricing/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/order/internal/consumer"
	"github.com/deeprath/commerce-platform/services/order/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/order/internal/saga"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
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

	svc := config.LoadService("order")
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

	conns := map[string]string{
		"cart":      config.String("CART_ADDR", "cart:50051"),
		"pricing":   config.String("PRICING_ADDR", "pricing:50051"),
		"inventory": config.String("INVENTORY_ADDR", "inventory:50051"),
		"payment":   config.String("PAYMENT_ADDR", "payment:50051"),
	}
	dialed := map[string]*grpc.ClientConn{}
	for name, addr := range conns {
		cc, derr := grpcx.Dial(addr)
		if derr != nil {
			return derr
		}
		defer func() { _ = cc.Close() }()
		dialed[name] = cc
	}

	orch := saga.New(st, saga.Clients{
		Cart:      cartv1.NewCartServiceClient(dialed["cart"]),
		Pricing:   pricingv1.NewPricingServiceClient(dialed["pricing"]),
		Inventory: inventoryv1.NewInventoryServiceClient(dialed["inventory"]),
		Payment:   paymentv1.NewPaymentServiceClient(dialed["payment"]),
	})

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

	cons, err := kafka.NewConsumer("order-saga", consumer.Topics(), consumer.Handler(orch),
		config.String("KAFKA_BROKERS", "kafka:9092"))
	if err != nil {
		return err
	}

	// Checkout is authenticated; the saga forwards the caller's token downstream.
	srv := grpcx.NewServer(grpcx.WithAuth(verifier,
		"/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch"))
	orderv1.RegisterOrderServiceServer(srv, grpcsvc.New(orch, st))
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
