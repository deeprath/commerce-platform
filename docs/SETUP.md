# Setup: local development and production deployment

Two runbooks in one file: **Part 1** takes a brand-new machine to a fully working local stack
(login, browse, checkout, admin console, seller dashboard). **Part 2** takes a bare Kubernetes
cluster to a live, monitored production deployment. Both are ordered, copy-pasteable command
sequences — if a step fails, the troubleshooting table at the end covers the failure modes
this repo actually hits.

Companion docs: [`ARCHITECTURE.md`](ARCHITECTURE.md) (the *why* behind every step here),
[`RUNBOOKS.md`](RUNBOOKS.md) (disaster recovery, once something is already running),
[`SECURITY.md`](SECURITY.md) (the CI scanning gates mentioned in Part 2).

---

## Part 1 — Local development, from a new machine

### 1.1 Prerequisites

Install these first; version numbers are what CI pins to, so matching them avoids
works-on-my-machine drift.

| Tool | Version | Check | Install |
|---|---|---|---|
| **Docker Desktop** (or Docker Engine + Compose v2) | any recent | `docker compose version` | https://docs.docker.com/get-docker/ |
| **Go** | 1.26.x | `go version` | https://go.dev/dl/ |
| **Node.js** | 22.x | `node -v` | https://nodejs.org (or `nvm install 22`) |
| **`task`** (Taskfile runner) | latest | `task --version` | `go install github.com/go-task/task/v3/cmd/task@latest` |
| **`buf`** (protobuf toolchain) | latest v2 | `buf --version` | `go install github.com/bufbuild/buf/cmd/buf@latest`, or `brew install bufbuild/buf/buf` |
| **`golangci-lint`** | v1.6x+ | `golangci-lint version` | https://golangci-lint.run/welcome/install/ |
| **git** | any recent | `git --version` | — |

Optional (only needed if you'll touch CI-parity security scanning locally, not for day-to-day dev):
`gitleaks`, the `trivy` CLI. See `task scan:secrets` / `task scan:deps` in `Taskfile.yml`.

**Resources:** the full stack (`task up:full`) pulls Postgres, Redis, Kafka, OpenSearch, MinIO,
Keycloak, ClickHouse, OpenFGA, and the Grafana/Prometheus/Loki/Tempo stack — budget **~6–8 GB
of RAM** for Docker Desktop and a few GB of disk for the first pull. `task up` (without `:full`)
skips Schema Registry, which is the only genuinely optional heavy image.

### 1.2 Clone and generate proto stubs

```bash
git clone https://github.com/deeprath/commerce-platform.git
cd commerce-platform

task proto          # buf lint + format + generate → gen/go (Go stubs) + web/*/src/gen (TS clients)
```

`gen/go` and the frontend generated clients are committed, so the app builds even if you skip
this step — but run it anyway after pulling any change that touched `proto/`, and always before
your first `go build` if you plan to modify a `.proto` file yourself.

### 1.3 Bring up the stack

```bash
task up              # first run: builds every service image + pulls infra images — several minutes
```

This does three things automatically: copies `deploy/compose/.env.example` → `.env` if it
doesn't exist yet (local-only defaults — never edit this file with real secrets), builds every
service's Docker image from `deploy/docker/*.Dockerfile`, and runs each service's Postgres
migrations on container startup (no separate migrate step). A one-shot `catalog-seed` container
also runs on every `up`, publishing a small demo catalog through the BFF's admin API — this is
what populates the storefront with anything to browse. Re-run it standalone any time with
`task seed`.

Watch it come up:

```bash
task logs                     # everything
task logs -- bff              # one service
docker compose -f deploy/compose/docker-compose.yml --env-file deploy/compose/.env ps
```

Wait until `keycloak`, `postgres`, `kafka`, and `minio` show `healthy` before expecting the
domain services to work — several of them `depends_on: condition: service_healthy` those and
will crash-loop briefly if you hit them before, which is harmless and self-resolves.

If you need OpenSearch-backed search results and haven't already, or want Schema Registry:

```bash
task up:full
```

### 1.4 What's running, and where

| URL | What | Login |
|---|---|---|
| http://localhost:5173 | Storefront (customer app) | `testuser` / `testuser123` |
| http://localhost:5174 | Admin dashboard (operator app) | needs an operator role — see 1.6 |
| http://localhost:8080 | Edge gateway (Envoy) — what the SPAs actually call | — |
| http://localhost:8088 | BFF direct (bypasses the gateway; handy for `curl`) | — |
| http://localhost:8081 | Keycloak admin console | `admin` / `admin` |
| http://localhost:3000 | Grafana (dashboards, Explore) | `admin` / `admin` |
| http://localhost:9001 | MinIO console | `minio` / `minio12345` |
| http://localhost:9090 | Prometheus | — |
| http://localhost:9200 | OpenSearch (raw API) | — |
| http://localhost:8086 | CDN stand-in (product-media reads) | — |

(Ports come straight from `deploy/compose/docker-compose.yml` / `deploy/compose/.env.example` —
if you've changed either, that's the source of truth, not this table.)

Open http://localhost:5173, sign in as `testuser` / `testuser123`, and you should see the seeded
demo catalog. That's the smoke test for "local dev is working."

### 1.5 Running the frontends against the live stack (hot reload)

The `storefront`/`admin` containers `task up` starts are **built** apps served on 5173/5174 — fine
for a smoke test, but you'll want Vite's dev server with HMR while actually changing frontend
code:

```bash
cd web/storefront   # or web/admin
npm install
npm run dev          # http://localhost:5173 (Vite dev server), proxies /api to the BFF
```

Stop (or don't start) the compose `storefront`/`admin` container for the app you're running this
way, to avoid a port clash — `docker compose -f deploy/compose/docker-compose.yml stop storefront`.

### 1.6 Giving a user the admin operator role

The seeded realm's `testuser` is a plain `customer`. To use the admin app, add an operator role
in the Keycloak admin console (http://localhost:8081, `admin`/`admin`):

1. **Users** → `testuser` (or **Add user** for a new one) → **Role mapping** → **Assign role**.
2. Pick `admin` for full operator access, or one of the narrower roles (`order_manager`,
   `catalog_manager`, `inventory_manager`, `pricing_manager`, `finance`, `csr`) — see
   [`ARCHITECTURE.md` §7.3](ARCHITECTURE.md#7-identity-authn-and-rbac) for what each grants.
3. Sign out and back in at http://localhost:5174 (a stale token won't carry the new role).

### 1.7 Running tests

```bash
task test           # everything: unit + integration (testcontainers spin up real Postgres — needs Docker, nothing else)
task test:unit         # fast path, no Docker (go test -short)
task vet
task lint            # golangci-lint across every module

# one service:
cd services/order && go test ./... -v
cd services/order && go test ./internal/saga/... -run TestCreateOrder_HappyPath -v

# frontend:
cd web/storefront && npm run test              # vitest run
cd web/storefront && npm run test:coverage        # + lcov (what SonarCloud consumes)
cd web/storefront && npm run typecheck && npm run lint
```

A first `task test` run is slow (every integration test in every service starts its own
Postgres testcontainer); subsequent runs reuse cached images and are much faster.

### 1.8 Stopping / resetting

```bash
task down             # stop, keep all data (Postgres volumes, MinIO objects, Kafka log)
task down:v            # stop AND wipe every volume — the full reset
```

**Reach for `task down:v` first**, not a code investigation, if anything looks like stale state
after a partial restart — Kafka has no persistent volume in compose while Postgres does (see
`CLAUDE.md`), so a plain `task down` + `task up` cycle is a legitimate source of a Kafka-offset /
`processed_events` mismatch that a full-wipe restart resolves instantly.

### 1.9 Optional: exercising the real mesh locally (kind/k3d)

Compose deliberately has no service mesh (`ARCHITECTURE.md` §8.7). To test the actual Istio
ambient + Gateway API + `AuthorizationPolicy` setup before it hits a shared cluster:

```bash
kind create cluster --name commerce-dev
# or: k3d cluster create commerce-dev

# Build + load every service image into the kind node (no registry needed for local-only testing)
for svc in bff catalog cart inventory order payment pricing fulfillment notification review \
           search media analytics seller payout ext-authz; do
  docker build -t "commerce/$svc:dev" -f "deploy/docker/$svc.Dockerfile" .
  kind load docker-image "commerce/$svc:dev" --name commerce-dev
done

helm upgrade --install platform deploy/helm/platform -n argocd --create-namespace \
  -f deploy/helm/platform/values.yaml   # bootstraps istio-base/istiod/cni/ztunnel, cert-manager,
                                          # External Secrets, KEDA, Argo Rollouts, OpenFGA, observability
helm upgrade --install commerce-services deploy/helm/commerce-services -n commerce --create-namespace \
  -f deploy/helm/commerce-services/values-dev.yaml   # 1 replica, side-loaded images, no PDB
```

This is a heavier, slower path than `task up` — only reach for it when you specifically need to
verify mTLS / `AuthorizationPolicy` / KEDA / canary behavior, not for day-to-day feature work.

---

## Part 2 — Production deployment

This is the **target production procedure** this repo's Helm charts and Istio manifests are
built for — run it against a real cluster and cloud account. It assumes: a Kubernetes cluster
(1.28+) with LoadBalancer support, a container registry, DNS you control, and (for the secrets
step) a HashiCorp Vault instance or equivalent. If any of those aren't provisioned yet, that
provisioning (Terraform, cloud console, whatever your org uses) is a prerequisite this doc
doesn't cover — everything below starts from "I have `kubectl` pointed at an empty cluster."

### 2.1 One-time cluster prerequisites

```bash
# 1. Argo CD itself (not part of the app-of-apps — it has to exist before the app-of-apps can use it)
kubectl create namespace argocd
kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
kubectl -n argocd rollout status deploy/argocd-server

# 2. Log in (default admin password is auto-generated on first install)
kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d
argocd login <argocd-server-address>
```

### 2.2 Bootstrap the platform (mesh, gateway, operators, observability)

`deploy/helm/platform` is an **app-of-apps**: one Argo CD `Application` per third-party chart
(Istio, cert-manager, External Secrets, KEDA, Argo Rollouts, OpenFGA, kube-prometheus-stack,
Loki, Tempo, the OTEL Collector) plus one `Application` for `deploy/helm/commerce-services`
itself (§2.4). Apply the app-of-apps once:

```bash
argocd app create commerce-platform \
  --repo https://github.com/deeprath/commerce-platform.git \
  --path deploy/helm/platform \
  --helm-values deploy/helm/platform/values-prod.yaml \
  --dest-server https://kubernetes.default.svc \
  --dest-namespace commerce \
  --sync-policy automated

argocd app sync commerce-platform
argocd app wait commerce-platform --health
```

Argo CD syncs the child `Application`s in the order Kubernetes lets it (CRDs and namespaces
first via `ServerSideApply` + `CreateNamespace=true`); watch it settle:

```bash
argocd app list                       # every child Application should reach Synced + Healthy
kubectl -n istio-system get pods       # istiod, ztunnel DaemonSet
kubectl get gatewayclass                # istio-provided GatewayClass
```

This step alone can take 10–15 minutes on a fresh cluster (CRDs, then controllers, then their
own pods becoming ready).

### 2.3 Configure the secrets backend

Nothing in `values-prod.yaml` or the rendered manifests contains a real secret — every service's
`DATABASE_URL`, `KEYCLOAK_CLIENT_SECRET`, MinIO keys, and `CLICKHOUSE_DSN` come from a Kubernetes
`Secret` that the **External Secrets Operator** (installed in §2.2) populates from Vault.

1. Point External Secrets at your Vault: create a `SecretStore` (or `ClusterSecretStore`) in the
   `commerce` namespace with your Vault address + auth method (Kubernetes auth is the common
   choice in-cluster).
2. Write the actual values into Vault at the paths your `ExternalSecret` resources reference —
   at minimum: per-service `<SERVICE>_DATABASE_URL` (from your managed Postgres instances —
   §2.5), `KEYCLOAK_CLIENT_SECRET` (from the realm you configure in §2.6), MinIO/S3 access keys,
   `CLICKHOUSE_DSN`, and `SECRET_ENCRYPTION_KEY` (Fernet/AES-GCM key for any at-rest app
   secrets).
3. Confirm sync: `kubectl -n commerce get externalsecret,secret` — each `ExternalSecret` should
   show `SecretSynced`.

Never put any of these in `values.yaml`, a Helm `--set`, or a committed file — that's exactly
what Gitleaks (§5.1 of `SECURITY.md`) and Trivy's secret scanner are there to catch if it
happens by accident.

### 2.4 Provision managed data stores

The Helm charts assume these already exist and are reachable — they are **not** provisioned by
`deploy/helm/`:

- **PostgreSQL** — one instance (or database) per service. Either a managed service (RDS,
  Cloud SQL, etc.) or a self-hosted **CloudNativePG** `Cluster` per service if you're running
  Postgres in-cluster. Either way, run each service's migrations once reachable — the
  Helm chart's `pre-upgrade`/`pre-install` Job (`goose up`) does this automatically on every
  deploy, so a first-time manual migration isn't required as long as the DB exists and the
  chart's `db:` flag for that service points at the right secret key.
- **Kafka** — 3 brokers minimum, `min.insync.replicas=2`, KRaft mode. A managed service or
  Strimzi in-cluster.
- **Redis** — cluster mode or a managed equivalent for cart/session/rate-limit state.
- **OpenSearch** — for the `search` service's index.
- **MinIO or S3** — `product-media` (public-read via the CDN), `invoices` (private,
  object-locked), `exports` (private, 7-day lifecycle), `security-reports`. Versioning on for
  all four.
- **ClickHouse** — for `analytics`.
- **Keycloak** — see §2.6.
- **OpenFGA** — installed by the platform app-of-apps (§2.2) but its **store + model** are
  provisioned out of band, not by the chart — run the model-load job/script against
  `pkg/fga/model.json` once the OpenFGA `Application` is healthy.

Wire each into the secrets backend (§2.3) before moving on — `commerce-services` will roll out
but every pod will crash-loop on a missing secret key otherwise.

### 2.5 Configure Keycloak for production

- Import the realm structure from `deploy/compose/keycloak/realm-commerce.json` as a starting
  point, then in the **production** realm: rotate every client secret, tighten token lifetimes
  if the local defaults (5 min access / 30 min sliding refresh) don't match your risk appetite,
  turn on the required actions (email verification, and OTP for admin-capable roles) that are
  optional in the dev realm import, and point `KEYCLOAK_ISSUER` at the public-facing hostname
  (browser-visible URL) while `KEYCLOAK_SERVER_URL`/JWKS fetch inside the cluster uses the
  internal service address — see `ARCHITECTURE.md` §7.1 for why these two URLs must differ.
- Create real users/groups mapped to the realm roles in `ARCHITECTURE.md` §7.3 — don't ship
  `testuser`/`testuser123` anywhere but local/staging.

### 2.6 Deploy `commerce-services`

If §2.2's app-of-apps already includes the `commerce-services` `Application` entry (it does, by
default, in `deploy/helm/platform/values.yaml`), it deploys automatically once its dependencies
(§2.3–2.5) are in place — nothing further to run. To deploy or update it directly instead:

```bash
helm template commerce-services deploy/helm/commerce-services \
  -f deploy/helm/commerce-services/values.yaml | kubeconform -strict -summary   # schema-check first

argocd app sync commerce-services       # or: helm upgrade --install (not the GitOps path — Argo CD should own this)
argocd app wait commerce-services --health
```

Each service gets its own `ServiceAccount` (the SPIFFE identity `AuthorizationPolicy` keys on),
`Deployment` (PSS `restricted`, native gRPC/HTTP health probes, `GOMEMLIMIT` from the memory
limit), `Service`, `PodDisruptionBudget`, `NetworkPolicy` pair, Istio `AuthorizationPolicy`, and
— where `autoscaling`/`rollout` are enabled in `values.yaml` — a KEDA `ScaledObject` and/or an
Argo `Rollout`. None of this needs hand-editing per service; it's all driven by the `services:`
map in `values.yaml` (see `deploy/helm/commerce-services/README.md` for every flag).

### 2.7 TLS and DNS

- `cert-manager` (installed in §2.2) issues the public-facing TLS certificate for the Istio
  ingress `Gateway` via a `Certificate` resource referencing your `ClusterIssuer` (Let's
  Encrypt or your CA).
- Point your DNS (`store.<domain>`, `admin.<domain>`, `auth.<domain>` for Keycloak) at the
  ingress gateway's `Service type=LoadBalancer` external IP/hostname.
- The `admin.*` `HTTPRoute` is additionally restricted by a source-IP `AuthorizationPolicy` —
  update its CIDR list for your office/VPN ranges before relying on it.

### 2.8 Post-deploy verification

```bash
# Every service healthy and passing its own probes:
kubectl -n commerce get pods
kubectl -n commerce get argo-rollouts   # if any Rollout is mid-canary

# Smoke test against the real ingress:
curl -sf https://store.<domain>/api/v1/healthz

# The two tools this repo already wires up for exactly this:
infra/security/zap-scan.sh                                          # edit its target host, or pass one — see the script
task perf:load -- -e BASE_URL=https://store.<domain>/api/v1           # override the compose-default BASE_URL (a later -e wins)
```

Grafana (via the `kube-prometheus-stack`/`loki`/`tempo` Applications from §2.2) is your
post-deploy dashboard — check the **Platform overview** and **Checkout funnel** dashboards for
error-rate and latency regressions before calling the deploy done.

### 2.9 Promotion flow and rollback

- **Staging** auto-syncs on every push to `main` (`values-staging.yaml`'s `syncPolicy.automated`).
- **Production** promotes by bumping `targetRevision` in `values-prod.yaml` from `main` to a
  release tag, then a **manual** `argocd app sync` — prod does not auto-sync
  (`prune: false`, no `selfHeal` surprise mid-business-day).
- **Rollback:** see [`RUNBOOKS.md` RB-7](RUNBOOKS.md#rb-7--roll-back-a-bad-deploy) — an Argo
  Rollouts canary aborts on its own `AnalysisRun` failure; anything else is
  `argocd app rollback <app> <previous-revision>` or `helm rollback`. Expand/contract migrations
  mean a Deployment rollback never needs a matching DB rollback.

### 2.10 CI/CD pipeline this all assumes

`.github/workflows/ci.yml` builds and pushes every image (tagged with the Git SHA, never
`latest`) on a merge to `main`, with an SBOM (`syft`) and a `cosign` signature attached.
`.github/workflows/security.yml` (Gitleaks, Trivy, SonarCloud, ZAP) gates the PR before it can
merge — see [`SECURITY.md`](SECURITY.md) for exact policy per tool. Argo CD then picks up the
new image tags from the manifests `ci.yml` updates in `deploy/helm/` on `main`. If your registry
or signing setup differs from what's in `ci.yml`, update that workflow — this doc assumes it's
authoritative for how images get built and named.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `task up` — a service crash-loops right after start | It came up before a dependency (`postgres`/`kafka`/`keycloak`) finished its healthcheck | Wait — `depends_on: condition: service_healthy` means it self-recovers once the dependency is ready. `task logs -- <svc>` to confirm it's retrying, not erroring. |
| Storefront/admin show no products | The `catalog-seed` one-shot container hasn't run yet, or errored | `task seed` to re-run it; `task logs -- catalog-seed` for why. |
| Consumers look "stuck" or an event seems ignored right after a restart | Kafka has no persistent volume in compose — a plain `task down`/`task up` cycle can desync `processed_events` dedup rows against reused low offsets | `task down:v && task up` (full wipe), not a code investigation. |
| An OpenFGA-gated action (order sharing, seller/staff features) silently fails that should succeed | Store-bootstrap race — multiple services created their own same-named OpenFGA store on a cold instance | `curl http://localhost:8083/stores` to find duplicates, `curl -X DELETE http://localhost:8083/stores/<id>` on the extras, restart the affected service containers. |
| Admin app: logged in but every action 403s | `testuser` (or your account) has no operator role yet | §1.6 — assign a role in the Keycloak admin console, then sign out/in. |
| `KEYCLOAK_SERVER_URL` / issuer mismatch errors | The internal JWKS-fetch URL and the token's browser-facing `iss` claim point at different hosts | They're deliberately two different settings (`ARCHITECTURE.md` §7.1) — internal traffic uses the in-cluster/compose service name, tokens must validate against the URL the browser actually used. |
| `task proto` fails | `buf` isn't installed, or a `.proto` change breaks an existing consumer | Install `buf` (§1.1); `task proto:breaking` shows exactly what broke against `main`. |
| SonarCloud gate fails on a PR with dozens of unrelated-looking issues | The project's "New Code" period is `previous_version` mode pinned to a fixed date, not a rolling window — everything since that date counts as new | Server-side SonarCloud project setting (Administration → New Code), needs account-owner access; not fixable from a PR. |
| A pushed follow-up commit doesn't show up in an already-merged PR | The branch's PR merged before the extra commit landed — it's stranded on an orphaned branch | `git merge-base --is-ancestor <branch> origin/main` confirms it; open a fresh branch off current `main` with the commit cherry-picked. Never force-push to "fix" this. |
| `commerce-services` pods crash-loop in production with a missing-env error | An `ExternalSecret` hasn't synced, or Vault is missing a key the chart expects | `kubectl -n commerce get externalsecret,secret` — fix the Vault path/auth, not the Deployment. |
