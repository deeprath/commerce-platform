# Decision log (ADRs)

Every significant technology and design choice, why it was made, what else was considered,
and what it costs us. Newest decisions can supersede older ones — when that happens the old
ADR stays, marked **Superseded**, with a pointer forward. Nothing is deleted; the reasoning
trail is the point.

Format per entry: **Status · Context · Decision · Alternatives · Consequences**.

| # | Decision | Status |
|---|---|---|
| [ADR-001](#adr-001--go-for-all-services) | Go for all services | Accepted |
| [ADR-002](#adr-002--echo-at-the-edge-only-grpc-internally) | Echo at the edge only, gRPC internally | Accepted |
| [ADR-003](#adr-003--postgresql-as-primary-store-database-per-service) | PostgreSQL primary, database-per-service | Accepted |
| [ADR-004](#adr-004--opensearch-for-catalog-search) | OpenSearch for catalog search | Accepted |
| [ADR-005](#adr-005--redis-for-cache-and-ephemeral-state) | Redis for cache / ephemeral state | Accepted |
| [ADR-006](#adr-006--kafka-as-the-event-backbone-transactional-outbox) | Kafka event backbone + transactional outbox | Accepted |
| [ADR-007](#adr-007--minio-for-object-storage) | MinIO for object storage | Accepted |
| [ADR-008](#adr-008--keycloak-as-idp-custom-login-not-the-default-page) | Keycloak IdP + custom login | Accepted |
| [ADR-009](#adr-009--rbac-keycloak-realm-roles--repository-owner-checks) | RBAC: realm roles + repo owner checks | Accepted |
| [ADR-010](#adr-010--orchestrated-saga-for-checkout) | Orchestrated saga for checkout | Accepted |
| [ADR-011](#adr-011--edge-gateway-istioenvoy-over-kong) | **Edge gateway: Istio/Envoy over Kong** | Accepted (reverses a prior call) |
| [ADR-012](#adr-012--istio-ambient-mode-over-sidecars) | Istio ambient mode over sidecars | Accepted |
| [ADR-013](#adr-013--keep-the-bff-alongside-the-gateway) | Keep the BFF alongside the gateway | Accepted |
| [ADR-014](#adr-014--observability-otel--prometheus--loki--tempo--grafana) | Observability: OTEL + Prom/Loki/Tempo/Grafana | Accepted |
| [ADR-015](#adr-015--kubernetes--helm--argo-cd-keda-for-event-driven-scaling) | K8s + Helm + Argo CD, KEDA for event scaling | Accepted |
| [ADR-016](#adr-016--security-scanning-gitleaks-trivy-sonarcloud-zap) | Security scanning: Gitleaks/Trivy/SonarCloud/ZAP | Accepted |
| [ADR-017](#adr-017--monorepo-with-gowork--buf) | Monorepo with `go.work` + `buf` | Accepted |

---

## ADR-001 — Go for all services

**Status:** Accepted.

**Context:** Need one backend language for ~13 services built by two people. Candidates:
Go, Java/Kotlin (Spring), Node/TypeScript, Rust.

**Decision:** Go (1.25+ toolchain, `GOTOOLCHAIN=auto`) for every service.

**Alternatives:**
- *Java/Spring* — richest e-commerce ecosystem, but heavy memory footprint per pod (bad for
  running 13 of them + autoscaling) and slow cold start.
- *Node/TS* — shares a language with the frontend, but weaker for CPU-bound work and the
  concurrency story is worse for a service doing lots of fan-out gRPC.
- *Rust* — best runtime profile, but slower to write and a smaller hiring/knowledge pool;
  overkill when Go's GC is not our bottleneck.

**Consequences:** Small static binaries → tiny distroless images, fast cold start (good for
HPA/KEDA), one shared `pkg/`. Generics are recent so some libraries still feel bare. No
default DI framework — we wire dependencies by hand (acceptable, and explicit).

---

## ADR-002 — Echo at the edge only, gRPC internally

**Status:** Accepted.

**Context:** Need an HTTP framework for the browser-facing surface and a service-to-service
protocol. User suggested Echo and asked whether it's the right pick.

**Decision:** **gRPC (grpc-go)** for all service-to-service calls. **Echo** only in the BFF
and in services with a genuine public HTTP surface (`payment` PSP webhooks). Internal
services expose no HTTP.

**Alternatives:**
- *Gin* — largest ecosystem, slightly dated API. Fine; no decisive edge over Echo.
- *Chi* — most `net/http`-idiomatic, lightest. Also fine; Echo chosen for batteries-included
  middleware (CORS, gzip, recovery, request-id, JWT) without being a heavy framework.
- *Fiber* — **rejected.** Built on `fasthttp`, not `net/http`, which breaks `otelhttp`,
  `grpc-gateway`, and much of the ecosystem.
- *REST between services instead of gRPC* — rejected: no typed contracts, no
  breaking-change detection, per-connection HTTP/1 load balancing, more boilerplate.

**Consequences:** The framework choice is low-stakes because the public HTTP surface is thin.
Swapping Echo for Chi later would touch only the BFF. Contracts live in `proto/` and
`buf breaking` gates every change.

---

## ADR-003 — PostgreSQL as primary store, database-per-service

**Status:** Accepted.

**Context:** User asked which database to use. The domain has strongly relational parts
(orders, payments, inventory reservations, promotions ledger) and loosely structured parts
(product attributes).

**Decision:** **PostgreSQL 16**, one logical database (or schema) per service, no
cross-service joins or shared tables. `JSONB` for flexible attributes. Partition `orders` /
`order_events` by month.

**Alternatives:**
- *MongoDB* — the catalog *looks* schemaless, but the money-handling core needs transactions
  and constraints. `JSONB` covers the flexible ~20% without giving those up.
- *MySQL* — fine, but Postgres's `JSONB`, partial indexes, `LISTEN/NOTIFY`, and logical
  replication (for Debezium CDC on the outbox) are worth more here.
- *CockroachDB now* — distributed-SQL scale-out we don't need yet; kept as the drop-in
  scale path precisely because the no-cross-service-join rule makes that migration local.

**Consequences:** One Postgres cluster with many DBs in dev; split `order`/`payment`/
`inventory` to their own clusters as load grows, with no app change. Eventual consistency
*between* services is handled by events + sagas (ADR-006, ADR-010).

---

## ADR-004 — OpenSearch for catalog search

**Status:** Accepted.

**Context:** Product browse/listing is the bulk of traffic and needs full-text, faceted
filtering, autocomplete, and relevance tuning.

**Decision:** **OpenSearch**, populated **only** by the `search` service consuming
`catalog.*`, `inventory.*`, `pricing.*` Kafka events. Never dual-written from request paths.

**Alternatives:**
- *Postgres full-text search* — fine for a blog, not for faceted catalog navigation at scale;
  would also put browse load on the transactional DB.
- *Elasticsearch* — functionally equivalent; OpenSearch chosen for the Apache-2.0 license.
- *Algolia / Typesense (hosted)* — great DX, but a third-party dependency and cost centre for
  a core capability we want to own.

**Consequences:** Search results can lag catalog writes by the event-propagation delay
(seconds) — acceptable for browse. The index is effectively a cache and can be rebuilt from
Kafka by replay.

---

## ADR-005 — Redis for cache and ephemeral state

**Status:** Accepted.

**Context:** Need caching, session lookups, cart storage, rate-limit counters, idempotency
keys, and distributed locks.

**Decision:** **Redis 7** for all of the above. Never a system of record for anything
durable — cart included (it has a TTL; a converted cart becomes an order in Postgres).

**Alternatives:**
- *Memcached* — cache only, no data structures, no persistence options; too narrow.
- *In-process caches only* — lose cross-pod consistency for rate limits and locks.

**Consequences:** Redis is on the critical path for cart and checkout; prod runs Redis
Cluster / a managed equivalent with failover. Cart loss on a total Redis outage is a
tolerated failure mode (annoying, not corrupting).

---

## ADR-006 — Kafka as the event backbone, transactional outbox

**Status:** Accepted.

**Context:** Services must react to each other's state changes without synchronous coupling;
analytics and search need a firehose; checkout needs reliable cross-service messaging.

**Decision:** **Kafka** (KRaft mode). Domain events as Protobuf messages in a Schema
Registry. **Transactional outbox**: services write domain rows + an `outbox` row in one
Postgres transaction; a relay (Debezium or a `pkg/kafka` poller) publishes them. Consumers
are idempotent; failures after N retries go to a per-topic DLQ.

**Alternatives:**
- *RabbitMQ* — good for work queues, but no log/replay, weaker for CDC and analytics
  fan-out, and consumer-group semantics are less suited to partitioned ordering by aggregate.
- *Dual-write (DB then publish)* — rejected: the classic lost-message / phantom-message bug.
- *Postgres-only via `LISTEN/NOTIFY`* — no durability, no replay, no fan-out at scale.

**Consequences:** Operational weight of running Kafka (Strimzi operator in non-prod, managed
in prod). Every consumer must be genuinely idempotent — enforced by a `processed_events`
table or Redis dedupe. Trace context is propagated in Kafka headers so async hops stay in
the same trace.

---

## ADR-007 — MinIO for object storage

**Status:** Accepted.

**Context:** Need to store product media, generated invoices, data exports, and archived
security-scan reports. User specified MinIO.

**Decision:** **MinIO** (S3 API). `media` service is the only writer. Clients upload via
**presigned PUT** URLs (size- and content-type-locked); downloads via presigned GET or CDN.
Buckets: `product-media` (CDN-fronted), `invoices`, `exports` (7-day lifecycle),
`security-reports`. Versioning on.

**Alternatives:**
- *Cloud-native (S3 / GCS) from day one* — ties local dev to a cloud account; MinIO gives
  the same API locally and in-cluster, and is swappable for real S3 in prod by config.
- *Store blobs in Postgres* — rejected; bloats the DB, kills backup/restore times.

**Consequences:** Media never transits gRPC or the gateway. An antivirus scan (ClamAV) and
derivative generation happen in `media` before `media.asset_ready` is emitted.

---

## ADR-008 — Keycloak as IdP, custom login (not the default page)

**Status:** Accepted.

**Context:** User wants Keycloak for auth/RBAC but a **custom login page**, not Keycloak's
stock screen.

**Decision:** Keycloak is the OIDC provider and user store. **Ship approach B first**
(custom Keycloak **theme** — your markup/branding, standard Authorization Code + PKCE, every
MFA/recovery/social flow for free). **Migrate to approach A** (custom React SPA → BFF brokers
credentials to Keycloak; tokens held only in an httpOnly `Secure` cookie by the BFF, never
in `localStorage`) if/when a same-origin SPA login experience is wanted. MFA under approach A
uses Keycloak Application-Initiated Actions rendered as React steps.

**Alternatives:**
- *Approach A first* — more UI control immediately, but you re-own reset / verify / OTP
  enrolment screens before auth is even solid. Slower path to correct.
- *ROPC / Direct Access Grants as the permanent design* — an OAuth anti-pattern for anything
  but first-party; we use it only inside approach A, brokered by a confidential client.
- *Auth0 / Cognito* — hosted, less to run, but a cost centre and less control over a core
  flow; Keycloak was specified.

**Consequences:** Approach B is a redirect to `/realms/commerce/...` with FreeMarker
templates. The BFF cookie-handling code (approach A) is the part to get right regardless and
is where token security lives.

---

## ADR-009 — RBAC: Keycloak realm roles + repository owner checks

**Status:** Accepted.

**Context:** Need role-based access for operators (catalog, inventory, pricing, orders,
finance, admin) and per-resource protection for customers.

**Decision:** Roles are **Keycloak realm roles**, assigned via **groups**, carried in the
JWT (`realm_access.roles`). `pkg/auth` provides a gRPC interceptor + Echo middleware that
verify the token against Keycloak **JWKS** and expose `RequireRole` / `RequireAnyRole`.
**Resource-level** ownership (`owner_id == principal.Subject`) is enforced in every
repository — never by role alone. Fine-grained relationship rules (OpenFGA/Casbin) are a
phase-2 slot behind the same interface.

**Alternatives:**
- *Roles only, no owner checks* — rejected; wouldn't stop "customer reads another customer's
  order".
- *OPA/OpenFGA from day one* — more moving parts than v1 needs; the interface leaves room.
- *Gateway does authz* — rejected as the authority; it does only a shallow pre-check
  (ADR-011 §8.3). Trust logic lives in one place: `pkg/auth`.

**Consequences:** Every protected RPC needs an explicit guard and, for customer-owned data,
an explicit owner filter — reviewed in code review as a checklist item.

---

## ADR-010 — Orchestrated saga for checkout

**Status:** Accepted.

**Context:** Checkout spans `cart`, `pricing`, `inventory`, `order`, `payment`,
`fulfillment` and must either fully complete or fully compensate.

**Decision:** **Orchestration** saga with `order` as the coordinator, holding an explicit
state machine in its own DB. Inventory reservations have a 15-minute TTL; every step is
idempotent and retried with backoff; terminal failure → `CANCELLED` + customer notified.
Compensations, not distributed transactions. No 2PC.

**Alternatives:**
- *Choreography (services react to each other's events with no coordinator)* — harder to
  reason about, no single place to see "where is this order stuck", compensation logic
  scattered.
- *2PC / XA* — poor availability, poor scaling, not supported across our mix of stores.

**Consequences:** `order` carries orchestration complexity, but it's contained and
inspectable. A crashed orchestrator resumes from the last persisted state. Every
participant must expose compensating operations (`Release`, `Refund`, `void`).

---

## ADR-011 — Edge gateway: Istio/Envoy over Kong

**Status:** Accepted. **Reverses** the earlier "use Kong" decision taken the same day.

**Context & history:**
1. First architecture draft listed "Envoy (via Contour/Emissary) **or** Kong" as the edge
   gateway, leaning Envoy as a mesh-centric default without a strong argument.
2. User asked to **use Kong** (it's the gateway in their other project). The docs were fully
   rewritten to Kong Gateway OSS via Kong Ingress Controller, DB-less, with the plugin list
   and a shallow-edge-auth split.
3. User then asked for the honest case *for* Envoy, and finally asked for the pick **with
   team familiarity explicitly removed** — "just you and me on this project".
4. With operational familiarity off the table, the technical balance favours Envoy/Istio.

**Decision:** The **Istio ingress gateway** (Envoy), configured via **Gateway API**, is the
single north-south entry point. Rate limiting is a dedicated `envoyproxy/ratelimit` + Redis
service; the shallow edge auth-check is a small `ext-authz` service (or folded into the BFF).
This pairs with — does not replace — the BFF (ADR-013) and the ambient mesh (ADR-012).

**Why Envoy/Istio wins here:**
- **One data plane edge-to-internal.** mTLS between all services is already required (§4.2/
  §12); the clean implementation is a mesh, whose data plane is Envoy. Kong at the edge = a
  second proxy technology to learn, tune, patch, and observe. It also lets us **delete the
  SPIFFE/cert-loading code** from `pkg/grpcx`.
- **Per-request gRPC load balancing.** Kong/nginx balance per *connection*; long-lived
  HTTP/2 gRPC channels then leave freshly scaled-up replicas idle. Envoy balances per
  request. Real scaling-correctness issue with 13 services under HPA.
- **Native traffic management.** Weighted routing, traffic mirroring/shadowing, outlier
  detection, fault injection — config, not plugins, and not gated behind an Enterprise tier.
  Enables Argo Rollouts canary with automated SLO analysis.
- **Gateway API portability.** The same `Gateway`/`HTTPRoute` resources run under plain
  Envoy Gateway locally; no vendor CRDs.
- **No OSS/Enterprise cliff.** Kong OSS → Enterprise for WAF, the `openid-connect` plugin,
  and Admin-API RBAC. Envoy equivalents (Coraza filter, `ext_authz` + OPA) are open.

**Alternatives:**
- *Kong Gateway OSS + KIC* (the reverted choice) — most productive on day one: rate-limiting,
  CORS, proxy-cache, bot-detection are toggled plugins, not services to run. Best pick **if
  team familiarity counts** or if we ever drop the mesh. Full plugin design is preserved in
  git history (the commit before this ADR).
- *Envoy Gateway without a mesh* — edge only; keeps `pkg/grpcx` doing mTLS. Viable middle
  ground; rejected because we want the mesh's east-west mTLS + policy anyway.
- *Linkerd* — lighter mesh, but its proxy isn't Envoy (no edge unification) and its L7
  traffic-management / Gateway API story is thinner.

**Consequences (accepted costs):**
- Heavier to stand up: istiod + `ztunnel` DaemonSet + Istio CNI + gateway. More cluster
  moving parts; a misbehaving mesh is harder to debug than Kong.
- Rate-limit and `ext-authz` are small services **we** run and monitor.
- No built-in response cache — prod uses a CDN; the BFF has Redis read-through for anonymous
  browse.
- Local dev diverges: compose runs plain Envoy Gateway, no mesh, plaintext between
  containers (documented, §8.7 / README).

**Trigger to revisit:** if we ever remove the service mesh, re-evaluate Kong — without the
"one data plane" argument, its day-one productivity likely wins again.

---

## ADR-012 — Istio ambient mode over sidecars

**Status:** Accepted. Depends on ADR-011.

**Context:** Having chosen Istio, pick the data-plane model: per-pod sidecars or ambient
(`ztunnel` + optional `waypoint`).

**Decision:** **Ambient.** `ztunnel` (per-node DaemonSet) provides L4 + mTLS for every
meshed pod. `waypoint` proxies (L7: routing, retries, outlier detection, per-method
`AuthorizationPolicy`) are deployed **only** in `order`, `payment`, `inventory`.

**Alternatives:**
- *Sidecars* — the traditional model, most battle-tested, per-workload L7 everywhere. But:
  an injection webhook, a pod restart to upgrade Envoy, ~50–100 MB extra per pod × every
  replica, and startup ordering hazards with the app container. Historically the main reason
  teams avoided "just use Istio".
- *No mesh, `pkg/grpcx` mTLS* — see ADR-011 alternatives.

**Consequences:** Lower overhead and simpler upgrades than sidecars. Ambient is newer — some
edge-case features and third-party integrations still assume sidecars; we validate our
specific needs (mTLS, `AuthorizationPolicy`, `waypoint` retries, telemetry) in Phase 0. L7
features require a `waypoint` in that namespace — a deliberate, explicit opt-in.

---

## ADR-013 — Keep the BFF alongside the gateway

**Status:** Accepted.

**Context:** With a capable gateway (ADR-011), is a separate BFF still worth it?

**Decision:** Yes. The **gateway/mesh own infrastructure concerns** (TLS, routing, rate
limit, CORS, IP allow-list, edge metrics/tracing, canary, maintenance mode, mTLS). The
**BFF owns application concerns** the gateway cannot: terminating the auth cookie and
holding Keycloak tokens server-side, concurrent **aggregation** of multiple gRPC calls into
one view response, gRPC→HTTP error mapping, per-view response shaping.

**Alternatives:**
- *Gateway only, browser calls services* — pushes aggregation and token handling into
  JavaScript; tokens end up in the browser; chatty.
- *BFF only, no gateway* — every BFF re-implements TLS/rate-limit/CORS/observability; no
  single choke point; no mesh.

**Consequences:** One more hop (gateway → BFF → services) and one more deployable per client
family. Accepted for the security boundary (tokens never reach the browser) and the clean
seam.

---

## ADR-014 — Observability: OTEL + Prometheus + Loki + Tempo + Grafana

**Status:** Accepted.

**Context:** User asked for a monitoring tool and a proper dashboard.

**Decision:** **OpenTelemetry** SDK in every service (traces, metrics, logs), exported via
the **OTEL Collector** to **Prometheus** (metrics), **Loki** (logs), **Tempo** (traces),
all visualised in **Grafana**. Alertmanager on **SLO burn-rate**, not raw thresholds. Trace
context flows through gRPC interceptors **and** Kafka headers. Nine provisioned dashboards
incl. business boards (checkout funnel, revenue/orders).

**Alternatives:**
- *Datadog / New Relic (SaaS)* — less to run, excellent UX, but per-host/GB pricing on a
  13-service system gets expensive fast and it's a lock-in for a core capability.
- *ELK for logs + Prometheus + Jaeger* — three UIs; Grafana over Loki/Tempo/Prom is one.
- *Prometheus metrics only* — no traces means no way to follow a slow checkout across
  services.

**Consequences:** We run the LGTM stack (Helm: `kube-prometheus-stack` + Loki + Tempo).
Instrumentation is a shared `pkg/telemetry` so services get it for free. Istio `Telemetry`
adds the mesh hop to the same traces.

---

## ADR-015 — Kubernetes + Helm + Argo CD; KEDA for event-driven scaling

**Status:** Accepted.

**Context:** User asked for Docker, Kubernetes, Helm, and a scalable distributed-system
deployment.

**Decision:** Multi-stage Docker → distroless non-root images. Kubernetes with per-service
`Deployment`/`Service`/`ServiceAccount`, HPA on CPU + custom metrics, **KEDA** `ScaledObject`
on **Kafka consumer lag** for event-driven services, PDBs, `topologySpreadConstraints`.
**Helm umbrella chart** with a `commerce-common` library chart; `values-<env>.yaml` per
environment. **Argo CD** for GitOps (auto-sync staging, manual promote to prod). Migrations
run as Helm pre-upgrade Jobs with the expand/contract pattern for zero-downtime.

**Alternatives:**
- *Kustomize instead of Helm* — no templating/conditionals; painful across 13 similar
  services with per-env variance.
- *Flux instead of Argo CD* — equivalent; Argo chosen for its UI and `Rollouts` companion
  (ADR-011 canary story).
- *Plain `kubectl apply` in CI* — no drift detection, no single source of truth.

**Consequences:** A fresh cluster bootstraps mesh + platform components via an Argo
app-of-apps before any service. Every service chart is a thin `values.yaml` over the library
chart.

---

## ADR-016 — Security scanning: Gitleaks, Trivy, SonarCloud, ZAP

**Status:** Accepted. Follows the `security-scanning` skill.

**Context:** User asked for SonarQube, Trivy, OWASP ZAP, Gitleaks and "best practice".

**Decision:** All four in `.github/workflows/security.yml`:
- **Gitleaks** — every push + PR + weekly full-history; pre-commit hook. Any finding fails.
- **Trivy** — three scans, **different policies on purpose**: our-dependency (fs + image
  `--pkg-types library`) scans **block**; full-image base-OS and IaC/config scans are
  **informational** (upstream OS CVEs move on their own cadence). `.trivyignore` entries each
  dated + justified.
- **SonarCloud** — free because the repo is **public**; no server to run. Go + TS, coverage
  wired in, quality gate **on new code** blocks merge. (If the repo goes private later,
  switch to self-hosted SonarQube Community — SonarCloud charges for private repos.)
- **OWASP ZAP** — baseline scan (weekly + manual) against the live compose stack; full
  authenticated active scan weekly against staging. Config pre-empts the two traps from the
  skill: target `host.docker.internal` not `localhost` (or it scans nothing and still goes
  green), and a world-writable report dir (or the report never gets written).

**The rule (SECURITY.md §7):** a scanner that is "configured" but has never actually run for
real is worse than none. Each tool must be triggered once for real, its actual output read,
and real findings recorded — before Phase 0 is "done".

**Alternatives:**
- *Self-hosted SonarQube Community* — needed only if the repo becomes private; more to run
  (a server + its Postgres) for no gain while the repo is public.
- *Snyk / GitHub Advanced Security* — overlap Trivy/Sonar; extra cost; not requested.
- *Skip ZAP (SAST only)* — misses runtime issues (missing headers, auth-boundary bugs) that
  only appear against a running instance.

**Consequences:** CI has a security workflow that can block merges. Base-OS CVE noise is
deliberately non-blocking to avoid alert fatigue; base images are rebuilt weekly instead.

---

## ADR-017 — Monorepo with `go.work` + `buf`

**Status:** Accepted.

**Context:** User asked for a monorepo microservice architecture.

**Decision:** One repository. `go.work` workspace with one Go module per service plus
`pkg/`. All protobuf contracts in `proto/`, managed by **`buf`** (`buf lint`,
`buf breaking` against `main`, `buf generate` for Go + TS stubs). One `Taskfile.yml`.
Frontend apps in `web/` (pnpm workspace).

**Alternatives:**
- *Multi-repo (one per service)* — a `.proto` change becomes N coordinated PRs; shared
  `pkg/` needs versioning + release ceremony; cross-cutting changes are painful for a
  two-person team.
- *Single Go module for everything* — dependency hell; one service's dep bump forces a
  rebuild/redeploy of all.

**Consequences:** One PR can change a contract and every consumer atomically, with
`buf breaking` catching incompatibilities across all services at once. Repo is larger; CI
uses path filters and Go build caching to stay fast. Independent deployability is preserved
because each service is its own module and its own image.
