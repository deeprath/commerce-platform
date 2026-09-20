# Tasks

Open work, newest assessment first. Sourced from the roadmap ([`ARCHITECTURE.md`](ARCHITECTURE.md) §14),
the known bottlenecks ([`OVERVIEW.md`](../OVERVIEW.md) §10), and the security open items
([`SECURITY.md`](SECURITY.md) §8).

Last reviewed: 2026-09-20 — against `origin/main` at `b4b4a02` (PR #96, the Kafka hold fix, is merged).

Status key: `[ ]` open · `[~]` in progress · `[x]` done · `[!]` blocked on something outside the repo.

---

## Now

- [ ] **Alert on DLQ messages.** Retry-and-park is wired, the *alerting* half of
  ARCHITECTURE.md §4.3 is not. A parked record is currently invisible until someone looks.
  Needs: a `<topic>.dlq` depth metric, a Prometheus rule, and a Grafana panel.
  → `pkg/kafka/`, `observability/prometheus/rules/`. Listed as a live gap in `OVERVIEW.md` §9.
- [ ] **Make the outbox relay tunable, then tune it.** Every service constructs
  `NewOutboxRelay(pool, producer, 0, 0)` — always the defaults. 100 rows / 1s = a hard ~100
  events/sec/service ceiling and a 1-second floor under every event's propagation latency.
  Needs: config plumbed per service, plus a backlog alert (the backlog metric already exists,
  `06f990a`). → `pkg/kafka/outbox.go`, each `main.go`. `OVERVIEW.md` §10.2.
- [ ] **Widen DLQ coverage per topic.** `order-saga` consumes four topics; a poison message on a
  topic without a DLQ still blocks its partition. `OVERVIEW.md` §10.6.

## Next

- [ ] **Compensation failures need a visible signal.** `compensateRelease` / `compensateVoid` log
  and return nothing, so a cancelled order can hold stock until the TTL expires with nothing
  surfacing it. At minimum a counter + alert; ideally a retry queue. `OVERVIEW.md` §8.2.
- [ ] **Hot-SKU contention.** `inventory` serializes `Reserve`/`Commit`/`Release` on a
  `SELECT … FOR UPDATE` row lock. Fine for spread demand, a throughput wall on a flash sale.
  No fix required yet — needs a k6 scenario that actually demonstrates the wall first.
  → `perf/`. `OVERVIEW.md` §10.4.
- [ ] **Checkout latency is the sum of four synchronous hops.** `cart → pricing (→ catalog) →
  inventory → payment`, all inside the HTTP request. Worth measuring per-hop before optimising;
  `pricing` already avoids the N+1 via `BatchGetProducts`. `OVERVIEW.md` §10.3.
- [ ] **Reconcile the design doc's service table.** `ARCHITECTURE.md` §3 still lists an
  `identity` service and a `web/login` SPA that were never built. Either annotate the table in
  place or point it at `OVERVIEW.md` §9.

## Blocked on someone with account access

- [!] **Turn on SonarCloud.** The CI job is wired and green but self-skips until the `SONAR_TOKEN`
  repo secret exists. → `SECURITY.md` §5.3.
- [!] **Fix SonarCloud's New Code period.** It is `previous_version` mode pinned to a fixed date,
  not a rolling window, so every commit since that date counts as new code. Server-side setting
  (SonarCloud → Administration → New Code); no PR can fix it.
- [!] **Pen-test remediation.** Phase 4's last open item — needs an actual engagement.

## Done (recent, for context)

- [x] Kafka: a held record is genuinely held — retried in place with backoff capped at 30s, never
  advanced past, verified against a real poll/commit loop with `kfake` (PR #96).
- [x] `search`: hold events through an OpenSearch outage, park the unprocessable ones (PR #95).
- [x] Idempotent checkout — a retry cannot place a second order (PR #89).
- [x] Logout invalidates the refresh token instead of only clearing the cookie (PR #90).
- [x] Bounded analytics buffer — Kafka is the backlog, not the heap (`OVERVIEW.md` §10.1).
- [x] Roadmap phases 0–5, including the full marketplace/multi-seller model through the seller
  dashboard (ADR-038…044).

---

**Adding a task here:** one line of what, one line of why it matters, and the file or doc section to
start from. A task without a "where to start" is a wish, not a task.
