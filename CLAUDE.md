# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go microservices e-commerce platform (portfolio/reference project): 16 Go services + 2 React/TypeScript
SPAs, gRPC internally, Kafka as the event backbone, Postgres per service, Redis/OpenSearch/MinIO/Keycloak/
OpenFGA for cache/search/objects/identity/fine-grained authz. A thin Go BFF (Echo) is the only thing the
browser talks to. Locally it all runs via `docker-compose`; the target production topology is Kubernetes +
Helm + Argo CD with an Istio ambient service mesh — **local intentionally does not run the mesh** (see
"Local ≠ production" below). Full design and every technology decision (with reasoning and reversals) are
in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) and [`docs/DECISIONS.md`](docs/DECISIONS.md) — read those
before proposing a different stack for something; the tradeoffs were already litigated there. Day-2 and
setup procedures are in [`docs/SETUP.md`](docs/SETUP.md) (local + production, step by step) and
[`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) (disaster recovery). [`docs/SECURITY.md`](docs/SECURITY.md) is the
threat model + CI scanning policy + real findings log.

**Repo is public.** Commit messages and PR descriptions end with the standard Claude Code attribution
(see any recent commit for the exact form) — don't ask, just append it.

## Commands

### Whole stack

```bash
task proto              # buf lint + format + generate (Go stubs + TS clients) — run after any proto/ change
task up                 # docker-compose: everything except OpenSearch's heavy sibling images
task up:full             # + OpenSearch (real, not profile-gated) + Schema Registry (profile-gated)
task down                 # stop, keep volumes
task down:v                # stop, wipe all data volumes (fixes stale Kafka-offset/Postgres desync — see below)
task logs -- <service>       # tail one service's logs
task seed                    # re-run the idempotent demo-catalog seeder
task build / task test / task test:unit / task vet / task lint   # across every module in go.work
```

`task test` needs Docker (testcontainers spin up real Postgres per integration test). `task test:unit`
(`go test -short`) skips them. See [`docs/SETUP.md`](docs/SETUP.md) for the full local walkthrough
(prerequisites, ports, first-run seeding, troubleshooting) and the production deployment runbook.

### One Go service (`cd services/<name>`)

```bash
go build ./...
go test ./...                          # integration tests use testcontainers; needs Docker, nothing else
go test ./... -short                   # unit only
go test ./internal/grpcsvc/... -run TestName -v
golangci-lint run ./...
```

Every service follows the same internal shape: `internal/domain` (aggregate + business rules, no I/O),
`internal/store` (Postgres, `migrations/` subdir, `goose`), `internal/grpcsvc` (the gRPC server, thin —
maps requests to domain/store calls), `main.go` (wiring). `pkg/` (shared, NOT business logic):
`auth` (JWKS verify + RBAC), `errs` (error taxonomy → gRPC/HTTP status), `grpcx` (interceptors), `kafka`
(producer/consumer + outbox relay), `pgx` (pool + migrations), `fga` (OpenFGA client, one canonical
`pkg/fga/model.json`), `telemetry` (OTEL), `config`.

### Frontend (`cd web/storefront` or `cd web/admin`)

```bash
npm install
npm run dev                              # storefront :5173, admin :5174 in dev mode — needs the BFF (task up) reachable
npm run test                              # vitest run
npm run test:coverage                      # + lcov, feeds SonarCloud
npm run lint && npm run typecheck             # eslint / tsc -b --noEmit
npm run build                                # tsc -b && vite build
```

Both apps use **jsdom**, not happy-dom, for component tests (`vite.config.ts`) — happy-dom doesn't dispatch
a form's `submit` event when its `type="submit"` button is clicked, which silently no-ops every form-submit
test. Login: `testuser`/`testuser123` (storefront/customer) via the realm import; admin needs an operator
role assigned in Keycloak (see `docs/SETUP.md`).

## Architecture — the non-obvious parts

### Local ≠ production, on purpose

`docker-compose` runs **plain Envoy** with a hand-written static config as the edge, and **no service
mesh** — containers talk plaintext on the compose network. Production is Kubernetes with Istio ambient
mode (`ztunnel` mTLS everywhere, `PeerAuthentication: STRICT`) plus the *same* Envoy build as a proper
Gateway-API-configured ingress gateway. The `HTTPRoute`/`Gateway` resources are portable between the two,
but mTLS, `AuthorizationPolicy`, and multi-zone locality failover only exist on the k8s side. Don't "fix"
compose to add a mesh — it's a deliberate, documented gap (ARCHITECTURE.md §8.7).

### The design doc vs. what actually got built

`docs/ARCHITECTURE.md`'s service table describes an `identity` service and a separate `web/login` SPA.
**Neither was built.** Login/session-brokering to Keycloak lives directly in `services/bff/internal/auth`
(`broker.go`) — the BFF does the Resource-Owner-Password grant, sets the httpOnly cookie, and refreshes —
and each React app (`web/storefront/src/pages/Login.tsx`, `web/admin/src/pages/Login.tsx`) has its own
plain login form. `docs/DECISIONS.md`'s ADR log and the roadmap table at the bottom of
`docs/ARCHITECTURE.md` §14 (checkmarks) are the accurate record of what's actually implemented — the
service table further up in the same file is the *original* design and has drifted in a few places like
this one. When in doubt, check `services/` and `web/` directly.

### The checkout saga — `order` is the orchestrator, not a coordinator service

`CreateOrder` synchronously calls `pricing.QuotePrice` → `inventory.Reserve` → `payment.CreatePayment`,
persisting saga state to its own Postgres *before* each side effect, so a crashed orchestrator resumes from
durable state. Confirmation and cancellation are driven by **Kafka events** consumed back into `order`
(`payment.authorized`, `payment.failed`, `inventory.reservation_expired`), never by the client polling a
sync call. There is no 2PC — every failure path is an explicit compensation (`Release` the reservation,
`Void` the payment), and every compensation call is itself idempotent and safe to double-run. Read
`services/order/internal/saga/saga.go` before touching any checkout-adjacent code; its tests
(`saga_test.go`) exercise every compensation branch against a real Postgres via testcontainers.

### The BFF is aggregation + the auth-cookie boundary, nothing else

`services/bff` has no database. React apps never hold a gRPC client and never call a domain service
directly — they only ever talk to the BFF's REST/JSON surface (`services/bff/internal/api/*.go` for the
full route list). A single BFF endpoint routinely fans out to 3–4 domain services concurrently and composes
one JSON response (a product-detail page = `catalog.GetProduct` + `pricing.QuotePrice` +
`review.ListReviews` + `inventory.CheckAvailability`). gRPC status → HTTP status mapping is centralized in
`pkg/errs`; don't hand-roll a new status mapping in a handler.

### Marketplace / multi-seller is layered onto everything else, additively

Sellers, shop staff, and per-shop product ownership are enforced via **OpenFGA** (`pkg/fga`), which is
strictly additive on top of Keycloak realm roles and `owner_id`-scoped repository queries — an OpenFGA
outage fails a delegated-access check closed, never open, and never blocks a resource's actual owner. One
canonical model (`pkg/fga/model.json`) is grown additively as new relation types are needed (order sharing,
shop staff, per-shop catalog manager) — don't create a second model file. `services/seller` owns the shop
aggregate; `services/payout` does 100%-passthrough per-shop payouts (no platform commission yet);
fulfillment groups a confirmed order's lines by `shop_id` into one shipment per shop, and the order only
reaches `FULFILLED` once every shop group has delivered (`order_shipment_deliveries` table).

### Environment quirks worth knowing before you hit them

- **Kafka has no persistent volume in compose.** A `task down` + `task up` (without `-v`) leaves Postgres's
  data intact but wipes Kafka's log, so `processed_events` dedup rows can collide with a re-created topic's
  reused low offsets. If consumers look stuck or events look "already processed" right after a restart,
  `task down:v && task up` (full wipe) is the fix, not a code investigation.
- **OpenFGA store bootstrap race.** Multiple services calling `pkg/fga.ensureStore` against a cold, empty
  OpenFGA instance at once can each create their own same-named store. Symptom: authz checks that should
  pass silently fail because different services are pointed at different store IDs. Fix: find the
  duplicates (`curl http://localhost:8083/stores`), delete the extras (`curl -X DELETE
  http://localhost:8083/stores/<id>`), restart the affected service containers.
- **SonarCloud's "New Code" period is `previous_version` mode, pinned to a fixed date**, not a rolling
  window — every commit since that date counts as "new code" for the quality gate, which is why a
  first-ever full-scope scan can surface dozens of issues in one batch rather than trickling in per-PR.
  This is a server-side project setting (SonarCloud → Administration → New Code) that needs
  account-owner access; it isn't something a PR can fix.
- **A SonarCloud PR can merge before every commit on its branch lands.** If you push a follow-up commit to
  an already-merged PR's branch, it's stranded — `git merge-base --is-ancestor <branch> origin/main` tells
  you whether that happened. The fix is a fresh branch off current `main` with the same commit
  cherry-picked, not force-pushing anything.

## Repository layout

```
go.work                    multi-module workspace: pkg + gen/go + one module per service
Taskfile.yml                task runner — `task --list` for everything
buf.yaml / buf.gen.yaml       protobuf lint + breaking-change detection + codegen
proto/commerce/<domain>/v1/     single source of truth for every gRPC contract
gen/go/                       generated Go stubs (buf generate — never hand-edit)
pkg/                        shared libs: auth, errs, grpcx, kafka, pgx, fga, telemetry, config
services/
  bff/                     Echo HTTP↔gRPC BFF — the only thing the browser talks to
  catalog/ media/ search/    products+media, uploads/derivatives, OpenSearch indexer+query
  cart/ pricing/ inventory/    Redis cart, pricing/promos/tax, stock+reservations
  order/                     checkout saga orchestrator, order lifecycle, returns/RMA, order sharing
  payment/ fulfillment/          PSP integration (never stores a PAN), shipments
  notification/ review/          templated notifications, ratings & reviews
  seller/ payout/                marketplace shop aggregate, per-shop payouts
  analytics/                    Kafka → ClickHouse OLAP sink
  ext-authz/                    tiny Envoy ext_authz shallow token pre-check (not authoritative)
web/
  storefront/ admin/            React+TS+Vite; storefront also has /seller/* (seller dashboard)
deploy/
  compose/                     local docker-compose stack (this is what `task up` runs)
  docker/                     one Dockerfile per service
  helm/{platform,commerce-services}/     k8s: app-of-apps + one templated service chart
  istio/                     mesh + gateway config (mTLS policy, AuthorizationPolicy, WAF)
observability/                 Grafana dashboards, Prometheus rules, OTEL collector config
infra/
  dr/                        disaster-recovery scripts (pg-backup.sh, verify-restore.sh)
  security/                  gitleaks/trivy/zap config + scan scripts
perf/                        k6 load tests (checkout-funnel.js)
docs/
  ARCHITECTURE.md              the blueprint — read §14 for the phase-by-phase build-out roadmap
  DECISIONS.md                  ADR log — every technology choice, alternatives, and any reversal
  SETUP.md                     ← local dev + production deployment, step by step
  RUNBOOKS.md                   ← disaster-recovery procedures, numbered, with a drill log
  SECURITY.md                    threat model, CI scanning policy, real findings
.github/workflows/
  ci.yml                      lint · unit · integration · build · buf-breaking
  security.yml                  gitleaks · trivy · sonarcloud · zap
  perf.yml                     weekly k6 load test
```
