# Memory

Things that cost someone an hour to learn, kept so they cost the next person nothing. Symptoms
first, because that is how you arrive here — you see the symptom and don't know the cause.

Not a changelog ([`DECISIONS.md`](DECISIONS.md) is), not a runbook ([`RUNBOOKS.md`](RUNBOOKS.md) is).
This is for the traps.

Last reviewed: 2026-09-20.

---

## 1. Environment traps

### Consumers look stuck, or events look "already processed", right after a restart
**Kafka has no persistent volume in compose.** `task down` + `task up` (without `-v`) keeps
Postgres's data but wipes Kafka's log, so `processed_events` dedup rows collide with the re-created
topic's reused low offsets. The fix is `task down:v && task up` (full wipe), **not** a code
investigation. Burning time debugging the consumer here is the classic mistake.

### Authz checks that should pass silently fail
**OpenFGA store bootstrap race.** Several services calling `pkg/fga.ensureStore` against a cold,
empty OpenFGA instance each create their own same-named store, and then point at different store IDs.

```bash
curl http://localhost:8083/stores            # find the duplicates
curl -X DELETE http://localhost:8083/stores/<id>   # delete the extras
```

Then restart the affected service containers.

### A dependency pin won't stay pinned
18 modules, no shared `go.sum`. A per-service `go get` pin is reverted by the next `go mod tidy`
anywhere in the workspace. **`go.work`'s `replace` is the only durable mechanism.** This bit the repo
twice with `moby/go-archive`.

### Every form-submit test silently passes while testing nothing
**Use jsdom, not happy-dom** (`vite.config.ts` in both SPAs). happy-dom doesn't dispatch a form's
`submit` event when its `type="submit"` button is clicked, so the handler never runs and the
assertion never fires.

---

## 2. CI and tooling traps

### A first-ever Sonar scan dumps dozens of issues at once
SonarCloud's **New Code period is `previous_version` mode pinned to a fixed date**, not a rolling
window — every commit since that date counts as "new code". It is a server-side project setting
(SonarCloud → Administration → New Code) needing account-owner access. **No PR can fix it.**

### A commit on a merged PR's branch goes nowhere
**A SonarCloud PR can merge before every commit on its branch lands.** Push a follow-up to an
already-merged branch and it is stranded.

```bash
git merge-base --is-ancestor <branch> origin/main   # exit 0 = that happened
```

The fix is a **fresh branch off current `main` with the commit cherry-picked**. Not a force-push.

### ZAP reports SQL injection on a service with no SQL
Known, triaged, documented in [`SECURITY.md`](SECURITY.md) §8 — `cart` is Redis-only and ZAP's
boolean heuristic tripped on a real input-validation gap (since fixed). Read §8 before chasing any
ZAP finding; several are accepted tradeoffs with reasoning.

---

## 3. Bugs that hid behind passing tests

Both of these matter more than the fix itself: they show the *shape* of test that misses this class
of bug here.

### A "held" Kafka record was silently dropped (fixed, PR #96)
"Hold" meant only "skip this commit" — but franz-go advances its poll position past every record it
hands over, committed or not. The held record was never offered again, and the next successful fetch
committed straight over it. The record was lost, including across a restart.

**Why the tests missed it:** they asserted on what `dispatch` *returned*. They never ran a real
poll/commit loop. `pkg/kafka/hold_live_test.go` now does, against franz-go's in-process `kfake`.

**Rule that came out of it:** a consumer's retry/hold/park behaviour is only tested by a real
poll/commit loop.

### A DLQ topic that existed only in a test fixture
An earlier version of [`../OVERVIEW.md`](../OVERVIEW.md) claimed `commerce.order.confirmed.dlq` was
"the only dead-letter topic in the system". That string came from a **test fixture** for
`pkg/kafka`'s `DLQ()` helper. There were in fact no DLQ topics at all — `DLQ()` was written and
unit-tested but never called by anything, while `ARCHITECTURE.md` §4.3 specified DLQ-after-N-retries
plus alerting.

**Rule that came out of it:** grepping a string proves the string exists, not that the code path
runs. Check for a caller.

---

## 4. Standing facts worth not re-deriving

- **`docs/ARCHITECTURE.md` §3's service table is the original design and has drifted.** An
  `identity` service and a `web/login` SPA are listed; neither was built. Login lives in
  `services/bff/internal/auth/broker.go`. [`../OVERVIEW.md`](../OVERVIEW.md) §9 is the divergence list;
  §14's roadmap checkmarks are the accurate record of what shipped.
- **Compose has no mesh on purpose.** Not a bug, not a gap to close (ARCHITECTURE.md §8.7).
- **`search` registers with no auth interceptor deliberately** — browse is anonymous. It is the only
  service allowed to skip `grpcx.WithAuth`.
- **`ext-authz` never verifies a signature** (`auth.ParseInsecure`). It sheds obviously-unauthenticated
  load; it is not the gate. Real verification is per-service against JWKS.
- **The DLQ *alerting* half is still missing** even though retry-and-park is wired. Tracked in
  [`TASKS.md`](TASKS.md).
- **Local login:** `testuser` / `testuser123`. Admin needs an operator role assigned in Keycloak
  ([`SETUP.md`](SETUP.md)).

---

**Adding to this file:** lead with the symptom you would actually see, then the cause, then the fix.
If it is discoverable by reading the code for two minutes, it doesn't belong here.
