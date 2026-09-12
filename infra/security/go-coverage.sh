#!/usr/bin/env bash
# Generates coverage.out for the whole go.work workspace, for SonarCloud.
#
# This must run go test PER MODULE (cd into each module's own directory),
# not as a single invocation from the repo root. go test's -coverpkg=./...
# is resolved relative to the current working directory's module, so running
# it once from the repo root would expand "./..." to every module in
# go.work — all 16+ services plus pkg plus the generated protobuf code —
# and every package's coverage percentage would be measured against that
# entire workspace's statement count instead of its own. Verified empirically:
# services/order/internal/saga (genuinely ~92% covered) reported 5.3% that
# way, because the denominator ballooned to the whole workspace.
#
# Scoped per module instead, -coverpkg=./... only expands to that module's
# own packages (e.g. services/inventory's domain/store/grpcsvc/cmd), which
# is exactly the gap this script fixes: without -coverpkg at all, a fix in
# internal/store that's only exercised by a sibling package's test
# (internal/grpcsvc, same module) shows as uncovered to SonarCloud even
# though it's genuinely exercised at runtime (see the services/inventory
# reservationIsHeld fix, PR #54). Coverage credit never crosses a module
# boundary (e.g. a service's test exercising pkg/auth doesn't credit
# pkg/auth here) — pkg has its own tests for that, and crediting across
# modules would reintroduce the same denominator-inflation problem.
set -euo pipefail

out="${1:-coverage.out}"
rm -f "$out"

first=1
for dir in $(go list -m -f '{{.Dir}}'); do
  mod_cov="$(mktemp)"
  # Never let one module's test failure abort coverage collection for the
  # rest — this step is informational (feeds SonarCloud), not a test gate;
  # actual pass/fail is enforced by ci.yml's separate test job.
  (cd "$dir" && go test -coverprofile="$mod_cov" -coverpkg=./... -p 4 ./...) || true
  if [ -s "$mod_cov" ]; then
    if [ "$first" = "1" ]; then
      cat "$mod_cov" >>"$out"
      first=0
    else
      tail -n +2 "$mod_cov" >>"$out" # drop the repeated "mode:" header line
    fi
  fi
  rm -f "$mod_cov"
done

touch "$out" # keep the step green even if every module had no test files
