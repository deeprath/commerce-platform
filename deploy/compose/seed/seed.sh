#!/bin/sh
# Idempotent demo-catalog seeder for the compose stack.
#
# For each ./products/<slug>.json it:
#   1. POST /api/v1/admin/catalog/products   -> creates the product (DRAFT)
#   2. PUT  /api/v1/admin/catalog/products/{id} with status ACTIVE -> publishes it
# Publishing makes it visible in browse/search; catalog emits product_changed,
# which the inventory consumer turns into a stock row seeded to
# DEFAULT_STOCK_ON_HAND. Re-runs are safe: an existing product (409) is fetched
# by slug and (re)published.
#
# Needs only curl + sh (curlimages/curl).
set -eu

BFF_URL="${BFF_URL:-http://bff:8080}"
TOKEN_URL="${TOKEN_URL:-http://keycloak:8080/realms/commerce/protocol/openid-connect/token}"
SEED_USER="${SEED_USER:-adminuser}"
SEED_PASS="${SEED_PASS:-adminuser123}"
BFF_CLIENT_ID="${BFF_CLIENT_ID:-bff}"
BFF_CLIENT_SECRET="${BFF_CLIENT_SECRET:-local-dev-bff-secret}"
PRODUCTS_DIR="${PRODUCTS_DIR:-/seed/products}"

json_str() { # extract a top-level string field: json_str <key> < file/stdin
  sed -n 's/.*"'"$1"'":"\([^"]*\)".*/\1/p' | head -n 1
}

echo "seed: waiting for the BFF at ${BFF_URL}/healthz ..."
i=0
until curl -sf -o /dev/null "${BFF_URL}/healthz"; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || { echo "seed: BFF never became healthy"; exit 1; }
  sleep 3
done

echo "seed: getting a catalog_manager token as ${SEED_USER} ..."
TOKEN=$(curl -sf -X POST "$TOKEN_URL" \
  -d grant_type=password \
  -d "client_id=${BFF_CLIENT_ID}" \
  -d "client_secret=${BFF_CLIENT_SECRET}" \
  -d "username=${SEED_USER}" \
  -d "password=${SEED_PASS}" \
  -d scope=openid | json_str access_token)
[ -n "$TOKEN" ] || { echo "seed: could not obtain a token"; exit 1; }
AUTH="Authorization: Bearer ${TOKEN}"

rc=0
for f in "$PRODUCTS_DIR"/*.json; do
  slug=$(basename "$f" .json)

  code=$(curl -s -o /tmp/resp -w '%{http_code}' \
    -X POST "${BFF_URL}/api/v1/admin/catalog/products" \
    -H "$AUTH" -H 'Content-Type: application/json' --data-binary "@${f}")
  case "$code" in
    200 | 201) id=$(json_str id < /tmp/resp); echo "seed: created ${slug}" ;;
    409)
      id=$(curl -s -H "$AUTH" "${BFF_URL}/api/v1/catalog/products/${slug}" | json_str id)
      echo "seed: ${slug} exists" ;;
    *) echo "seed: FAILED create ${slug} (HTTP ${code}): $(cat /tmp/resp)"; rc=1; continue ;;
  esac

  [ -n "${id:-}" ] || { echo "seed: no id for ${slug}"; rc=1; continue; }

  # Publish: same body + status ACTIVE, minus "slug" (not an UpdateProduct field).
  pcode=$(sed -e '1 s/{/{"status":"PRODUCT_STATUS_ACTIVE",/' -e '/^  "slug":/d' "$f" \
    | curl -s -o /tmp/resp -w '%{http_code}' \
    -X PUT "${BFF_URL}/api/v1/admin/catalog/products/${id}" \
    -H "$AUTH" -H 'Content-Type: application/json' --data-binary @-)
  case "$pcode" in
    200) echo "seed: published ${slug}" ;;
    *) echo "seed: FAILED publish ${slug} (HTTP ${pcode}): $(cat /tmp/resp)"; rc=1 ;;
  esac
done

[ "$rc" -eq 0 ] && echo "seed: done"
exit "$rc"
