# commerce-services

One templated Helm chart that renders the platform's own services. For each
entry in `.Values.services` it emits:

| Resource | Notes |
|---|---|
| `ServiceAccount` | `name: <svc>` — the SPIFFE identity the mesh `AuthorizationPolicy` keys on. `automountServiceAccountToken: false`. |
| `Deployment` | PSS `restricted` pod + container securityContext, native `grpc`/`http` probes, `GOMEMLIMIT` from the memory limit, `preStop` drain, `topologySpreadConstraints`, `RollingUpdate maxUnavailable: 0`. |
| `Service` | `ClusterIP`, `appProtocol: grpc`/`http`. |
| `PodDisruptionBudget` | `minAvailable: 1` (toggle with `podDisruptionBudget.enabled`). |

It deliberately does **not** template Secrets, `NetworkPolicy`, `AuthorizationPolicy`,
HPA/KEDA, or `Rollout` — those layer on top and key off the `app: <svc>` label and
`sa/<svc>` identity created here.

## Values

`defaults:` applies to every service; a `services.<name>:` entry overrides it. Per-service
flags:

| Flag | Effect |
|---|---|
| `protocol` / `port` | `grpc` : `50051` (default) or `http` (the BFF, `8080`) |
| `db: <name>` | inject `DATABASE_URL` from secret key `<NAME>_DATABASE_URL` |
| `kafka: true` | inject `KAFKA_BROKERS` |
| `redis: true` | inject `REDIS_URL` |
| `auth: true` | inject `KEYCLOAK_JWKS_URL` + `KEYCLOAK_ISSUER` |
| `envFromSecret: true` | also `envFrom` the shared secret (BFF client secret, MinIO keys) |
| `env: {…}` | literal env, wins over `global.commonEnv` |
| `replicas`, `resources`, `podDisruptionBudget`, `topologySpread` | standard overrides |

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
