# Commerce Platform

A scalable, distributed e-commerce platform built as a **Go microservices monorepo** with a
**React + TypeScript** storefront and admin dashboard.

- **Internal comms:** gRPC (service-to-service), Kafka (async events / event backbone)
- **Mesh + edge:** Istio ambient mesh (`ztunnel` mTLS everywhere) + Istio ingress gateway (Envoy, Gateway API) + a thin Go BFF (Echo) for the browser
- **State:** PostgreSQL (per-service), Redis (cache/session/cart), OpenSearch (catalog search), MinIO (objects)
- **Identity:** Keycloak (OIDC) with a **custom-branded login**, realm/client roles for RBAC
- **Runtime:** Docker → Kubernetes → Helm, GitOps with Argo CD
- **Observability:** OpenTelemetry → Prometheus / Loki / Tempo, dashboards + SLO alerts in Grafana
- **Security in CI:** Gitleaks, Trivy, SonarCloud, OWASP ZAP (see [`docs/SECURITY.md`](docs/SECURITY.md))

Full design: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) · Every decision and why (incl. reversals): [`docs/DECISIONS.md`](docs/DECISIONS.md).

---

## Key decisions (the "suggest me" answers)

| Question | Decision | One-line why |
|---|---|---|
| **Go HTTP framework** | **Echo** at the edge/BFF only; **gRPC (grpc-go)** for everything internal | Echo is fast, `net/http`-compatible (works with OTEL, grpc-gateway), and actively maintained. Internal traffic never touches HTTP. |
| **Service mesh + gateway** | **Istio, ambient mode** + **Istio ingress gateway** (Envoy), configured via **Gateway API** | One Envoy data plane edge-to-internal: mTLS between all services with zero app code, per-request gRPC load balancing, native canary/mirroring, Gateway API portability, no OSS/Enterprise cliff. Rate-limit + shallow-auth become tiny services. **Chose Kong first, then reversed** once team-familiarity was ruled out — full reasoning in [DECISIONS.md ADR-011](docs/DECISIONS.md). Pairs with the BFF, doesn't replace it. |
| **Primary database** | **PostgreSQL 16**, one logical DB per service, `JSONB` for flexible product attributes | ACID for orders/payments/inventory, mature ops, logical replication + Debezium CDC for the outbox. Scale path: read replicas → Citus/CockroachDB if ever needed. |
| **Search** | **OpenSearch** | Faceted catalog filtering, autocomplete, relevance tuning — Postgres FTS won't carry a real catalog. |
| **Cache / ephemeral state** | **Redis** | Cart, sessions, rate-limit counters, idempotency keys, distributed locks. |
| **Event backbone** | **Kafka** | Order/inventory/pricing events, transactional outbox, DLQ, replay. |
| **Object storage** | **MinIO** (S3 API) | Product media, invoices, data exports, ZAP/Trivy report archive. |
| **Login UI** | **Custom React page → BFF brokers to Keycloak** (primary), or **custom Keycloak theme** (most standards-pure) | See [ARCHITECTURE.md §7](docs/ARCHITECTURE.md#7-identity-authn-and-rbac). |

### Why not the alternatives

- **Kong instead of Istio/Envoy at the edge** — Kong is more productive on day one (plugins
  vs. assembling a `ratelimit` + `ext-authz` service). It lost on: two proxy technologies in
  the stack once you run a mesh for mTLS, per-*connection* gRPC load balancing that starves
  new replicas, and the OSS→Enterprise cliff for WAF/OIDC. Trigger to revisit: if we ever
  drop the mesh. Full write-up: [DECISIONS.md ADR-011](docs/DECISIONS.md).
- **Sidecar instead of ambient mesh** — ambient drops the per-pod proxy (no injection
  webhook, no pod restarts to upgrade Envoy, ~no per-pod memory tax); L7 `waypoint` proxies
  are added only where policy is needed. [ADR-012](docs/DECISIONS.md).
- **Gin / Chi / Fiber instead of Echo** — Gin and Chi are both fine; Echo wins on
  batteries-included middleware without the weight of a full framework. **Avoid Fiber**: it's
  built on `fasthttp`, not `net/http`, which breaks grpc-gateway, standard OTEL HTTP
  instrumentation, and a lot of the ecosystem.
- **MongoDB instead of Postgres** — a catalog *looks* schemaless but orders, payments,
  inventory and ledgers are painfully relational. Postgres `JSONB` covers the flexible 20%
  without giving up transactions on the critical 80%.
- **RabbitMQ instead of Kafka** — Kafka's log/replay semantics and partitioned consumer
  groups are what make the outbox pattern, CDC, and analytics fan-out clean.

---

## Repository layout

```
commerce-platform/
├── go.work                     # multi-module workspace (one module per service + /pkg)
├── Taskfile.yml                # task runner (build, test, lint, proto, up, down)
├── buf.yaml / buf.gen.yaml     # protobuf lint + breaking-change detection + codegen
├── proto/                      # ← single source of truth for all gRPC contracts
│   └── commerce/<domain>/v1/*.proto
├── pkg/                        # shared Go libraries (NOT business logic)
│   ├── telemetry/              # OTEL setup: traces, metrics, logs
│   ├── auth/                   # JWKS cache + JWT verify + RBAC middleware (gRPC + HTTP)
│   ├── kafka/                  # producer/consumer wrappers, outbox relay, DLQ
│   ├── pgx/                    # pool setup, migrations runner, tx helpers
│   ├── grpcx/                  # interceptors: auth, logging, recovery, deadlines, app quotas
│   │                          #   (transport retries/outlier-detection = mesh, not here)
│   └── errs/                   # error taxonomy → gRPC status → HTTP status
├── services/
│   ├── bff/                    # Echo. HTTP/JSON ↔ gRPC. Owns the auth cookie + aggregation.
│   ├── ext-authz/             # tiny Envoy ext_authz: shallow "token present/valid" edge check
│   ├── identity/               # Keycloak broker, custom-login backend, RBAC source
│   ├── catalog/                # products, categories, media refs
│   ├── search/                 # OpenSearch indexer (Kafka consumer) + query API
│   ├── inventory/              # stock, reservations, warehouses
│   ├── cart/                   # Redis-backed, TTL'd
│   ├── pricing/                # prices, promotions, coupons, tax rules
│   ├── order/                  # checkout saga orchestrator + order lifecycle
│   ├── payment/                # PSP integration (Stripe/Adyen) — never stores PAN
│   ├── fulfillment/            # shipments, carrier integration, returns
│   ├── notification/           # email/SMS/push (Kafka consumer)
│   ├── review/                 # ratings & reviews + moderation
│   └── media/                  # MinIO uploads, image derivatives, virus scan
├── web/
│   ├── storefront/             # React + TS customer app (Vite)
│   ├── admin/                  # React + TS operator dashboard (Vite)
│   └── login/                  # custom login/registration/recovery SPA (or a Keycloak theme)
├── deploy/
│   ├── docker/                 # per-service multi-stage Dockerfiles, base images
│   ├── compose/                # docker-compose local stack (Envoy Gateway, no mesh)
│   ├── istio/                  # Gateway + HTTPRoutes, PeerAuthentication, AuthorizationPolicy,
│   │                          #   Telemetry, waypoint configs, ratelimit descriptors
│   └── helm/
│       ├── platform/           # app-of-apps: istio-base/istiod/cni/ztunnel, gateway,
│       │                       #   cert-manager, ESO, KEDA, observability stack
│       └── charts/<service>/   # one subchart per service
├── observability/
│   ├── grafana/dashboards/     # provisioned dashboard JSON
│   ├── prometheus/rules/       # recording + alerting rules, SLO burn-rate alerts
│   └── otel/collector.yaml
├── infra/
│   └── security/
│       ├── zap-scan.sh
│       ├── .gitleaks.toml
│       └── .trivyignore        # every entry dated + justified
└── .github/workflows/
    ├── ci.yml                  # lint · unit · integration · build · buf-breaking
    └── security.yml            # gitleaks · trivy · sonarcloud · zap
```

**Why a monorepo:** one PR can change a `.proto` and every consumer of it, atomically;
`buf` breaking-change detection runs across all services at once; shared `pkg/` has no
versioning ceremony. `go.work` keeps each service an independently buildable module.

---

## Getting started (local)

> Scaffolding is not generated yet — this section is the intended workflow.

```bash
task proto        # generate Go + TS stubs from proto/ via buf
task up           # docker-compose: all services + Postgres, Redis, Kafka, OpenSearch,
                  #   MinIO, Keycloak, and the Grafana/Prometheus/Loki/Tempo stack
task migrate      # run all service migrations
task seed         # demo catalog + a testuser
task test         # unit + integration (integration uses testcontainers)
```

Storefront: http://localhost:5173 · Admin: http://localhost:5174 · Grafana: http://localhost:3000
Keycloak: http://localhost:8080 · MinIO console: http://localhost:9001

Local login: `testuser` / `testuser123` (seeded in the Keycloak realm import).

> **Local ≠ prod on purpose:** compose runs Envoy Gateway with plaintext between containers.
> mTLS, `PeerAuthentication`, and `AuthorizationPolicy` exist only on Kubernetes (kind/k3d
> for a full local mesh test). See [ARCHITECTURE.md §8.7](docs/ARCHITECTURE.md).

---

## Docs

| File | What |
|---|---|
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | The blueprint — 14 sections, service topology + checkout-saga diagrams. |
| [`docs/DECISIONS.md`](docs/DECISIONS.md) | ADR log — every technology choice, its alternatives, its consequences, and any reversal. |
| [`docs/SECURITY.md`](docs/SECURITY.md) | Threat model, controls, the 4-tool CI scanning policy, and the "did it actually run" verification rule. |

## Status

Design phase. The three docs above are complete; service code, Istio/Helm manifests, and CI
pipelines are the next milestone (see ARCHITECTURE.md §14).
