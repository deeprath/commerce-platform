// Command bff is the storefront/admin backend-for-frontend: an Echo HTTP API in
// front of the catalog, media and search gRPC services. It owns the auth cookie
// and shapes per-view JSON.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/deeprath/commerce-platform/pkg/config"
	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/bff/internal/api"
	"github.com/deeprath/commerce-platform/services/bff/internal/auth"
	"github.com/deeprath/commerce-platform/services/bff/internal/clients"
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

	svc := config.LoadService("bff")
	shutdown, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName: svc.Name, ServiceVersion: svc.Version,
		Environment: svc.Environment, OTLPEndpoint: svc.OTLPEndpoint, LogLevel: svc.LogLevel,
	})
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

	cl, err := clients.Dial(clients.Targets{
		Catalog:     config.String("CATALOG_ADDR", "catalog:50051"),
		Media:       config.String("MEDIA_ADDR", "media:50051"),
		Search:      config.String("SEARCH_ADDR", "search:50051"),
		Cart:        config.String("CART_ADDR", "cart:50051"),
		Pricing:     config.String("PRICING_ADDR", "pricing:50051"),
		Order:       config.String("ORDER_ADDR", "order:50051"),
		Payment:     config.String("PAYMENT_ADDR", "payment:50051"),
		Fulfillment: config.String("FULFILLMENT_ADDR", "fulfillment:50051"),
		Review:      config.String("REVIEW_ADDR", "review:50051"),
	})
	if err != nil {
		return err
	}
	defer cl.Close()

	broker := auth.NewBroker(
		config.MustString("KEYCLOAK_TOKEN_URL"),
		config.String("KEYCLOAK_CLIENT_ID", "bff"),
		config.MustString("KEYCLOAK_CLIENT_SECRET"),
		config.Bool("COOKIE_SECURE", svc.Environment != "local"),
	)

	origins := strings.Split(config.String("CORS_ORIGINS", "http://localhost:5173,http://localhost:5174"), ",")
	e := api.New(cl, broker).Router(origins)

	srv := &http.Server{
		Addr:              config.String("HTTP_ADDR", ":8080"),
		Handler:           e,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()
	slog.Info("bff listening", slog.String("addr", srv.Addr))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
