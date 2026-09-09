#!/usr/bin/env bash
# OWASP ZAP scan for the commerce platform.
#
# Phase 0: a passive baseline scan against the edge gateway (:8080). It proves
# security headers, cookie flags, TLS hints and information-disclosure checks on
# whatever the edge serves. The OpenAPI-driven scan is wired in Phase 1 once the
# BFF serves /api/v1/openapi.json; the authenticated context is Phase 4.
#
# Usage:
#   task up          # bring up deploy/compose first
#   ./infra/security/zap-scan.sh
#
# Reports land in ./reports/ (gitignored).
#
# IMPORTANT: do NOT set GATEWAY_URL to "localhost" from CI. ZAP runs inside its
# own container; "localhost" there is the ZAP container, not the host publishing
# port 8080. The host.docker.internal default below is what makes it reachable.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPORT_DIR="$REPO_ROOT/reports"
mkdir -p "$REPORT_DIR"
# The zaproxy image writes reports as a container-internal UID; without this the
# write fails with EACCES against the bind mount even when the scan succeeded.
chmod 777 "$REPORT_DIR"

ZAP_IMAGE="ghcr.io/zaproxy/zaproxy:stable"
GATEWAY_URL="${GATEWAY_URL:-http://host.docker.internal:8080}"
API_SPEC_URL="${API_SPEC_URL:-}" # e.g. http://host.docker.internal:8080/api/v1/openapi.json (Phase 1)

echo "==> Pulling $ZAP_IMAGE"
docker pull "$ZAP_IMAGE"

# --add-host makes host.docker.internal resolve on native Linux Docker
# (GitHub Actions runners included); harmless on Docker Desktop.
HOST_GATEWAY=(--add-host host.docker.internal:host-gateway)

echo "==> Baseline scan: $GATEWAY_URL"
docker run --rm "${HOST_GATEWAY[@]}" -v "$REPORT_DIR:/zap/wrk:rw" "$ZAP_IMAGE" \
  zap-baseline.py -t "$GATEWAY_URL" \
  -r edge-baseline-report.html -J edge-baseline-report.json \
  -I || true # baseline exits non-zero on WARN too; the CI job parses the JSON for HIGH

if [[ -n "$API_SPEC_URL" ]]; then
  echo "==> API scan (OpenAPI-driven): $API_SPEC_URL"
  docker run --rm "${HOST_GATEWAY[@]}" -v "$REPORT_DIR:/zap/wrk:rw" "$ZAP_IMAGE" \
    zap-api-scan.py -t "$API_SPEC_URL" -f openapi \
    -r api-scan-report.html -J api-scan-report.json \
    -I || true
else
  echo "==> Skipping API scan (API_SPEC_URL unset — wired in Phase 1)"
fi

echo "==> Reports in $REPORT_DIR"
