# commerce-services

One templated Helm chart that renders the platform's own services. For each
entry in `.Values.services` it emits:

| Resource | Notes |
|---|---|
| `ServiceAccount` | `name: <svc>` — the SPIFFE identity the mesh `AuthorizationPolicy` keys on. `automountServiceAccountToken: false`. |
| `Deployment` | PSS `restricted` pod + container securityContext, native `grpc`/`http` probes, `GOMEMLIMIT` from the memory limit, `preStop` drain, `topologySpreadConstraints`, `RollingUpdate maxUnavailable: 0`. |
| `Service` | `ClusterIP`, `appProtocol: grpc`/`http`. |
| `PodDisruptionBudget` | `minAvailable: 1` (toggle with `podDisruptionBudget.enabled`). |
| `NetworkPolicy` (×2) | `<svc>-ingress` (only the callers) + `<svc>-egress` (infra per flags + downstream services, *derived* from every service's `callers`). Plus a one-time namespace `default-deny-all` and `allow-egress-common` (DNS + otel). Toggle with `networkPolicy.enabled`. |
| `AuthorizationPolicy` | `allow-to-<svc>` — Istio L7, keyed on the caller's SPIFFE identity; method-scoped where `callers[].methods` is set. `callers: []` → `rules: []` = deny all L7. Toggle with `authorizationPolicy.enabled`. |

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
| `envFromSecret: true` | also `envFrom` the shared secret (BFF client secret, MinIO keys) |
| `env: {…}` | literal env, wins over `global.commonEnv` |
| `replicas`, `resources`, `podDisruptionBudget`, `topologySpread`, `networkPolicy`, `authorizationPolicy` | standard overrides |

Secrets (`global.secretName`, default `commerce-services`) hold the per-service
`<SERVICE>_DATABASE_URL` keys, `KEYCLOAK_CLIENT_SECRET`, MinIO keys — provisioned by the
External Secrets Operator, never by this chart.

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
