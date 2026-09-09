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
| Internal network | Lateral movement after one pod compromise | Default-deny NetworkPolicies (L3/L4) **and** Istio `AuthorizationPolicy` (L7 identity); mesh mTLS between all services (`PeerAuthentication: STRICT`); per-service ServiceAccount / SPIFFE identity; PSS `restricted` |
| Secrets | Leak via Git, image layers, env dumps | Gitleaks in CI + pre-commit; External Secrets Operator + Vault; nothing sensitive in `values.yaml` or images; at-rest app secrets AES-GCM encrypted |
| Supply chain | Malicious/vulnerable dependency, poisoned base image | Trivy (blocking on our deps); pinned versions; `go mod verify` / `buf` ; SBOM; signed images; Renovate |
| Public endpoints | Injection, DoS, scraping, header misconfig | Ingress-gateway rate limiting (`envoyproxy/ratelimit` + Redis) + `ext-authz` shallow check + Coraza (OWASP CRS) Envoy filter; ZAP baseline scan; input validation at BFF + services |

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
  every repository — never role-only.
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
  `Permissions-Policy` minimal. These fail silently if regressed — the **ZAP baseline scan is
  the backstop** that catches a missing header against the real running stack.
- **CORS:** one `HTTPRoute` CORS filter at the ingress gateway; explicit origin allow-list
  per environment; no wildcard with credentials.
- **Webhooks (payment):** HMAC signature verified before the body is parsed; replay window
  enforced; handler idempotent on the PSP event id.

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
image) and SonarCloud run on every push + PR; ZAP is `workflow_dispatch` / weekly and was
dispatched once against the live stack. Re-check these rows whenever the pipeline changes.

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
`trivy-image` matrix runs at once). Every flag is explicit and behaviour doesn't
drift. Four invocations,
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
- **Scans:**
  - **Baseline** (passive + a short spider) — on a **weekly schedule** and
    `workflow_dispatch`. Fails on new `HIGH` alerts; `WARN` triaged.
  - **Full active scan** — weekly, longer, against staging only (never prod), auth context
    configured with a test user so authenticated routes are actually exercised.
- **Reports:** HTML + JSON uploaded as the `zap-reports` CI artifact (MinIO archival is a
  Phase 1 TODO).
- **First run (2026-09-09, `workflow_dispatch`):** brought up the full compose stack in the
  runner, scanned the Envoy edge on `host.docker.internal:8080` (3 URLs), ran 7m36s.
  - **FAIL-NEW: 0** — no HIGH alerts; the gate passed.
  - **WARN-NEW: 1** — `Storable and Cacheable Content [10049]` ×3: the edge's 401/403/503
    responses carry no `Cache-Control: no-store`. Low severity; fix is a header on the BFF's
    authed responses in Phase 1. Tracked in §8.
  - The security-header checks (CSP, HSTS, `X-Content-Type-Options`, Permissions-Policy, …)
    all report PASS, but **only because the edge has no HTML/content responses yet** — real
    header enforcement gets exercised once the BFF serves real payloads (Phase 1).
- **TODO (Phase 4):** authenticated context (Keycloak token in a ZAP context file) + the
  full active scan against staging.

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
- [x] ZAP: real `workflow_dispatch` scan against the live compose stack — confirmed it hit
      `host.docker.internal:8080` (the Envoy edge), not an empty `localhost`; report read;
      FAIL-NEW 0, one WARN tracked in §8. Authenticated context is Phase 4.
- [x] All four wired into `security.yml` with the documented block/inform policy
- [x] This document updated from the real output

---

## 8. Open items / accepted tradeoffs

### ZAP-10049 — Storable and Cacheable Content on edge error responses
- Tool: zap (baseline, 2026-09-09)
- Status: **open item**
- Detail: the Envoy edge's 401/403/503 responses have no `Cache-Control: no-store`.
  Low severity (no sensitive body today), but authed API responses must not be cacheable.
- Fix: set `Cache-Control: no-store` on the BFF's authenticated responses; revisit in Phase 1.

### SonarCloud not yet active
- Tool: sonarcloud
- Status: **open item** (owner action)
- Detail: the CI job is wired and green but self-skips its scan until the `SONAR_TOKEN`
  repo secret exists. See §5.3 for the one-time setup.

### Fixed
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
