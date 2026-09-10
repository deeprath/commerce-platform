#!/usr/bin/env bash
# Restore drill: prove a logical backup actually restores. Non-destructive to
# real data — it works on a throwaway `dr_drill` database cloned from `order`.
#
#   infra/dr/verify-restore.sh
#
# Exits non-zero if the restored row count doesn't match the source. Run it
# after infra/dr/pg-backup.sh, and on a schedule (docs/RUNBOOKS.md §"Drills").
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PSQL="docker compose -f ${REPO_ROOT}/deploy/compose/docker-compose.yml exec -T postgres"
PGUSER="${POSTGRES_USER:-commerce}"
SRC_DB="${SRC_DB:-order}"
DRILL_DB="dr_drill"

q() { $PSQL psql -U "$PGUSER" -d "$1" -tAc "$2"; }

echo "==> source: ${SRC_DB}"
src_tables=$(q "$SRC_DB" "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")
echo "    ${src_tables} public tables"

echo "==> dump ${SRC_DB} -> restore into fresh ${DRILL_DB}"
$PSQL psql -U "$PGUSER" -d postgres -c "DROP DATABASE IF EXISTS ${DRILL_DB}" >/dev/null
$PSQL psql -U "$PGUSER" -d postgres -c "CREATE DATABASE ${DRILL_DB}" >/dev/null
$PSQL pg_dump -U "$PGUSER" -d "$SRC_DB" -Fc \
  | $PSQL pg_restore -U "$PGUSER" -d "$DRILL_DB" --no-owner --exit-on-error

echo "==> compare row counts, per table"
fail=0
for t in $(q "$SRC_DB" "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename"); do
  a=$(q "$SRC_DB" "SELECT count(*) FROM \"$t\"")
  b=$(q "$DRILL_DB" "SELECT count(*) FROM \"$t\"")
  if [ "$a" = "$b" ]; then
    printf '    ok   %-24s %s\n' "$t" "$a"
  else
    printf '    FAIL %-24s src=%s restored=%s\n' "$t" "$a" "$b"; fail=1
  fi
done

$PSQL psql -U "$PGUSER" -d postgres -c "DROP DATABASE ${DRILL_DB}" >/dev/null
if [ "$fail" -eq 0 ]; then
  echo "==> RESTORE VERIFIED"
else
  echo "==> RESTORE MISMATCH"
  exit 1
fi
