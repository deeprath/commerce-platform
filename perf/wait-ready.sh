#!/usr/bin/env bash
# Block until the BFF is healthy and the catalog has seeded at least one
# product (checkout needs stock rows, which the inventory consumer creates
# from catalog.product_changed). Used by the perf CI job and locally.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8088/api/v1}"
HEALTH_URL="${HEALTH_URL:-http://localhost:8088/healthz}"
TIMEOUT="${TIMEOUT:-180}"

deadline=$(( $(date +%s) + TIMEOUT ))

echo "waiting for BFF at ${HEALTH_URL} ..."
until curl -fsS -o /dev/null "${HEALTH_URL}"; do
  [ "$(date +%s)" -lt "$deadline" ] || { echo "BFF not healthy within ${TIMEOUT}s"; exit 1; }
  sleep 2
done
echo "BFF healthy."

echo "waiting for a seeded catalog at ${BASE_URL}/catalog/products ..."
until [ "$(curl -fsS "${BASE_URL}/catalog/products?page_size=1" | grep -o '"product_id"' | head -n1)" = '"product_id"' ]; do
  [ "$(date +%s)" -lt "$deadline" ] || { echo "catalog not seeded within ${TIMEOUT}s"; exit 1; }
  sleep 3
done
echo "catalog seeded."
