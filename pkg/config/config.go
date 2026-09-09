// Package config reads configuration from the environment. Twelve-factor:
// no config files at runtime, everything is an env var, secrets are mounted
// as env or files by the platform (External Secrets Operator in k8s).
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// String returns the env var or def if unset/empty.
func String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// MustString returns the env var or panics. Use only at startup.
func MustString(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env var %s is not set", key))
	}
	return v
}

// Int returns the env var parsed as int, or def.
func Int(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		panic(fmt.Sprintf("env var %s = %q is not an int: %v", key, v, err))
	}
	return n
}

// Bool returns the env var parsed as bool ("1","true","yes"), or def.
func Bool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "":
		return def
	case "1", "true", "TRUE", "yes", "on":
		return true
	default:
		return false
	}
}

// Duration returns the env var parsed as a Go duration, or def.
func Duration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		panic(fmt.Sprintf("env var %s = %q is not a duration: %v", key, v, err))
	}
	return d
}

// Service is the config every service needs. Embed it in a service-specific struct.
type Service struct {
	Name        string // logical service name, e.g. "catalog"
	Environment string // "local" | "staging" | "prod"
	Version     string // build version / git sha
	GRPCAddr    string // listen address for the gRPC server
	OTLPEndpoint string // OpenTelemetry collector, host:port (no scheme)
	LogLevel    string // "debug" | "info" | "warn" | "error"
}

// LoadService reads the common service config from the environment.
func LoadService(name string) Service {
	return Service{
		Name:         name,
		Environment:  String("ENVIRONMENT", "local"),
		Version:      String("SERVICE_VERSION", "dev"),
		GRPCAddr:     String("GRPC_ADDR", ":50051"),
		OTLPEndpoint: String("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317"),
		LogLevel:     String("LOG_LEVEL", "info"),
	}
}
