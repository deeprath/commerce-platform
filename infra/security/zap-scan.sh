#!/usr/bin/env bash
# OWASP ZAP scan for the commerce platform.
#
# Two passes:
#   1. Passive baseline against the Envoy edge (:8080) — security headers, cookie
#      flags, cache hints, info-disclosure on whatever the edge serves.
#   2. Authenticated active scan of the BFF API, driven by the ZAP Automation
#      Framework plan (infra/security/zap/api-scan.yaml) + a hand-maintained
#      OpenAPI description (infra/security/openapi/bff.yaml). ZAP logs in as a
#      seeded shopper and actively scans every endpoint/method/param.
#
# Usage:
#   task up                       # bring up deploy/compose first
#   ./infra/security/zap-scan.sh                 # both passes
#   ZAP_SKIP_ACTIVE=1 ./infra/security/zap-scan.sh   # baseline only (fast)
#
# Reports land in ./reports/ (gitignored).
#
# IMPORTANT: do NOT target "localhost" from CI. ZAP runs inside its own
# container; "localhost" there is the ZAP container, not the host publishing the
# port. host.docker.internal (added via --add-host on native Linux Docker) is
# what makes the host reachable.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPORT_DIR="$REPO_ROOT/reports"
mkdir -p "$REPORT_DIR"
# The zaproxy image writes reports as a container-internal UID; without this the
# write fails with EACCES against the bind mount even when the scan succeeded.
chmod 777 "$REPORT_DIR"

ZAP_IMAGE="${ZAP_IMAGE:-ghcr.io/zaproxy/zaproxy:stable}"
GATEWAY_URL="${GATEWAY_URL:-http://host.docker.internal:8080}"
ZAP_AUTH_USERNAME="${ZAP_AUTH_USERNAME:-testuser}"
ZAP_AUTH_PASSWORD="${ZAP_AUTH_PASSWORD:-testuser123}"

echo "==> Pulling $ZAP_IMAGE"
docker pull -q "$ZAP_IMAGE"

# --add-host makes host.docker.internal resolve on native Linux Docker
# (GitHub Actions runners included); harmless on Docker Desktop.
HOST_GATEWAY=(--add-host host.docker.internal:host-gateway)

echo "==> [1/2] Passive baseline: $GATEWAY_URL"
docker run --rm "${HOST_GATEWAY[@]}" -v "$REPORT_DIR:/zap/wrk:rw" "$ZAP_IMAGE" \
  zap-baseline.py -t "$GATEWAY_URL" \
  -r edge-baseline-report.html -J edge-baseline-report.json \
  -I || true # baseline exits non-zero on WARN too; the CI job parses the JSON for HIGH

if [[ "${ZAP_SKIP_ACTIVE:-0}" == "1" ]]; then
  echo "==> [2/2] Skipping authenticated active scan (ZAP_SKIP_ACTIVE=1)"
  echo "==> Reports in $REPORT_DIR"
  exit 0
fi

echo "==> [2/2] Authenticated active scan (Automation Framework)"
# The plan lives at /zap/wrk/zap/api-scan.yaml and imports /zap/wrk/openapi/bff.yaml;
# reports are written to /zap/wrk/reports (-> ./reports on the host).
docker run --rm "${HOST_GATEWAY[@]}" \
  -e ZAP_AUTH_USERNAME="$ZAP_AUTH_USERNAME" \
  -e ZAP_AUTH_PASSWORD="$ZAP_AUTH_PASSWORD" \
  -v "$REPORT_DIR:/zap/wrk/reports:rw" \
  -v "$SCRIPT_DIR/zap:/zap/wrk/zap:ro" \
  -v "$SCRIPT_DIR/openapi:/zap/wrk/openapi:ro" \
  "$ZAP_IMAGE" \
  zap.sh -cmd -autorun /zap/wrk/zap/api-scan.yaml || true # gate is the JSON parse in CI

echo "==> Reports in $REPORT_DIR"
ls -1 "$REPORT_DIR"
