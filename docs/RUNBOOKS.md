# Disaster-recovery runbooks

Operational procedures for data loss and infrastructure failure. Each runbook is
a numbered, copy-pasteable sequence. The Postgres and MinIO ones have been run
against the local `deploy/compose` stack (`infra/dr/verify-restore.sh` runs the
Postgres drill in CI-able form).

Design context is in [`ARCHITECTURE.md`](ARCHITECTURE.md) §11 and
[`DECISIONS.md`](DECISIONS.md) ADR-032.

---

## Targets

| | Target | How it's met |
|---|---|---|
| **RPO** (max data loss) | ≤ 5 min | Postgres WAL archived continuously to object storage; Kafka RF 3; MinIO versioning + cross-region replication. |
| **RTO** (time to restore service) | ≤ 1 h | CloudNativePG in-place PITR or a standby promotion; Argo CD re-syncs the whole platform from Git; `velero` for cluster objects. |
| **Backup retention** | 30 days PITR window, 90 days weekly base backups | CloudNativePG `retentionPolicy`; MinIO lifecycle rules. |

## What is backed up, and where

| Data | Mechanism | Restore runbook |
|---|---|---|
| Per-service Postgres DBs | **CloudNativePG**: nightly base backup + continuous WAL → object storage (`s3://commerce-pg-backups`). Local: `infra/dr/pg-backup.sh` (logical). | RB-1 (logical), RB-2 (PITR) |
| MinIO objects (`product-media`, `invoices`, `exports`, `security-reports`) | Bucket **versioning** on + **object lock** (governance) on `invoices`; **bucket replication** to a second region. | RB-3 (object), RB-4 (bucket) |
| Kafka topics | Replication factor **3**, `min.insync.replicas=2`. Topic configs in Git. | RB-5 |
| Consumer state after a service-DB loss | The **transactional outbox** + `processed_events` — events can be re-driven. | RB-6 |
| Keycloak realm + users | Realm export in Git (`deploy/compose/keycloak/realm-commerce.json`) for structure; the `keycloak` Postgres DB for live user data (RB-1). | RB-1 |
| Cluster objects (CRs, RBAC, PVC metadata) | `velero` scheduled backup; everything reconstructible is in Git via Argo CD. | RB-8 |

---

## Decision tree

| Symptom | Runbook |
|---|---|
| One service's DB is corrupt / a bad migration / a wrong `DELETE` | **RB-1** if you have a good logical dump and can tolerate losing writes since it; otherwise **RB-2** (PITR to a timestamp just before the incident). |
| Need data as of a specific past moment | **RB-2** |
| A product image / invoice was deleted or overwritten | **RB-3** |
| A whole MinIO bucket is gone / a region is down | **RB-4** |
| A Kafka broker is down / partitions under-replicated | **RB-5** |
| A downstream service lost its DB and its Kafka-derived state is stale | RB-1/RB-2 to restore the DB, then **RB-6** to re-drive events. |
| A deploy is bad (errors, latency, failed canary) | **RB-7** |
| The cluster / region is gone | **RB-8** |

---

## RB-1 — Restore a service database from a logical backup

Use when a single database is damaged and a recent `pg_dump` exists. Writes made
after the dump are lost — prefer **RB-2** if the RPO matters.

**Local (compose):**

```bash
# 1. Take a fresh backup FIRST (capture whatever is still good).
infra/dr/pg-backup.sh                      # -> backups/pg/<ts>/<db>.dump + MANIFEST

# 2. Restore one database from a chosen dump. --clean --if-exists drops and
#    recreates each object; --no-owner ignores the compose superuser.
DB=order
DUMP=backups/pg/20260910T134020Z/${DB}.dump
docker compose -f deploy/compose/docker-compose.yml exec -T postgres \
  pg_restore -U commerce -d "$DB" --clean --if-exists --no-owner --exit-on-error < "$DUMP"

# 3. Verify.
docker compose -f deploy/compose/docker-compose.yml exec -T postgres \
  psql -U commerce -d "$DB" -c "\dt" -c "SELECT count(*) FROM orders;"
```

**Cluster (CloudNativePG):** restore into a *new* `Cluster` from a `Backup`
object, then repoint the service, rather than restoring in place:

```bash
kubectl -n commerce apply -f - <<'EOF'
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata: { name: order-restored, namespace: commerce }
spec:
  instances: 3
  bootstrap:
    recovery:
      source: order          # an externalCluster pointing at the backup store
EOF
# then patch the order Deployment's DATABASE_URL secret to order-restored-rw.
```

**Caveat — single-table restores.** `pg_restore -t orders` brings back the table
and *its own* constraints, not foreign keys other tables had pointing at it (a
`DROP TABLE ... CASCADE` removes those). For anything but a trivial case, restore
the whole database.

---

## RB-2 — Postgres point-in-time recovery (cluster)

Use to recover to a timestamp just before an incident (bad migration at 14:03 →
recover to 14:02:30). Requires continuous WAL archiving (CloudNativePG default).

```bash
# 1. Find the target time (UTC). e.g. from the deploy that ran the bad migration:
#    kubectl -n commerce get events --sort-by=.lastTimestamp | grep migrate

# 2. Bootstrap a new Cluster with a recovery target.
kubectl -n commerce apply -f - <<'EOF'
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata: { name: order-pitr, namespace: commerce }
spec:
  instances: 3
  bootstrap:
    recovery:
      source: order
      recoveryTarget:
        targetTime: "2026-09-10 14:02:30+00"
  externalClusters:
    - name: order
      barmanObjectStore:
        destinationPath: s3://commerce-pg-backups/order
        s3Credentials: { accessKeyId: {…}, secretAccessKey: {…} }
EOF

# 3. Wait for `status.phase: Cluster in healthy state`, sanity-check the data,
#    then cut traffic over (update the DATABASE_URL secret, roll the Deployment).
kubectl -n commerce get cluster order-pitr -w
```

RPO here is the WAL archive interval (`archiveTimeout`, default 5 min) — the last
partial WAL segment may not have shipped.

---

## RB-3 — Recover a deleted or overwritten MinIO object

Versioning is on for all buckets, so a delete leaves a **delete marker** and an
overwrite keeps the prior version.

```bash
mc() { docker compose -f deploy/compose/docker-compose.yml exec -T minio mc "$@"; }
mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"

OBJ=local/product-media/p/abc123/hero.jpg

# See the version history (DEL = delete marker):
mc ls --versions "$OBJ"

# Simplest: undo the last op (removes a delete marker, or rolls back one
# overwrite). Repeatable — run it again to go back another version.
mc undo "$OBJ"

# Or restore an explicit version by copying it over the current key:
mc cp --version-id <VERSION-ID> "$OBJ" "$OBJ"
```

`invoices/` has object-lock (governance) — a version cannot be permanently
deleted before its retention date without `s3:BypassGovernanceRetention`.

---

## RB-4 — Restore a MinIO bucket

From the replication target (another region) or a `mc mirror` backup.

```bash
mc() { docker compose -f deploy/compose/docker-compose.yml exec -T minio mc "$@"; }

# Recreate the bucket with the same settings, then pull objects back.
mc mb --ignore-existing local/product-media
mc version enable local/product-media
mc mirror --overwrite --preserve backup/product-media local/product-media

# If a region is down, point clients at the replica endpoint and fail back with
# `mc mirror` once the primary is healthy.
```

Public read on `product-media` and the CSP `media-src` are set by
`deploy/compose/minio-init` / the storefront config — re-apply if the bucket was
recreated.

---

## RB-5 — Kafka: broker loss / under-replicated partitions

RF 3 + `min.insync.replicas=2` means one broker down = no data loss, producers
keep working.

```bash
K() { docker compose -f deploy/compose/docker-compose.yml exec -T kafka \
      /opt/kafka/bin/"$@" --bootstrap-server localhost:9092; }

# 1. Which partitions are under-replicated?
K kafka-topics.sh --describe --under-replicated-partitions

# 2. Bring the broker back (or replace it — same broker.id, empty data dir; it
#    re-replicates from the leaders). Watch ISR recover:
K kafka-topics.sh --describe --under-replicated-partitions   # -> empty

# 3. If a broker is permanently gone, reassign its partitions to the survivors:
K kafka-reassign-partitions.sh --generate \
  --topics-to-move-json-file /tmp/topics.json --broker-list "1,2"
K kafka-reassign-partitions.sh --execute --reassignment-json-file /tmp/plan.json
```

Topic configs (partitions, RF, retention) live in Git / the Strimzi `KafkaTopic`
CRs — re-apply to recreate a lost topic; consumers re-read from their committed
offsets, and anything not yet consumed is still on the surviving replicas.

---

## RB-6 — Re-drive the transactional outbox

After RB-1/RB-2 restores a producer service's DB to an earlier point, some rows
it had already published are marked `published_at` but consumers downstream may
be ahead or behind. Because every consumer dedupes on `processed_events`,
**re-publishing is safe** — a replayed event that was already handled hits a
`23505` and is skipped.

```bash
DB=order
docker compose -f deploy/compose/docker-compose.yml exec -T postgres \
  psql -U commerce -d "$DB" -c \
  "UPDATE outbox SET published_at = NULL WHERE created_at >= '2026-09-10 14:00:00+00';"
# The OutboxRelay (pkg/kafka) re-sends them on its next poll.
```

To make a *consumer* re-process from scratch (e.g. `search` after an OpenSearch
loss): reset its group and let it re-consume — the topics are the source of
truth.

```bash
docker compose -f deploy/compose/docker-compose.yml exec -T kafka \
  /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
  --group search-indexer --reset-offsets --to-earliest --all-topics --execute
```

---

## RB-7 — Roll back a bad deploy

**Canaried service (BFF):** the `AnalysisRun` aborts on its own (ADR-031). To
force it:

```bash
kubectl argo rollouts -n commerce abort bff       # shift 100% back to stable
kubectl argo rollouts -n commerce undo  bff       # roll back to the previous ReplicaSet
```

**Everything else:** Argo CD or Helm.

```bash
argocd app rollback commerce-services <previous-revision>
# or, direct:
helm -n commerce rollback commerce-services
```

Expand/contract migrations mean the old pods keep working against the new schema,
so a Deployment rollback is safe without a DB rollback. If a migration itself is
the problem, RB-2.

---

## RB-8 — Full cluster / region loss

1. **Provision a cluster** (Terraform / the platform module) in the recovery region.
2. **Bootstrap Argo CD**, point it at `deploy/helm/platform` on `main`. It syncs
   the mesh, operators, observability, then `commerce-services`.
3. **Restore state that isn't in Git:**
   - Postgres: new `Cluster`s with `bootstrap.recovery` from the backup store (RB-2), or promote the cross-region replica.
   - MinIO: it's already the replication target — flip it to primary (RB-4).
   - Kafka: MirrorMaker 2 replica, or accept the RPO and let topics rebuild from Postgres outboxes (RB-6).
   - Secrets: External Secrets Operator re-pulls from Vault.
4. **Cut DNS** (`store.` / `admin.` / `auth.`) to the new gateway's LoadBalancer.
5. **Smoke test:** `infra/security/zap-scan.sh` baseline + a k6 `perf/checkout-funnel.js` short run.

---

## Drills

| What | Cadence | How |
|---|---|---|
| Postgres restore verification | every CI run of the `dr` job + weekly on staging | `infra/dr/verify-restore.sh` (dump → restore into a scratch DB → row-count compare) |
| PITR to a random timestamp | quarterly, staging | RB-2 against real backup store |
| MinIO object + bucket restore | quarterly | RB-3 / RB-4 on a `dr-drill/` prefix |
| Full RB-8 game day | twice a year | scripted region failover, timed against the RTO |

Record each drill (date, RTO achieved, issues) below.

| Date | Drill | RTO | Notes |
|---|---|---|---|
| 2026-09-10 | RB-1 local (order DB, DROP TABLE → restore) | n/a (local) | 287/287 rows recovered; single-table `-t` restore doesn't rebuild inbound FKs — use full-DB restore. |
| 2026-09-10 | RB-3 local (MinIO delete → `mc undo`) | n/a (local) | `mc undo` reverts a delete marker and, run again, the prior version. |
