# Security

Status: **design / not yet executed.** This document is the target security posture and the
CI scanning policy. Every "current findings" section below is a placeholder until the tool
has actually run once for real against this repo — see [§7](#7-the-verification-rule).

Companion to [`ARCHITECTURE.md`](ARCHITECTURE.md). Where they overlap, ARCHITECTURE.md is
the *what*, this is the *how it's enforced and verified*.

## Contents

1. [Threat model summary](#1-threat-model-summary)
2. [Application security controls](#2-application-security-controls)
3. [Infrastructure & runtime hardening](#3-infrastructure--runtime-hardening)
4. [Supply chain](#4-supply-chain)
5. [CI security scanning — the four tools](#5-ci-security-scanning--the-four-tools)
6. [Data protection & compliance](#6-data-protection--compliance)
7. [The verification rule](#7-the-verification-rule)
8. [Open items / accepted tradeoffs](#8-open-items--accepted-tradeoffs)

---

## 1. Threat model summary

| Asset | Primary threats | Where it's addressed |
|---|---|---|
| Customer PII (name, address, email, phone) | Exfiltration via app bug, over-broad admin access, log leakage | Owner-scoped repo queries; RBAC; PII encrypted/tokenized at rest; PII never logged; audit log on admin reads |
| Payment data | PAN capture/storage → PCI scope explosion | **PSP-hosted fields / tokenization — platform never sees a PAN.** SAQ-A scope. Webhooks signature-verified |
| Auth tokens | XSS theft, replay, privilege escalation | httpOnly `Secure` `SameSite` cookie held by BFF (not `localStorage`); 5-min access tokens; JWKS-verified at every service; RBAC + resource-owner checks |
| Order / inventory integrity | Race conditions, replay, negative-price coupons, oversell | Idempotency keys; saga with compensations; inventory reservations with TTL; pricing rules validated server-side |
| Internal network | Lateral movement after one pod compromise | ✅ Per-service default-deny NetworkPolicies (L3/L4) **and** per-service Istio `AuthorizationPolicy` (L7 SPIFFE identity, method-scoped for `payment`) — both generated from one `callers` call graph (`deploy/helm/commerce-services`, ADR-029); mesh mTLS between all services (`PeerAuthentication: STRICT`); per-service ServiceAccount; PSS `restricted` |
| Secrets | Leak via Git, image layers, env dumps | Gitleaks in CI + pre-commit; External Secrets Operator + Vault; nothing sensitive in `values.yaml` or images; at-rest app secrets AES-GCM encrypted |
| Supply chain | Malicious/vulnerable dependency, poisoned base image | Trivy (blocking on our deps); pinned versions; `go mod verify` / `buf` ; SBOM; signed images; Renovate |
| Public endpoints | Injection, DoS, scraping, header misconfig | Ingress-gateway rate limiting (`envoyproxy/ratelimit` + Redis) + `ext-authz` shallow check + Coraza (OWASP CRS) Envoy filter; ZAP passive baseline + **authenticated active API scan**; input validation at BFF + services |

---

## 2. Application security controls

- **Transport:** TLS 1.2+ at the Istio ingress gateway (cert-manager cert); **mTLS
  service-to-service via the Istio ambient mesh** — `ztunnel`, istiod-issued SPIFFE certs,
  `PeerAuthentication: STRICT` mesh-wide (no app TLS code, no SPIFFE loading in `pkg/grpcx`);
  Kafka SASL_SSL; Postgres/Redis/OpenSearch/MinIO all TLS + authenticated.
- **AuthN:** Keycloak OIDC. Access tokens 5 min, refresh short and sliding. Custom login via
  the BFF (see ARCHITECTURE.md §7.2) — tokens live only in an httpOnly cookie.
- **AuthZ:** `pkg/auth` verifies every JWT against Keycloak **JWKS** (cached, refreshed on
  `kid` miss), checks `iss`/`aud`/`exp`, extracts `realm_access.roles`. `RequireRole` guards
  per RPC/route. **Resource-level** ownership (`owner_id == principal.Subject`) enforced in
  every repository — never role-only. **Fine-grained (ReBAC):** an OpenFGA store
  (`pkg/fga`) holds relationship grants that don't fit roles — currently `order#viewer` for
  delegated order sharing. It is **strictly additive**: a `Check` runs only *after* the role
  gate and owner-scoping have already denied access, and any OpenFGA error there leaves the
  denial in place — delegated access **fails closed**, and a resource owner is never blocked
  by an OpenFGA outage. See [DECISIONS.md ADR-035](DECISIONS.md).
- **Input validation:** protobuf field constraints (`protovalidate`) on every gRPC request;
  BFF validates and normalizes before fan-out; parameterized SQL only (`pgx`, no string
  building); OpenSearch queries built via the typed client, never string concatenation.
- **Output encoding:** React escapes by default; no `dangerouslySetInnerHTML` without a
  sanitizer; JSON responses only (no server-rendered HTML from user data).
- **Rate limiting / abuse:** ingress-gateway global rate limit via `envoyproxy/ratelimit`
  (Redis, cluster-wide) — strict descriptors on `/auth/*`, moderate elsewhere; per-user
  limits at the BFF; per-method application quotas in `pkg/grpcx`; Keycloak brute-force
  detection on.
- **Idempotency:** every state-changing endpoint takes an `Idempotency-Key`; results cached
  24h in Redis.
- **Security headers (BFF + ingress gateway):** `Strict-Transport-Security`,
  `Content-Security-Policy` (`default-src 'self'`; `media-src 'self' blob:` for any audio;
  `connect-src` to the API origin only), `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `Referrer-Policy: strict-origin-when-cross-origin`,
  `Permissions-Policy` minimal. These fail silently if regressed — the **ZAP scan is the
  backstop** that catches a missing header against the real running stack. The BFF itself
  sets `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy:
  no-referrer` and `Cache-Control: no-store` on every response (`secureHeaders` middleware,
  asserted by `TestSecureHeaders_OnEveryResponse`); TLS/HSTS and CSP are set at the edge.
- **CORS:** one `HTTPRoute` CORS filter at the ingress gateway; explicit origin allow-list
  per environment; no wildcard with credentials.
- **Webhooks (payment):** HMAC signature verified before the body is parsed; replay window
  enforced; handler idempotent on the PSP event id.
- **Clickstream ingest (`POST /api/v1/events`):** the one **anonymous, unauthenticated**
  write endpoint (browser `navigator.sendBeacon`). It only appends to Kafka — no DB write, no
  downstream RPC. Hardened by: the edge rate-limiter (generous default bucket; no client key),
  the shared 1 MiB request-body cap, a 20-events-per-beacon cap, per-field length caps, and a
  closed `EventType` enum (unknown types dropped, not stored). The `owner_id` it records is a
  **best-effort, signature-unverified** `sub` from any bearer token presented — used only to
  attribute analytics rows, never for authorization. The first-party `cid` cookie it mints is
  an opaque random UUID (HttpOnly, `SameSite=Lax`), never joined to PII; a production EU
  deployment would gate it behind consent. See [DECISIONS.md ADR-034](DECISIONS.md).

---

## 3. Infrastructure & runtime hardening

- **Images:** multi-stage → distroless/Chainguard; single static binary; `USER 65532`;
  `readOnlyRootFilesystem: true`; no shell, no package manager in the final layer.
- **Pod Security Standards:** namespaces labelled `restricted`. `runAsNonRoot`,
  `allowPrivilegeEscalation: false`, `seccompProfile: RuntimeDefault`, all capabilities
  dropped.
- **NetworkPolicies (L3/L4):** default-deny ingress **and** egress per namespace; explicit
  allows only (`ingress-gw → bff`, `bff → domain services`,
  `order → payment/inventory/fulfillment`, `<svc> → its own DB`, `* → Kafka`,
  `* → otel-collector`, `* → Keycloak JWKS`). No service can reach another service's database.
- **Istio `AuthorizationPolicy` (L7):** default-deny per namespace; allows keyed on the
  caller's **SPIFFE identity** + path/method (e.g. only `order`'s identity may call
  `inventory.Reserve`). Complements NetworkPolicy — a stolen pod IP still fails the identity
  check.
- **Secrets:** External Secrets Operator syncs from Vault into k8s Secrets mounted as files
  (not env where avoidable); rotation supported; no secret in Git, `values.yaml`, or an image
  layer (Gitleaks + Trivy secret scan enforce this).
- **Admin surfaces:** no gateway Admin API surface (Gateway API CRDs only, reconciled by
  Argo CD); istiod/`ztunnel` have no user-facing API; Grafana/Prometheus/Keycloak admin
  behind SSO + IP allow-list; `admin` app `HTTPRoute` IP-restricted via `AuthorizationPolicy`
  on the ingress gateway.
- **Resource limits:** every pod has requests+limits; `GOMEMLIMIT` from the memory limit;
  namespace `ResourceQuota` + `LimitRange`.

---

## 4. Supply chain

- Dependencies pinned (`go.sum`, `package-lock.json`); **Renovate** for controlled updates.
- `go mod verify` and `buf` module verification in CI.
- **SBOM** per image via `syft` (SPDX), stored as a CI artifact + in MinIO `security-reports`.
- Images **signed with `cosign`** (keyless / OIDC); deploy admission policy (Kyverno or
  Sigstore policy-controller) rejects unsigned images in `staging`/`prod`.
- **SLSA provenance** attestation generated in CI (target: build level 3).
- Base images rebuilt weekly to pick up OS patches even with no code change.

---

## 5. CI security scanning — the four tools

Pipeline: `.github/workflows/security.yml`. Policy below follows the `security-scanning`
skill.

**First real run: PR #1, 2026-09-09.** `ci` + `security` both green. Every scanner below
executed for real and its actual output was read (§7). Gitleaks, Trivy (fs / config /
image) and SonarCloud run on every push + PR; ZAP is `workflow_dispatch` / weekly and runs
a passive baseline + an authenticated active API scan against the live stack. Re-check these
rows whenever the pipeline changes.

### 5.1 Gitleaks — secret scanning

- **Runs:** every `push` and `pull_request` (scans the diff); a **full-history** scan on a
  weekly schedule and on first setup.
- **Config:** `infra/security/.gitleaks.toml` — default ruleset plus allow-list entries for
  known test fixtures (each dated + justified).
- **Policy:** any finding **fails the build**. No `|| true`.
- **Needs `GITHUB_TOKEN`** in the job env (gitleaks-action v2 requires it to diff a PR) and a
  full-history checkout (`fetch-depth: 0`).
- **First run (2026-09-09):** ✅ 0 leaks. Scanned the full history + the PR diff.
- **TODO:** pre-commit hook (`lefthook`/`pre-commit`) so secrets are caught before a commit —
  not yet added.

### 5.2 Trivy — dependency / image / IaC scanning

Run the **raw `trivy` CLI** (pinned `v0.74.0`), installed from the pinned GitHub
release tarball — not a wrapper action, and not the upstream `install.sh` (its
unauthenticated "check for latest tag" API call gets rate-limited when the whole
`trivy-image` matrix runs at once). The tarball download itself uses
`curl --retry 5 --retry-connrefused --retry-all-errors` inside an outer
backoff loop, because the release CDN occasionally drops the TLS handshake
(curl exit 35) under the same ~17-way parallel pull. Every flag is explicit and
behaviour doesn't drift. Four invocations,
**different policies on purpose** (per the skill's guidance):

| Scan | What | Policy | First run (2026-09-09) |
|---|---|---|---|
| `trivy fs --scanners vuln,secret` | Our deps (`go.mod` ×3) + stray secrets | **Blocks** on `HIGH`/`CRITICAL` in our deps | ✅ 0 vulns, 0 secrets |
| `trivy image --pkg-types library` per service | App packages baked into the image | **Blocks** on `HIGH`/`CRITICAL` | ✅ `ext-authz` gobinary — 0 |
| `trivy image` (full, incl. base OS) | Upstream CVEs in the distroless base | **Informational** (`--exit-code 0`) | ✅ 0 at HIGH/CRITICAL |
| `trivy config .` | IaC misconfig (Dockerfile, Helm, k8s/Istio manifests) | **Informational** first; promote to blocking once baseline clean | ✅ 0 at HIGH/CRITICAL |

- **`.trivyignore`** at `infra/security/.trivyignore` — empty today; every future entry gets a
  **date**, a **CVE id**, and a **one-line reason**. Reviewed each release.

### 5.3 SonarCloud — static analysis & quality

- **This repo is public** → **SonarCloud** (free for public repos). No server to run.
  - Bootstrap (the account owner does this — it can't be scripted): sign in to
    sonarcloud.io with GitHub, import `deeprath/commerce-platform`, disable "Automatic
    Analysis" (we drive it from CI), generate a token, add it as the `SONAR_TOKEN` repo
    secret. Then the CI job runs `sonar-scanner` on every PR.
  - If the repo is later made private, switch to **self-hosted SonarQube Community**
    (`docker run -d -p 9000:9000 sonarqube:community`) — SonarCloud charges for private repos.
- **Scope:** Go (all `services/` + `pkg/`) and TypeScript (`web/`).
  - Coverage: `go test ./... -coverprofile=coverage.out` → `gocov`/native; vitest
    `--coverage` lcov for the React apps. Both uploaded to Sonar.
  - Exclusions: generated code (`**/*.pb.go`, `**/*_grpc.pb.go`, `web/**/gen/**`), migrations.
- **Quality gate:** the **"Sonar way" gate on new code** — new-code coverage ≥ 80%, 0 new
  bugs, 0 new vulnerabilities, security hotspots reviewed, duplication < 3%. **Gate failure
  blocks merge** via the PR check.
- **`sonar-project.properties`** is committed (project key `deeprath_commerce-platform`,
  org `deeprath`, coverage from `coverage.out`).
- **First run (2026-09-09):** the job runs but **self-skips** its scan step with a `::notice::`
  because the `SONAR_TOKEN` repo secret does not exist yet. Add the secret (steps above) to
  turn it on — nothing else needs to change.

### 5.4 OWASP ZAP — dynamic scanning

The tool most prone to "configured but never actually scanned anything." Guardrails baked in:

- **Target:** the full stack brought up by `deploy/compose/` in the CI runner (Envoy Gateway
  is the entry point locally — the mesh is not exercised by ZAP; the ingress gateway on
  staging is). The scan script (`infra/security/zap-scan.sh`, adapted from the skill's
  `assets/zap-scan.sh`) points ZAP at **`http://host.docker.internal:<gateway-port>`**, **not
  `localhost`** — inside the ZAP container `localhost` is the ZAP container itself, so a
  `localhost` target scans nothing while still exiting green. On Linux CI runners
  `host.docker.internal` is added via `--add-host=host.docker.internal:host-gateway`.
- **Report dir** is created `chmod 777` on the host before the run — the ZAP container's
  internal user otherwise can't write the report into the bind mount, and the step would go
  green with no report file.
- **Two passes** (`infra/security/zap-scan.sh`, weekly + `workflow_dispatch`):
  1. **Passive baseline** — `zap-baseline.py` against the Envoy edge. Security headers,
     cookie flags, cache hints, info-disclosure on whatever the edge serves.
  2. **Authenticated active API scan** — the ZAP Automation Framework plan
     `infra/security/zap/api-scan.yaml`. It imports a hand-maintained OpenAPI description
     of the BFF surface (`infra/security/openapi/bff.yaml` — *not* served by the BFF; it
     exists so ZAP scans every endpoint/method/param, not just what a spider finds), logs
     in as the realm-seeded `testuser` (`POST /api/v1/auth/login` → httpOnly `access_token`
     cookie, replayed by ZAP's cookie session management; the 401 body `SIGN_IN_REQUIRED`
     on `GET /orders` is the "logged out" signal so ZAP re-auths mid-scan), then runs an
     active scan tuned for a JSON API (SQLi / command / code injection / traversal / CRLF
     at low threshold, high strength).
- **Alert filter:** rule `10049` (Storable/Cacheable Content) is downgraded to INFO in the
  plan — it is a known low-severity item on the edge's error responses and the BFF now
  sends `Cache-Control: no-store` itself. A fresh finding on any other rule still fails.
- **Gate:** the CI step parses every `reports/*.json` and fails on any `HIGH` (riskcode 3).
  `MEDIUM`/`LOW`/`WARN` are printed with counts and triaged into §8.
- **`/checkout` + `/checkout/confirm` are excluded from the active scan** — they drive the
  irreversible order saga (Kafka → payment / inventory / fulfillment); fuzzing them creates
  thousands of orders, drains seeded stock, and stalled an early run. Thin BFF
  pass-throughs; still covered by the passive pass.
- **Reports:** HTML + JSON uploaded as the `zap-reports` CI artifact.
- **First authenticated run (2026-09-10, local, live compose stack):** full stack up, ZAP
  authenticated as `testuser`, OpenAPI import → spider → active scan (~4 min).
  - Round 1 raised 4 real issues: `10021` X-Content-Type-Options missing, `90022`
    Application Error Disclosure (`GET /orders/{id}` → 500 on a bad UUID), `40018` **SQL
    Injection HIGH** on `PUT /cart/items/{productId}`, plus `10106` HTTP-Only-Site.
  - `40018` was run down by hand and confirmed a **false positive** (the cart is
    Redis-backed); `10021`, `90022` and the `40018` root-cause (no product-id validation)
    were all **fixed** in this change.
  - Round 2 (post-fix): **0 HIGH, 0 LOW, 1 MEDIUM** (`10106` HTTP-Only-Site — the
    intentional dev-stack artifact, §8), 4 INFO. Gate: **PASS**.
- **Not covered / follow-ups:** the admin surface (`/api/v1/admin/**`) needs an operator
  role + is source-IP gated — a second operator-context scan is a follow-up. Staging runs
  the same plan against the real ingress gateway.

### 5.5 Coraza WAF (OWASP CRS v4) — edge request filtering

- **Where:** the ingress gateway / edge Envoy, *before* ext-authz and the rate limiter
  (`phase: AUTHN`), so a malicious payload is dropped before anything downstream sees it.
- **What:** the [coraza-proxy-wasm](https://github.com/corazawaf/coraza-proxy-wasm) module
  (v0.6.0, CRS **v4.14.0** bundled) in anomaly-scoring **blocking** mode, paranoia level 1.
  Denies with `403` when the inbound anomaly score crosses the CRS threshold.
- **Local == cluster:** the `.wasm` is baked into the edge Envoy image
  (`deploy/docker/envoy.Dockerfile`, checksum-pinned) and wired in
  `deploy/compose/envoy/envoy.yaml`; the cluster runs the identical directives as an Istio
  `WasmPlugin` (`deploy/istio/waf-wasmplugin.yaml`, module from
  `oci://ghcr.io/corazawaf/coraza-proxy-wasm`). `task up` exercises the real ruleset.
- **Audit:** CRS events are written to stdout as JSON (`SecAuditLog /dev/stdout`,
  `SecAuditLogFormat JSON`) → picked up by Alloy/Loki like every other container log.
- **Verified (2026-09-10, live compose stack):**
  - **Blocks:** boolean/`UNION` SQLi, `<script>` XSS, `../etc/passwd` traversal, `; cat
    /etc/passwd` + Shellshock UA — all `403` (CRS 930/932/941/942, blocking rule 949111).
  - **No false positives** on the checkout funnel: k6 through the WAF edge, 112/112
    checkouts, 0 failures; plus hand-checked apostrophe/ampersand/unicode addresses
    (`Sean O'Brien`, `Smith & Sons`, `Düsseldorf`) and search terms (`25% off`, `C++ book`,
    `men's shirt (blue)`) — all pass.
- **Tuning:** one deliberate policy widening — CRS 911100 (method enforcement) defaults to
  `GET HEAD POST OPTIONS`, but the JSON API legitimately uses **`PUT`** (cart quantity,
  admin catalog publish) and **`DELETE`** (cart item removal, order-share revoke). A
  `SecAction id:900200` sets `tx.allowed_methods` to add `PUT PATCH DELETE` before the rules
  load, in both `deploy/compose/envoy/envoy.yaml` and `deploy/istio/waf-wasmplugin.yaml`.
  Any other exclusion would go in the `directives_map` (`SecRuleRemoveById` /
  `ctl:ruleRemoveTargetById`), never a blanket rule disable.

---

## 6. Data protection & compliance

- **PCI-DSS:** scope minimized to **SAQ-A** — card data entered into PSP-hosted fields /
  iframes, tokenized by the PSP; the platform stores only a PSP token + last-4 + brand.
  No PAN, no CVV, ever, anywhere (including logs and traces — `pkg/telemetry` has a
  scrubbing processor).
- **GDPR / privacy:**
  - **Right to erasure:** `user.erasure_requested` event → each service scrubs/anonymizes
    its owner-scoped rows (orders are retained but PII-stripped for legal/financial record);
    a saga tracks completion.
  - **Right to access / portability:** `identity` orchestrates an export job → JSON bundle
    in MinIO `exports` with a 7-day presigned link.
  - **Data minimization:** only fields with a stated purpose are collected; retention rules
    per data class (e.g. auth logs 1 year, marketing consent until withdrawn).
  - **Consent:** cookie/consent state stored server-side against the principal; analytics
    events gated on consent.
- **Audit log:** append-only, in its own datastore, one entry per admin/`csr`/`platform_admin`
  action (actor, action, target, before/after hash, request id) — tamper-evident (hash
  chain), never deleted by application code.
- **Backups:** encrypted at rest; Postgres PITR (WAL to object storage); restore tested
  quarterly.

---

## 7. The verification rule

From the `security-scanning` skill, and non-negotiable for this repo:

> A security tool that is "configured" but has never actually executed a real scan is worse
> than not having one, because everyone downstream assumes it's covering them.

A tool is **not done** until:

1. **It has been triggered for real** — a real CI run (`gh workflow run security.yml`), or a
   real local run, not just merged config.
2. **It finished, and its actual log output was read** — not just a green checkmark. A green
   step can mean "scanned everything, found nothing" *or* "scanned nothing" — only the log
   tells them apart.
3. **The real artifact was pulled and opened** — the Trivy table, the ZAP JSON/HTML, the
   SonarQube API (`/api/measures/component`, `/api/issues/search`), the Gitleaks report.
4. **The real findings replace the "_not yet run_" placeholders** in §5, and each is
   classified as *fixed*, *accepted tradeoff* (with reason, in §8), or *open item* (with an
   owner).
5. **Any failure was fixed at the real cause** — never with `|| true`, never by loosening a
   threshold to go green.

Checklist to close out before Phase 0 is "done":

- [x] Gitleaks: real CI run (PR #1, 2026-09-09), full history + PR diff, 0 leaks
- [x] Trivy: real CI run of fs / config / image (×2), all tables read, all clean at HIGH/CRITICAL
- [~] SonarCloud: job wired and green; **scan self-skips until the `SONAR_TOKEN` secret is
      added** (owner action — §5.3)
- [x] ZAP: passive baseline **and** authenticated active API scan against the live compose
      stack (2026-09-10) — real reports read; the first authenticated run found + fixed a
      missing `X-Content-Type-Options` header and a 500 on a malformed order id (§8), and a
      SQL-injection alert on `PUT /cart/items/{productId}` that was verified a false positive
      (the cart is Redis-backed) with the underlying input-validation gap closed.
- [x] All four wired into `security.yml` with the documented block/inform policy
- [x] This document updated from the real output

---

## 8. Open items / accepted tradeoffs

### ZAP active-scan rules downgraded to INFO (compose-stack artifacts)
- Tool: zap (authenticated active scan, 2026-09-10) — `infra/security/zap/api-scan.yaml` `alertFilter`
- Status: **accepted tradeoff**, re-checked each run
- `10049` **Storable and Cacheable Content** — now only fires on Envoy's *own* 404/40x
  error bodies (`/robots.txt`, `/sitemap.xml`). The BFF itself sends `Cache-Control:
  no-store` on every response (`secureHeaders`), so authed API payloads are covered.
  Envoy's static error pages carry no sensitive data.
- `10106` **"HTTP Only Site"** — the compose edge listens on plain HTTP by design; TLS is
  terminated at the k8s ingress `Gateway` (ARCHITECTURE §8.7). Staging runs the same plan
  over HTTPS.
- `10024` **"Sensitive Information in URL"** — matches the opaque `page_token` pagination
  cursor against a "sensitive param name" list. The cursor is not a secret.

Only `10049` is filtered in the plan; `10106` (MEDIUM) and `10024` (INFO) surface as WARN
and are triaged here — the CI gate fails on HIGH only, and a fresh finding on any other
rule is still visible in the report and the step output.

### ZAP-40018 — SQL Injection on `PUT /cart/items/{productId}` — false positive
- Tool: zap (authenticated active scan, 2026-09-10), reported HIGH
- Status: **false positive, verified**; underlying input-validation gap **fixed**
- Detail: the `cart` service is Redis-backed (JSON blobs) — there is no SQL. ZAP's
  boolean-based heuristic (`… AND 1=1 --` vs `… AND 1=2 --`) tripped because the endpoint
  accepted *any* string as a line id and appended a line per request, so the two probe
  responses differed. Reproduced by hand: both probes now return an identical `400`.
- Fix: `cart` rejects a non-UUID `product_id` with `InvalidArgument` (`checkProductID` in
  the domain aggregate). Not a rule suppression — a real validation gap that also removes
  the signal the heuristic keyed on.

### SonarCloud not yet active
- Tool: sonarcloud
- Status: **open item** (owner action)
- Detail: the CI job is wired and green but self-skips its scan until the `SONAR_TOKEN`
  repo secret exists. See §5.3 for the one-time setup.

### Fixed
- **ZAP `10021` — X-Content-Type-Options missing** on every BFF response. First
  authenticated scan, 2026-09-10. **Fixed** by the `secureHeaders` middleware (also adds
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store`).
- **ZAP `90022` — Application Error Disclosure**: `GET /orders/{id}` returned HTTP 500 for
  a syntactically invalid id (Postgres `uuid` cast error; body was already generic).
  First authenticated scan, 2026-09-10. **Fixed** — the order store rejects a malformed id
  as `NotFound` before the query (`notFoundID`), so it is a clean 404.
- **CVE-2026-17106** — `github.com/moby/go-archive` (transitive via testcontainers-go,
  test-only). Trivy `fs` HIGH on PR #2. **Fixed** by bumping to `v0.3.0`.
- **CVE-2026-55677** — `github.com/labstack/echo/v4` (the BFF's HTTP framework),
  unauthorized information disclosure. Trivy `fs` HIGH on PR #2. **Fixed** by bumping to
  `v4.15.3`.

Both fixes were dependency bumps caught by the blocking `trivy fs` gate before merge — no
`.trivyignore` entry, no accepted risk.

### Accepted for now (revisit as noted)
- **Base-OS CVE scan is informational**, not blocking — deliberate (§5.2); distroless base
  is rebuilt weekly. Revisit if a reachable base CVE ever appears.
- **`trivy config` is informational**, not blocking — promote to blocking once Phase 1–3
  manifests are all clean.
- **No pre-commit secret hook yet** — CI gitleaks is the backstop; add `lefthook` in Phase 1.

---

Template for a new entry:

```
### <CVE id / finding id> — <short title>
- Tool: <gitleaks|trivy|sonarqube|zap>
- First seen: <date>
- Status: accepted tradeoff | open item
- Rationale (if accepted): <why it's not exploitable here, or why the fix is deferred>
- Owner / review date (if open): <name> / <date>
```
