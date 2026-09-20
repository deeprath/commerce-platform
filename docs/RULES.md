# Rules

Engineering conventions for this repo. [`CLAUDE.md`](../CLAUDE.md) is the orientation; this is the
checklist. A rule is here because breaking it caused a real problem, not because it sounded tidy.

---

## 1. Hard don'ts

1. **Don't hand-edit `gen/go/`.** It is generated. Change the `.proto`, run `task proto`.
2. **Don't add a service mesh to `deploy/compose/`.** Local runs plaintext on purpose; mTLS,
   `AuthorizationPolicy` and locality failover exist only on Kubernetes (ARCHITECTURE.md §8.7).
3. **Don't create a second OpenFGA model file.** `pkg/fga/model.json` is canonical and grows
   additively (ADR-039).
4. **Don't hand-roll a gRPC→HTTP status mapping in a handler.** It belongs in `pkg/errs`.
5. **Don't put business logic in `pkg/`.** Shared libraries only: `auth`, `config`, `errs`, `fga`,
   `grpcx`, `kafka`, `pgx`, `telemetry`.
6. **Don't let a React app hold a gRPC client** or call a domain service. The BFF is the only door.
7. **Don't pin a dependency per-service.** With 18 modules and no shared `go.sum`, the next
   `go mod tidy` anywhere reverts it. `go.work`'s `replace` is the only durable mechanism — this bit
   the repo twice with `moby/go-archive`.
8. **Don't log PII, tokens, or anything a PSP would call cardholder data.**
9. **Don't propose a different technology without reading [`DECISIONS.md`](DECISIONS.md)
   first.** 44 ADRs, including the reversals. The tradeoff was probably already litigated.

## 2. Service shape

Every service is its own Go module and looks the same inside:

```
internal/domain/    aggregate + business rules — NO I/O, no pgx, no proto types
internal/store/     Postgres via pgx; migrations/ with goose
internal/grpcsvc/   the gRPC server — thin; maps requests to domain/store calls
internal/consumer/  Kafka handlers, where applicable
main.go             wiring only
```

If a rule can be tested without a database, it belongs in `internal/domain`.

## 3. Protobuf

- `proto/commerce/<domain>/v1/` is the single source of truth for every contract.
- Run `task proto` (buf lint + format + generate) after **any** `proto/` change; commit the
  regenerated stubs in the same commit.
- Backward compatibility is enforced by `buf breaking` in CI. A breaking change needs a new version
  directory, not a fixed-up field number.
- Validate inputs with `protovalidate` field constraints, not hand-written guards in the handler.

## 4. Kafka

- **Publish through the outbox**, never directly from a handler. The domain write and the event row
  commit in one transaction; the relay drains it.
- **Every consumer is idempotent.** Thread the `eventID` into a store-level
  `Apply(ctx, id, eventID, fn)` that no-ops on replay. At-least-once delivery guarantees you will
  see duplicates.
- **A failing record is retried a bounded number of times, then parked on `<topic>.dlq`.** A failure
  the downstream will accept later (backpressure) is *held* — retried in place with backoff capped
  at 30s, never advanced past — not parked and not dropped.
- **Test a consumer against a real poll/commit loop**, not just the dispatch function's return
  value. `pkg/kafka/hold_live_test.go` exists because unit tests on `dispatch` missed a bug where
  held records were silently lost (see [`MEMORY.md`](MEMORY.md)).

## 5. Authorization

Apply all three layers, in order, every time:

1. `pkg/auth` verifies the JWT against Keycloak JWKS and `RequireRole(...)` gates the RPC.
2. The **repository** scopes by `owner_id == principal.Subject`. Never role-only.
3. `pkg/fga.Check(...)` runs **only after** 1 and 2 have denied, and any error leaves the denial
   standing.

A service that omits `grpcx.WithAuth` is unprotected — the edge check is structural only and is not
a gate. `search` omits it deliberately because browse is anonymous; that is the only exemption.

## 6. Errors

- Domain code returns `pkg/errs` taxonomy errors.
- `internal/grpcsvc` translates to gRPC status via `pkg/errs`.
- The BFF translates gRPC status to HTTP via `pkg/errs`.
- Never leak an internal error string to an HTTP response.

## 7. Testing

- `task test` runs everything and **needs Docker** — integration tests spin up real Postgres via
  testcontainers. `task test:unit` (`go test -short`) skips them.
- Integration tests run against the real dependency, not a mock. That is the house style.
- A compensation path is not done until a test exercises it (`services/order/internal/saga/saga_test.go`
  is the reference).
- Front-end component tests use **jsdom, not happy-dom** — happy-dom doesn't dispatch a form's
  `submit` event when its `type="submit"` button is clicked, which silently no-ops every form test.

## 8. Before you push

```bash
task proto   # only if proto/ changed
task vet && task lint && task test
```

Front-end: `npm run lint && npm run typecheck && npm run test` in the app you touched.

## 9. Commits and PRs

- Subject line: `<area>: <imperative, lowercase>` — e.g. `kafka: make holding a record actually keep it`.
  Say what changed in behaviour, not which files moved.
- One concern per commit. A Sonar fix and a feature don't share a commit.
- **The repo is public.** Commit messages and PR descriptions end with the standard Claude Code
  attribution — check a recent commit for the exact form, and just append it.
- Branch off current `main`. If a PR merged before a follow-up commit on its branch landed, that
  commit is stranded — check with `git merge-base --is-ancestor <branch> origin/main`; the fix is a
  fresh branch with the commit cherry-picked, never a force-push.
