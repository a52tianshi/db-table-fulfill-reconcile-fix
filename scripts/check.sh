#!/usr/bin/env bash
# Shared local/CI entry point. No downloaded lint tools or hidden fixes.
set -euo pipefail
cd "$(dirname "$0")/.."

modules() {
  local module
  for module in . e2e; do
    (cd "$module" && "$@")
  done
}

build() { modules go build ./...; }
vet() { modules go vet ./...; }
test_units() {
  go test -race -count=1 ./...
  (cd e2e && go test -race -count=1 ./internal/... ./cmd/...)
}
lint() {
  local unformatted
  unformatted=$(gofmt -l .)
  if [[ -n "$unformatted" ]]; then
    printf 'gofmt required:\n%s\n' "$unformatted" >&2
    return 1
  fi
  bash -n scripts/check.sh e2e/run.sh
  printf 'lint PASS (gofmt + bash -n)\n'
}
integration() {
  local log
  log=$(mktemp)
  trap 'rm -f "$log"' RETURN
  (cd e2e && go test -race -count=1 -v ./mysqltable_it) 2>&1 | tee "$log"
  # The existing tests intentionally skip without MySQL. A release gate must
  # not mistake that successful Go exit status for database validation.
  if grep -q -- '--- SKIP:' "$log"; then
    printf 'integration FAIL: skipped MySQL tests; database evidence is missing\n' >&2
    return 1
  fi
}

case "${1:-all}" in
  build) build ;;
  vet) vet ;;
  test) test_units ;;
  lint) lint ;;
  integration) integration ;;
  all) build; vet; test_units; lint ;;
  *) printf 'Usage: %s [all|build|vet|test|lint|integration]\n' "$0" >&2; exit 2 ;;
esac
