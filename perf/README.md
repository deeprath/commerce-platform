# Performance tests

[k6](https://k6.io) load tests that replicate the storefront checkout funnel.
They feed the capacity numbers in [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md)
§11 (resource requests, HPA / KEDA thresholds) and guard against latency
regressions on the checkout path.

## What `checkout-funnel.js` does

Two scenarios run together against the BFF's public REST API:

| Scenario | Executor | Models |
|---|---|---|
| `browse` | `ramping-vus` | anonymous shoppers: product list, autocomplete, PDP |
| `checkout` | `ramping-arrival-rate` | signed-in funnel: add to cart → `CreateOrder` → `ConfirmPayment` (the order saga runs downstream over Kafka) |

`setup()` authenticates once and every checkout VU reuses that bearer token —
the funnel models already-signed-in users, and auth throughput is a separate
concern. A sold-out product answers `CreateOrder` with `409` and is counted as
`checkout_out_of_stock`, not a failure (the seeded stock is 100 units/product,
so a long run on a stack that isn't freshly seeded will drain it).

### Thresholds (the pass/fail gate)

| Metric | Budget |
|---|---|
| `http_req_failed` | < 1% (transport / 5xx only; 409 is filtered out) |
| `checks` | > 99% |
| `http_req_duration{scenario:browse}` | p95 < 400 ms, p99 < 900 ms |
| `checkout_flow_duration` (login-less: cart → order → confirm) | p95 < 2.5 s |
| `checkout_flow_success` | > 95% |

k6 exits non-zero if any threshold is crossed.

## Run it

Bring the stack up first (`task up`), then:

```bash
task perf:load
# or, passing k6 env knobs through:
task perf:load -- -e BROWSE_VUS=40 -e CHECKOUT_RPS=8 -e HOLD=2m
```

`task perf:load` runs k6 in a container (no local install needed) against the
BFF on `:8088`. The JSON summary lands in `perf/out/summary.json` (gitignored).

With k6 installed locally you can also run it directly:

```bash
k6 run perf/checkout-funnel.js
BASE_URL=http://localhost:8080/api/v1 k6 run perf/checkout-funnel.js  # via the envoy edge
```

### Knobs (env vars)

| Var | Default | Meaning |
|---|---|---|
| `BASE_URL` | `http://localhost:8088/api/v1` | BFF direct; point at `:8080` for the envoy edge (adds ratelimit + ext-authz) |
| `K6_USERNAME` / `K6_PASSWORD` | `testuser` / `testuser123` | the realm-seeded shopper |
| `RAMP` / `HOLD` | `20s` / `40s` | k6 stage durations |
| `BROWSE_VUS` | `20` | peak browsing VUs |
| `CHECKOUT_RPS` | `3` | peak checkout iterations/sec |
| `CHECKOUT_MAX_VUS` | `40` | VU pool ceiling for the arrival-rate scenario |

## CI

[`.github/workflows/perf.yml`](../.github/workflows/perf.yml) runs this weekly
(Mondays 04:00 UTC) and on `workflow_dispatch` (with VU / RPS / hold inputs). It
brings up the full compose stack on a clean volume — so stock is freshly seeded
to 100/product — runs k6, uploads `summary.json`, and tears down. The job fails
if a threshold is crossed.
