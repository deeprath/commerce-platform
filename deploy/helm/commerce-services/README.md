# commerce-services

One templated Helm chart that renders the platform's own services. For each
entry in `.Values.services` it emits:

| Resource | Notes |
|---|---|
| `ServiceAccount` | `name: <svc>` — the SPIFFE identity the mesh `AuthorizationPolicy` keys on. `automountServiceAccountToken: false`. |
| `Deployment` | PSS `restricted` pod + container securityContext, native `grpc`/`http` probes, `GOMEMLIMIT` from the memory limit, `preStop` drain, `RollingUpdate maxUnavailable: 0`, and `topologySpreadConstraints` — a node-level spread (always soft) plus, when `global.multiZone.enabled`, a `topology.kubernetes.io/zone` spread (`ScheduleAnyway` by default; `DoNotSchedule` for `bff`/`order`/`payment` to guarantee a per-zone copy). Both use `matchLabelKeys: [pod-template-hash]` so a rollout spreads independently of the old ReplicaSet. |
| `Service` | `ClusterIP`, `appProtocol: grpc`/`http`. |
| `PodDisruptionBudget` | `minAvailable: 1` by default; a service may instead set `podDisruptionBudget.maxUnavailable` (e.g. `34%` on the critical services, so a one-zone voluntary drain proceeds but a whole-service eviction is blocked). Toggle with `podDisruptionBudget.enabled`. |
| `DestinationRule` (Istio) | `<svc>-locality` — only when `global.multiZone.enabled` and `localityLB.enabled` (default on). Locality-aware LB (`failoverPriority: zone → region`) keeps east-west gRPC in-zone; `outlierDetection` ejects unhealthy endpoints so failover to another zone actually triggers; bounded `connectionPool`. A no-op in a single-zone cluster. |
| `NetworkPolicy` (×2) | `<svc>-ingress` (only the callers) + `<svc>-egress` (infra per flags + downstream services, *derived* from every service's `callers`). Plus a one-time namespace `default-deny-all` and `allow-egress-common` (DNS + otel). Toggle with `networkPolicy.enabled`. |
| `AuthorizationPolicy` | `allow-to-<svc>` — Istio L7, keyed on the caller's SPIFFE identity; method-scoped where `callers[].methods` is set. `callers: []` → `rules: []` = deny all L7. Toggle with `authorizationPolicy.enabled`. |
| `ScaledObject` (KEDA) | only when `autoscaling.enabled`. Triggers: `rps` (Prometheus `rpc_server_*` rate), `kafkaLag` (consumer group lag), `cpu`/`memory` (utilization). The Deployment then omits `spec.replicas` so the HPA owns it; `scaleTargetRef` points at the `Rollout` if there is one. |
| `Rollout` + `<svc>-canary` Service + `AnalysisTemplate` (Argo Rollouts) | only when `rollout.enabled`. Canary via the Gateway API plugin on `global.rollout.httpRoute`; `workloadRef` → the Deployment (no template duplication); background analysis on checkout health. |

The namespace-wide Istio `default-deny` and the gateway-scoped policies stay in
`deploy/istio/authorization-policy.yaml`. Secrets (External Secrets Operator), HPA/KEDA,
and `Rollout` are not templated here — they layer on the `app: <svc>` label / `sa/<svc>`
identity this chart creates.

## Values

`defaults:` applies to every service; a `services.<name>:` entry overrides it. Per-service
flags:

| Flag | Effect |
|---|---|
| `protocol` / `port` | `grpc` : `50051` (default) or `http` (the BFF, `8080`) |
| `db: <name>` | inject `DATABASE_URL` from secret key `<NAME>_DATABASE_URL`; allow egress to Postgres |
| `kafka: true` | inject `KAFKA_BROKERS`; allow egress to Kafka |
| `redis: true` | inject `REDIS_URL`; allow egress to Redis |
| `auth: true` | inject `KEYCLOAK_JWKS_URL` + `KEYCLOAK_ISSUER`; allow egress to Keycloak |
| `callers: [...]` | who may call this service — drives `NetworkPolicy` ingress **and** `AuthorizationPolicy`. Entry = a service name, `"gateway"`, or `{name, methods: [<rpc>...]}`. Service X's egress to Y is derived: Y lists X in `callers`. |
| `egressExtra: [...]` | extra L3/L4 egress targets (`{selector, port, namespace?}`) — e.g. `media` → MinIO, `search` → OpenSearch |
| `grpcService` | proto service name (e.g. `commerce.payment.v1.PaymentService`) — required only for method-scoped `callers` |
| `autoscaling` | `{enabled, minReplicas, maxReplicas, triggers: [{type: rps\|kafkaLag\|cpu\|memory, threshold, group?}]}` — renders a KEDA `ScaledObject` and drops `spec.replicas` from the Deployment |
| `rollout` | `{enabled, mirror, analysis, steps: [...]}` — renders an Argo `Rollout` (canary via the Gateway API plugin), a `<svc>-canary` Service and, if `analysis`, an `AnalysisTemplate`. Only meaningful for a service behind an `HTTPRoute` (the BFF). |
| `envFromSecret: true` | also `envFrom` the shared secret (BFF client secret, MinIO keys) |
| `env: {…}` | literal env, wins over `global.commonEnv` |
| `topologySpread` | `{enabled, maxSkew, node: {whenUnsatisfiable}, zone: {enabled, whenUnsatisfiable}}` — zone spread also gated by `global.multiZone.enabled` |
| `localityLB` | `{enabled}` — render the `<svc>-locality` DestinationRule (gated by `global.multiZone.enabled`) |
| `replicas`, `resources`, `podDisruptionBudget`, `networkPolicy`, `authorizationPolicy` | standard overrides |

Secrets (`global.secretName`, default `commerce-services`) hold the per-service
`<SERVICE>_DATABASE_URL` keys, `KEYCLOAK_CLIENT_SECRET`, MinIO keys, `CLICKHOUSE_DSN` —
provisioned by the External Secrets Operator, never by this chart.

## Use

```bash
# production-shaped defaults
helm template commerce-services deploy/helm/commerce-services | kubeconform -strict -summary

# local kind: 1 replica, images side-loaded (kind load), plaintext issuer, no PDB
helm upgrade --install commerce-services deploy/helm/commerce-services \
  -n commerce -f deploy/helm/commerce-services/values-dev.yaml
```

In the cluster it is deployed by the `deploy/helm/platform` app-of-apps as the
`commerce-services` Argo CD `Application`.
