# services/

One directory per bounded context (see `docs/ARCHITECTURE.md` §3). Each is its
own Go module so it builds and versions independently; `go.work` at the repo
root ties them together for local development.

## The shape every service follows

```
services/<name>/
├── go.mod                     # module github.com/deeprath/commerce-platform/services/<name>
│                              # replace github.com/deeprath/commerce-platform/pkg => ../../pkg
├── main.go                    # wire config -> telemetry -> pgx/kafka -> grpc server; start
├── internal/
│   ├── <name>/                # domain logic (no gRPC/DB types leak in here)
│   ├── grpc/                  # the generated *ServiceServer implementation (adapters)
│   ├── repo/                  # pgx-backed repositories; owner-scoped queries
│   └── events/                # Kafka producers/consumers, outbox wiring
└── migrations/                # goose *.sql, embedded and run on startup
```

Shared concerns come from `pkg/`: `telemetry`, `auth`, `grpcx`, `pgx`, `kafka`,
`errs`, `config`. Do not re-implement them per service.

## Building an image

`deploy/docker/<name>.Dockerfile`, context = repo root. It copies `go.work`,
`pkg/`, `gen/`, and `services/<name>/`, then `go build ./services/<name>`.

## Current services

| Service | Status |
|---|---|
| `ext-authz` | Phase 0 — implemented (ingress shallow pre-check) |
| everything else in `docs/ARCHITECTURE.md` §3 | Phase 1+ |
