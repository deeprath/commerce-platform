// Package telemetry wires OpenTelemetry for a service: OTLP/gRPC trace and
// metric export to the collector, W3C trace-context propagation, and a
// slog logger that stamps every line with the active trace_id/span_id.
//
// Logs are emitted as JSON to stdout and collected by the platform (Promtail/
// Grafana Alloy -> Loki in Kubernetes). Traces -> Tempo, metrics -> Prometheus,
// both via the collector.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Config controls telemetry setup.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	OTLPEndpoint   string // host:port, no scheme; insecure gRPC (collector is in-mesh)
	LogLevel       string
}

// Shutdown flushes and closes all exporters. Call it on service shutdown.
type Shutdown func(context.Context) error

// Setup initialises the global tracer/meter providers, the propagator, and the
// default slog logger. It returns a Shutdown that flushes exporters.
func Setup(ctx context.Context, cfg Config) (Shutdown, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.DeploymentEnvironmentKey.String(cfg.Environment),
		),
		resource.WithHost(),
		resource.WithProcessPID(),
	)
	if err != nil {
		return nil, err
	}

	traceExp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExp, sdktrace.WithBatchTimeout(2*time.Second)),
	)
	otel.SetTracerProvider(tp)

	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(10*time.Second))),
	)
	otel.SetMeterProvider(mp)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	slog.SetDefault(newLogger(cfg))

	return func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}, nil
}

func newLogger(cfg Config) *slog.Logger {
	var lvl slog.Level
	switch cfg.LogLevel {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(&traceHandler{inner: h}).With(
		slog.String("service", cfg.ServiceName),
		slog.String("version", cfg.ServiceVersion),
	)
}

// traceHandler injects trace_id/span_id from the context into every record.
type traceHandler struct{ inner slog.Handler }

func (h *traceHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &traceHandler{inner: h.inner.WithAttrs(as)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{inner: h.inner.WithGroup(name)}
}

// Tracer returns a named tracer from the global provider.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// Attr is a convenience re-export so callers don't import the attribute pkg.
func Attr(key, value string) attribute.KeyValue { return attribute.String(key, value) }
