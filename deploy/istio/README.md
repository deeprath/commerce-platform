# Istio (ambient) + Gateway API

Kubernetes-only. Local `docker compose` uses plain Envoy instead — see
`docs/ARCHITECTURE.md` §8.7.

## Install order (also encoded in `deploy/helm/platform`)

```bash
# 1. Gateway API CRDs
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml

# 2. Istio ambient (base -> istiod -> cni -> ztunnel)
helm repo add istio https://istio-release.storage.googleapis.com/charts
helm install istio-base   istio/base   -n istio-system --create-namespace
helm install istiod        istio/istiod -n istio-system --set profile=ambient --wait
helm install istio-cni     istio/cni    -n istio-system --set profile=ambient --wait
helm install ztunnel       istio/ztunnel -n istio-system --wait

# 3. Platform config in this directory
kubectl apply -f deploy/istio/
```

`meshConfig.extensionProviders` (registered via the istiod values, see
`deploy/helm/platform/values.yaml`) must define:

- `otel` — the OpenTelemetry collector, for `Telemetry`
- `ext-authz-grpc` — the `ext-authz` service, for the `CUSTOM` `AuthorizationPolicy`
- `edge-ratelimit` — the `ratelimit` service, for the global rate-limit `EnvoyFilter`

## What each file does

| File | Purpose |
|---|---|
| `namespace.yaml` | `commerce` namespace, joined to the ambient mesh |
| `gateway.yaml` | Gateway API `Gateway` (the Istio ingress) on :443 |
| `httproute-bff.yaml` | routes `/api/v1/*` to the `bff` Service; admin host split |
| `peer-authentication.yaml` | `STRICT` mTLS mesh-wide |
| `authorization-policy.yaml` | default-deny + the Phase 0 allow-list; `CUSTOM` ext-authz on the gateway |
| `telemetry.yaml` | traces + metrics to the `otel` provider |
| `ratelimit-envoyfilter.yaml` | global edge rate limit via the `edge-ratelimit` provider |
| `waf-wasmplugin.yaml` | OWASP CRS WAF (Coraza proxy-wasm) on the gateway, `phase: AUTHN` |
