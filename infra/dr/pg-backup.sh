#!/usr/bin/env bash
# Logical backup of every per-service Postgres database in the compose stack.
#
# This is the LOCAL drill / last-resort tool. In the cluster, backups are
# continuous (CloudNativePG WAL archiving to object storage) — see
# docs/RUNBOOKS.md RB-2. Use this to take a portable snapshot before a risky
# local change, or to exercise the restore path (infra/dr/verify-restore.sh).
#
#   infra/dr/pg-backup.sh [OUTDIR]     # default: ./backups/pg/<timestamp>
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE="docker compose -f ${REPO_ROOT}/deploy/compose/docker-compose.yml"
OUTDIR="${1:-${REPO_ROOT}/backups/pg/$(date -u +%Y%m%dT%H%M%SZ)}"
PGUSER="${POSTGRES_USER:-commerce}"

mkdir -p "$OUTDIR"
echo "==> backing up to $OUTDIR"

# Every application database (skip the templates + the postgres bookkeeping db;
# keycloak is included — it holds the realm + users).
DBS=$($COMPOSE exec -T postgres psql -U "$PGUSER" -d postgres -tAc \
  "SELECT datname FROM pg_database WHERE datname NOT IN ('postgres','template0','template1') ORDER BY datname")

for db in $DBS; do
  out="$OUTDIR/${db}.dump"
  # -Fc custom format: compressed, selective pg_restore, parallel-restorable.
  $COMPOSE exec -T postgres pg_dump -U "$PGUSER" -d "$db" -Fc -Z6 > "$out"
  printf '    %-14s %s\n' "$db" "$(du -h "$out" | cut -f1)"
done

# A manifest so a restore knows what it's looking at.
{
  echo "created_utc=$(date -u +%FT%TZ)"
  echo "pg_version=$($COMPOSE exec -T postgres psql -U "$PGUSER" -d postgres -tAc 'SHOW server_version')"
  echo "databases=$(echo "$DBS" | tr '\n' ' ')"
} > "$OUTDIR/MANIFEST"

echo "==> done. Restore: docs/RUNBOOKS.md RB-1"
