# Observability — cluster manifests

Kubernetes-only. Local `docker compose` ships the same rules as a plain Prometheus
`rule_files:` entry (`deploy/compose/prometheus/rules/slo.yml`) — see
`docs/DECISIONS.md` ADR-024.

| File | Purpose |
|---|---|
| `prometheus-rules.yaml` | `PrometheusRule` CR: SLO recording rules + multi-window multi-burn-rate alerts, mirroring the compose `slo.yml` |

## Apply

`kube-prometheus-stack` (installed by the `deploy/helm/platform` app-of-apps into the
`observability` namespace) discovers `PrometheusRule` CRs by a release label:

```bash
kubectl apply -f deploy/k8s/observability/prometheus-rules.yaml
```

The manifest sets `metadata.labels.release: kube-prometheus-stack` — change it to match
your kube-prometheus-stack Helm release name if it differs. The two rule sources
(this CR and the compose file) are kept in sync by hand; the group and rule names are
identical.

## Alerts

| Alert | Severity | Condition |
|---|---|---|
| `SLOErrorBudgetFastBurn` | page | error ratio > 14.4× budget over 1h **and** 5m, for 2m |
| `SLOErrorBudgetSlowBurn` | ticket | error ratio > 6× budget over 6h **and** 30m, for 15m |
| `ServiceServingNoTraffic` | ticket | a service serves 0 gRPC calls for 10m while the platform is active |

SLO: 99.5% of gRPC calls per service return a non-error status over 30 days
(error budget 0.5%). Client-fault statuses (`NOT_FOUND`, `INVALID_ARGUMENT`,
`UNAUTHENTICATED`, `PERMISSION_DENIED`, `ALREADY_EXISTS`, `FAILED_PRECONDITION`) are
excluded from the error numerator.
