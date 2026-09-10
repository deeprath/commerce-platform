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

---

## ADR-018 — Fulfillment: event-carried shipment + a sandbox carrier

**Status:** Accepted (Phase 3).

**Context:** A confirmed order had no path to `FULFILLED` — the status existed in the
`order` aggregate but nothing produced it. Phase 3 adds a `fulfillment` service that turns
confirmed orders into shipments and drives them to delivery.

**Decision:**
- **One shipment per order in v1.** `shipments.order_id` is `UNIQUE`. Split shipments
  (per-warehouse, backorders) are a later change; modelling them now buys nothing while
  every order ships whole.
- **Event-carried state, no back-call.** `commerce.order.confirmed` is enriched with
  `ship_to` + `lines`; `fulfillment` builds the shipment straight from the event. The
  alternative — `fulfillment` calling `order.GetOrder` — needs either a service token or a
  not-owner-scoped internal RPC, and adds a synchronous dependency on `order` to a path
  that is otherwise pure Kafka.
- **Sandbox carrier = a background advancer.** `internal/carrier` sweeps on a ticker and
  moves shipments `PENDING → SHIPPED → DELIVERED` after configurable delays
  (`CARRIER_*_AFTER`), exactly like the reservation sweeper. Admin RPCs
  (`MarkShipped`/`MarkDelivered`/`CancelShipment`, gated on `order_manager`) allow manual
  control. Production deletes the advancer; a signature-verified carrier webhook drives the
  same transitions and emits the same `commerce.fulfillment.*` events.
- **Lifecycle close.** `fulfillment` emits `commerce.fulfillment.delivered`; `order`
  consumes it and moves `CONFIRMED → FULFILLED`, emitting `commerce.order.fulfilled` for
  downstream (notifications, analytics). Idempotent via `processed_events`; a cancelled
  order ignores a late delivery event.

**Alternatives:**
- *No `fulfillment` service, `order` self-fulfils on a timer* — puts carrier concerns and
  a second background loop inside the saga orchestrator; no shipment entity for the admin
  UI or the customer to track.
- *Synchronous `order.GetOrder` from `fulfillment`* — see above; couples the two services
  on the happy path.
- *`fulfillment` owns the `FULFILLED` transition by writing to `order`'s DB* — breaks
  database-per-service.

**Consequences:** `order.confirmed` payloads are larger (the line items travel twice — once
on `order.created`, once on `order.confirmed`). One more service, DB, and Kafka consumer
group. The `order` consumer now also subscribes to `commerce.fulfillment.delivered`.

---

## ADR-019 — Notification: event-driven, template registry, sandbox channel

**Status:** Accepted (Phase 3).

**Context:** Customers get no confirmation when an order is placed, confirmed, cancelled,
shipped, delivered, or fulfilled. Phase 3 adds a `notification` service.

**Decision:**
- **Consume the customer-facing lifecycle only.** `order.created / confirmed / cancelled /
  fulfilled` and `fulfillment.shipped / delivered` — every one of which carries an
  `owner_id`, so the recipient is known from the event alone. Payment events
  (`payment.failed`, `payment.refunded`) do **not** carry `owner_id`; a payment failure
  already surfaces to the customer as `order.cancelled` (reason `PAYMENT_FAILED:*`), and
  refund notifications are deferred until those events are enriched.
- **Template registry in `internal/domain`.** A `map[kind]tmpl` with `{placeholder}`
  substitution — no `text/template`, no files. Kinds are derived from the inbound topic.
  Rendering is a pure function, unit-tested for every kind.
- **`Channel` interface, sandbox `LogChannel`.** v1 records the notification and logs it;
  production swaps in SES / Twilio / a push gateway behind the same interface. A channel
  error marks the row `FAILED` but is **not** returned to the consumer — the event is still
  "handled" (the notification is a record of the attempt), so it is not redelivered
  forever. Retries/backoff belong in a real channel implementation.
- **Store writes the row + `commerce.notification.sent` outbox + `processed_events` in one
  tx.** Same idempotency pattern as the other services. `notification.sent` exists for
  analytics / an eventual digest, nothing consumes it yet.
- **`SendTest` requires authentication, not a role.** It only ever notifies the calling
  principal, so there is nothing to gate — the ARCHITECTURE table's "admin only" note
  predates the design. `ListNotifications` is owner-scoped.

**Alternatives:**
- *`text/template` + files* — more power than six one-line messages need; files to embed and
  test-load.
- *Return channel errors to the consumer (block the partition until delivery succeeds)* —
  one flaky provider call stalls every customer's notifications; a `FAILED` row + a real
  channel's own retry is the right seam.
- *Consume `payment.*` too* — needs `owner_id` on those events first; out of scope here.

**Consequences:** No delivery guarantee in v1 (the sandbox always "succeeds"; a real channel
failure is recorded, not retried by this service). One more service, DB, and consumer group.

---

## ADR-020 — Reviews: verified-purchase index from events, published-on-create

**Status:** Accepted (Phase 3).

**Context:** Product reviews need a "did this person actually buy it?" gate, aggregate
ratings for the PDP, and a moderation lever.

**Decision:**
- **Verified purchase = a confirmed order, learned from events.** The `review` service
  consumes `commerce.order.confirmed` (which carries the line items, per ADR-018) and keeps
  a `purchases (owner_id, product_id)` index. `CreateReview` checks that index; no
  synchronous call to `order`. "Confirmed" (paid), not "delivered", is the bar — matches the
  common "Verified Purchase" meaning and avoids enriching `order.fulfilled` with line items.
- **One review per `(product_id, author_id)`** — DB unique constraint → `ALREADY_EXISTS`.
  Editing a review is a later feature; re-review is not silently allowed.
- **Published on create.** v1 has no moderation queue: a review is `PUBLISHED` immediately
  and `commerce.review.published` is emitted. `ModerateReview` (role `catalog_manager`) can
  flip it to `HIDDEN` (emits `commerce.review.hidden`) or back. A pre-moderation queue is a
  policy change, not a schema change.
- **`GetRatingSummary` computes the aggregate on read** (`GROUP BY rating` over published
  rows) rather than maintaining a denormalised counter. Review volume per product is low;
  a materialised summary + its update path is not worth it yet.
- **`ListReviews` / `GetRatingSummary` are public** (`WithOptionalAuthMethods`); `CreateReview`
  needs auth; `ModerateReview` needs the role.
- **`author_name` is captured at write time** from the token's `name` claim (or the email
  local-part, or "Customer") — the service holds no user profile and does not call one.

**Alternatives:**
- *Call `order.ListOrders` to verify a purchase* — owner-scoped RPC, needs the caller's
  token forwarded, and couples the review write path to `order` availability.
- *Consume `order.fulfilled`* — stricter ("received it") but that event lacks line items;
  enriching it is out of scope here.
- *Maintain a denormalised rating counter* — premature; adds an update path and a
  consistency question for no measured benefit at this volume.
- *`search` consumes `review.published` to index `avg_rating`* — useful for "sort by
  rating", but expands scope into the search service; deferred. `review.published` is
  emitted so that wiring is a consumer-only change later.

**Consequences:** The rating summary is an aggregate query per PDP load (cheap at current
volume, revisit with a cache or a counter if it shows up in traces). No review editing in
v1. One more service, DB, and consumer group.

---

## ADR-021 — Returns (RMA) live in the order service; partial refunds in payment

**Status:** Accepted (Phase 3).

**Context:** A delivered order needs a return path: request → operator decision →
refund + restock. This spans `order`, `payment` and `inventory`.

**Decision:**
- **Returns are an order-service concern, not a new service.** A return is an aggregate
  hanging off an order (which order, which lines, how much to refund) and the decision
  orchestrates `payment.Refund` + `inventory.AdjustStock` — exactly the shape of the
  checkout saga, which already lives in `order` and already dials both. A separate
  `returns` service would re-dial the same two and re-fetch order data.
- **`RequestReturn`** loads the caller's order, requires `FULFILLED`, and for each line
  checks the requested quantity against `ordered − already-returned` (returns that are not
  `REJECTED` count). Per-line refund = the customer's share of what they actually paid
  (`order.total`, net of discount, incl. tax), allocated by running cumulative rounding so
  the parts sum exactly to the intended total — and to `order.total` for a whole-order
  return.
- **`DecideReturn`** (role `order_manager`) flips `REQUESTED → APPROVED|REJECTED` and emits
  the event in one transaction, then — on approval — calls `payment.Refund` (idempotency
  key = return id) and `inventory.AdjustStock(+qty)` per line. Restock is **not** idempotent
  on its own, so each `return_lines` row carries a `restocked` flag; a retry after a partial
  failure only re-attempts the unmarked lines. A refund/restock error is returned to the
  operator (the return is already `APPROVED`); calling `DecideReturn` again re-drives the
  unfinished side effects.
- **Payment gains partial, cumulative refunds.** `RefundRequest` takes an optional `amount`
  (0 ⇒ full remaining balance) and an `idempotency_key`. A `refunds` table records each one;
  `payments.refunded_cents` accumulates; status becomes `REFUNDED` only when fully refunded,
  otherwise stays `AUTHORIZED`. Each refund emits one `payment.refunded` for its own amount.
  `Transition()` no longer handles `REFUNDED` — refunds go through `Refund()`.

**Alternatives:**
- *A dedicated `returns` service* — duplicates the payment+inventory client wiring and needs
  a read path back into `order`; no ownership benefit while returns are simple.
- *Refund the whole payment on any return* — wrong for a partial return (1 of 3 items).
- *Do refund+restock inside the status-flip transaction* — impossible; they are external
  gRPC calls. The flag-per-line + operator-retry model is the pragmatic substitute for a
  distributed transaction, consistent with the rest of the saga.
- *Track "settled" on the return instead of per-line* — a mid-restock failure would then
  re-add already-restocked lines on retry.

**Consequences:** `order` now also exposes 4 return RPCs and emits 3 return events (on the
`commerce.order.*` topic namespace). Payment refunds are repeatable and no longer a single
state flip. No return UI yet — the RPCs are ready for the storefront/admin work.

---

## ADR-022 — Admin API: operator mode on existing list RPCs, not new services

**Status:** Accepted (Phase 3).

**Context:** The admin dashboard needs to see *every* customer's orders / returns /
shipments and to run the operator actions (decide a return, ship/deliver/cancel a
shipment). The customer-facing `List*` RPCs are owner-scoped.

**Decision:**
- **Overload the existing `List*` RPCs with an operator mode**, gated on the caller's role
  — the same pattern `fulfillment.GetShipment` already uses ("managers may read any
  shipment"). `order.ListOrders` / `order.ListReturns` / `fulfillment.ListShipments` gain
  optional `owner_id` / `status` filter fields that are **honoured only when the caller
  holds `order_manager`**; for everyone else the list stays scoped to `p.Subject` and the
  filters are ignored. `order.GetOrder` / `GetReturn` likewise return any record for an
  `order_manager`. No new RPCs, no new "admin" service.
- **The BFF gets an `/api/v1/admin/*` group** that forwards to these RPCs
  (`GET /admin/orders`, `/admin/orders/:id`, `/admin/returns`, `POST /admin/returns/:id/decide`,
  `GET /admin/shipments`, `POST /admin/shipments/:id/{ship,deliver,cancel}`). The BFF only
  checks that a token is present; **the services enforce the role** (defence in depth: the
  gateway will also restrict `/admin` by source IP, below).
- **BFF client set gains `Fulfillment` and `Review` connections**; `clients.Dial` was
  refactored from a straight-line list into a table so adding a service is one row.

**Alternatives:**
- *A dedicated admin/BFF-for-operators service* — a second aggregation layer and deploy for
  a handful of pass-through endpoints; the role gate already lives in the domain services.
- *New `ListAll*` / `AdminList*` RPCs* — doubles the surface and the store queries for the
  same data with a different filter; the role-gated filter fields are additive and smaller.
- *Enforce the admin role in the BFF* — the BFF would need its own JWKS verify + role check
  duplicating `pkg/auth`; keeping enforcement in the services means a mis-scoped BFF call
  still can't leak another customer's data.

**Consequences:** `List*` handlers now branch on role. The admin **SPA** and the gateway
**`HTTPRoute` + source-IP `AuthorizationPolicy`** for `/admin` are the remaining Phase 3
pieces (tracked in the roadmap); until then `/admin/*` is reachable on the dev BFF port and
protected only by the services' role check.

---

## ADR-023 — Admin console is its own SPA on its own hostname

**Status:** Accepted (Phase 3).

**Context:** Operators need a UI for the admin API (ADR-022): product CRUD, an order
browser, the returns queue, shipment actions.

**Decision:**
- **A separate SPA (`web/admin`), not a section of the storefront.** It has a different
  audience, a different threat model (privileged actions), and a different deploy target
  (`admin.commerce.example.com`, not `store.*`). It reuses the storefront's toolchain
  (Vite 7 / React 18 / react-router 7 / vitest), the same `/api` same-origin proxy so the
  httpOnly cookie works, and the same 401-drops-the-session pattern.
- **Two gates, layered.** The gateway `HTTPRoute` for `admin.*` routes only
  `/api/v1/admin/*` and is fronted by a source-IP `AuthorizationPolicy`
  (`admin-ip-allowlist`) that DENYs anything not from an operator CIDR. Behind that, the
  BFF forwards the token and every domain service still enforces the `order_manager` /
  `catalog_manager` role. Losing any one layer does not expose customer data.
- **No new BFF.** The admin SPA talks to the same BFF `/api/v1/admin/*` endpoints; the
  hostname split is purely at the gateway.

**Alternatives:**
- *An `/admin` route inside the storefront* — same origin and bundle as the customer app;
  the privileged surface would ship to every shopper's browser and share its CSP/cookie.
- *A dedicated operator BFF* — another deployable for endpoints that are already
  pass-throughs; the role check lives in the domain services regardless.

**Consequences:** A second frontend to build and deploy (added to the `web` CI matrix and
`build-images` / trivy). The IP allow-list CIDRs in `authorization-policy.yaml` are
placeholders that must be set per environment.

---

## ADR-024 — SLO alerting: multi-window multi-burn-rate on a request-availability SLO

**Status:** Accepted (Phase 4).

**Context:** The platform had dashboards but no alerting. We need pages that fire on
*user-visible* failure fast enough to matter, without paging on every transient blip, and
the same rules must work in both the compose stack and a real cluster.

**Decision:**
- **One SLO, per service: 99.5% of gRPC calls return a non-error status over a rolling
  30-day window** (error budget = 0.5%). "Error" is server-fault only — `OK`, `NOT_FOUND`,
  `INVALID_ARGUMENT`, `UNAUTHENTICATED`, `PERMISSION_DENIED`, `ALREADY_EXISTS`,
  `FAILED_PRECONDITION` are the client's problem and are excluded from the numerator.
  The signal is `rpc_server_call_duration_seconds_count` (already emitted by every service,
  labelled `job` / `rpc_method` / `rpc_response_status_code`) — no new instrumentation.
- **Recording rules** (`slo_recording`, 30s) precompute per-service request rate, error
  rate and error ratio at 5m / 30m / 1h / 6h so the alert expressions and the dashboard
  read cheap pre-aggregated series.
- **Two alerts, following the Google SRE workbook** (`slo_burn_rate_alerts`):
  - `SLOErrorBudgetFastBurn` — ratio > 14.4× budget over **1h AND 5m**, `for: 2m`,
    `severity: page`. Burns ~2% of the month's budget in an hour.
  - `SLOErrorBudgetSlowBurn` — ratio > 6× budget over **6h AND 30m**, `for: 15m`,
    `severity: ticket`.
  The long window sets sensitivity; the short window is the "still happening now"
  confirmation so an alert clears quickly once the burn stops.
- **`platform_alerts`** adds `ServiceServingNoTraffic` — a service that goes silent for
  10m while the rest of the platform serves (crash-loop, broken dial, stalled outbox
  relay on a producer).
- **Shipped twice from one source of truth:** `deploy/compose/prometheus/rules/slo.yml`
  for the compose stack (`rule_files:` + `--web.enable-lifecycle` reload), and
  `deploy/k8s/observability/prometheus-rules.yaml` — a `PrometheusRule` CR
  (`monitoring.coreos.com/v1`, `release: kube-prometheus-stack`) with the identical
  groups — for the cluster.
- **`checkout-funnel` Grafana dashboard** (the Phase 2 leftover): funnel rates
  (cart → quote → `CreateOrder` → `ConfirmPayment`), start-failure %, saga critical-path
  latency (`histogram_quantile` over `CreateOrder` buckets), saga dependency p95 from
  `order`'s client metrics, per-service error ratio vs the SLO threshold and a fast-burn
  factor, plus a returns/refunds panel.

**Alternatives:**
- *Alert on raw error rate / a static threshold* — pages on traffic spikes and on brief
  blips; says nothing about whether the budget is actually at risk.
- *Single-window burn-rate* — either slow to fire or slow to clear; the short
  confirmation window is what makes multi-window resolve fast.
- *Latency SLO too, now* — deferred; availability is the higher-signal first cut and the
  histograms are already recorded for when we add it.
- *Alertmanager routing / receivers* — out of scope here; the rules carry
  `severity: page|ticket` labels for whatever routing a given environment wires up.

**Consequences:** Two rule files to keep in sync (same group/rule names, checked by eye —
a lint could enforce it later). The error-status exclusion list is a policy choice baked
into the recording rules; revisit it if a service starts using one of those codes for a
server-side fault. Burn-rate multipliers and windows are the standard 2%/1h and 5%/6h
budget-spend figures for a 30-day window.

---

## ADR-025 — Load testing: k6 against the compose stack, checkout-funnel-shaped

**Status:** Accepted (Phase 2 deliverable, landed in Phase 4).

**Context:** The roadmap called for a k6 load test to feed capacity planning and
guard checkout-path latency. It needs to run somewhere reproducible, in CI, without
a standing staging environment.

**Decision:**
- **k6, scripted as the funnel** (`perf/checkout-funnel.js`): a `browse` scenario
  (`ramping-vus` — list / autocomplete / PDP) and a `checkout` scenario
  (`ramping-arrival-rate` — add to cart → `CreateOrder` → `ConfirmPayment`) run
  together. Arrival-rate for checkout so the offered load is a fixed rate, not a
  function of how fast the system responds.
- **Target the BFF directly (`:8088`), not the envoy edge.** The baseline measures
  the application; the edge's rate-limiter would cap the test and turn a perf run
  into a rate-limit test. `BASE_URL` points it at `:8080` when the edge *is* the
  thing under test.
- **Authenticate once in `setup()`; all checkout VUs share the bearer token.** The
  funnel models signed-in shoppers. Hammering `/auth/login` with one account just
  measures Keycloak's brute-force lockout — a separate concern, not this test.
- **Thresholds are the gate** (k6 exits non-zero on breach): `http_req_failed < 1%`,
  browse p95 < 400 ms, end-to-end checkout p95 < 2.5 s, checkout success > 95%.
  `409 INSUFFICIENT_STOCK` is a real funnel outcome (seeded stock is finite) and is
  excluded from the failure rate via `http.setResponseCallback`.
- **Runs in CI as its own workflow** (`.github/workflows/perf.yml`): `workflow_dispatch`
  (with VU / RPS / hold inputs) + weekly cron, an hour after the security run. Brings
  up the full compose stack on a clean volume (stock freshly seeded to 100/product),
  runs k6 in the `grafana/k6` container reaching the host via `host.docker.internal`
  (same pattern as `infra/security/zap-scan.sh`), uploads the JSON summary, tears down.
  Not on every PR — it needs the whole stack and minutes of wall time.
- **`task perf:load`** runs the same containerised k6 locally against `task up`.

**Alternatives:**
- *Vegeta / hey / wrk* — HTTP-rate tools without scenario modelling; the funnel is a
  multi-step stateful flow (cookies, an order id threaded into the confirm call).
- *Gatling / Locust* — heavier runtimes; k6 scripts are plain JS and the `grafana/k6`
  image keeps CI dependency-free.
- *Run on every PR* — minutes of stack bring-up per PR for a signal that moves slowly;
  weekly + on-demand is enough to catch regressions.
- *Hit the envoy edge by default* — see above; the edge rate-limit makes it a
  different test.

**Consequences:** The latency budgets in the thresholds are the current
contract — tighten them as the platform is tuned. The seeded-stock ceiling means a
deliberately long local run against a not-freshly-seeded stack will report
`checkout_out_of_stock`; that is expected, and CI always starts clean. k6's
`--summary-export` schema (Rate metrics expose `passes`/`fails`, not `rate`) is a
minor gotcha when post-processing `summary.json`.

---

## ADR-026 — DAST: authenticated ZAP active scan driven by a hand-kept OpenAPI file

**Status:** Accepted (Phase 4).

**Context:** Phase 0 shipped a passive ZAP baseline against the edge. It never
authenticated and never actively probed, so nothing behind sign-in — the whole
cart/checkout/orders surface — was tested. The roadmap called for the full
authenticated scan.

**Decision:**
- **ZAP Automation Framework plan** (`infra/security/zap/api-scan.yaml`), run by
  `infra/security/zap-scan.sh` as pass 2 after the existing passive baseline.
- **Endpoint list comes from a hand-maintained OpenAPI file**
  (`infra/security/openapi/bff.yaml`), *not* served by the BFF. The BFF has no
  spec endpoint and its HTML-less JSON responses give a spider nothing to
  follow, so without an explicit list an "active scan" would attack almost
  nothing. The file is small and kept in step with `api.go` by eye.
- **Auth is cookie replay, not a token in a header.** `POST /api/v1/auth/login`
  returns an httpOnly `access_token` cookie; ZAP's `sessionManagement: cookie`
  stores and replays it. The 401 body `SIGN_IN_REQUIRED` on `GET /orders` is the
  "logged out" poll signal, so ZAP re-authenticates when the session lapses
  mid-scan. This exercises the real browser flow rather than a side channel.
- **`/checkout` and `/checkout/confirm` are excluded from the active scan.** They
  drive the irreversible order saga (Kafka → payment / inventory / fulfillment);
  fuzzing them creates thousands of orders, drains seeded stock, and stalled an
  early run. They are thin BFF pass-throughs and still get passive coverage.
- **Gate = any HIGH fails** (the CI step parses every `reports/*.json` for
  riskcode 3). Three rules are downgraded to INFO by an `alertFilter` because
  they are artifacts of scanning the local compose stack over plain HTTP —
  `10049` (cache headers on envoy's own error bodies), `10106` ("HTTP Only
  Site" — TLS is at the k8s gateway), `10024` (the opaque `page_token` cursor
  matching a "sensitive param name" list). A fresh finding on any *other* rule,
  or HIGH on any rule, still fails. All three are logged in §8 of `SECURITY.md`.
- **Weekly + `workflow_dispatch`, never on PRs** — it needs the whole stack up
  and ~5–15 min. Runs against the compose edge locally / in CI and the real
  ingress gateway on staging.

**What the first authenticated run found (2026-09-10):**
- **`X-Content-Type-Options` missing** on every BFF response → added a
  `secureHeaders` middleware (`nosniff`, `X-Frame-Options: DENY`,
  `Referrer-Policy: no-referrer`, `Cache-Control: no-store`).
- **`GET /orders/{id}` returned 500** for a syntactically invalid id (Postgres
  `uuid` cast error). The order store now rejects a malformed id as `NotFound`
  before the query — a malformed id matches no row and 404 discloses nothing.
- **SQL Injection alert (HIGH) on `PUT /cart/items/{productId}`** — verified a
  **false positive**: the cart is Redis-backed (JSON blobs), no SQL anywhere.
  ZAP's boolean heuristic tripped on a stateful, input-reflecting endpoint that
  accepted *any* string as a line id and grew the cart between the `1=1` / `1=2`
  probes. The real (lower-severity) gap — no product-id validation — was closed:
  `cart` now rejects a non-UUID `product_id` with `InvalidArgument`, which also
  removes the boolean-diff the scanner keyed on. No global suppression of the
  SQLi rule.

**Alternatives:**
- *Spider-only, no spec* — finds nothing on a JSON API with no HTML.
- *Serve OpenAPI from the BFF* — a real feature, but adds a generated-spec
  pipeline and a public endpoint for what is only needed by the scanner; the
  hand-kept file is smaller and has no runtime surface.
- *Bearer token via a ZAP replacer rule* — simpler to wire, but skips the
  cookie/session code path that production browsers actually use.
- *Fail on MEDIUM too* — the local-HTTP artifacts are all MEDIUM/less; gating on
  HIGH + "any new rule" keeps signal without permanent suppressions.

**Consequences:** `bff.yaml` is a second description of the API surface to keep
current (a stale entry only means that path isn't scanned — fail-safe). The
admin surface (`/api/v1/admin/**`, operator role + source-IP gated) is not in
this scan; an operator-context pass is a follow-up.
