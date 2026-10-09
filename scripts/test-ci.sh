#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
results=$(mktemp)
trap 'rm -f "$results"' EXIT

# pipefail preserves test failures while keeping the full output visible.
go test ./... -v -count=1 "$@" | tee "$results"
if grep -Eq '^[[:space:]]*--- SKIP:' "$results"; then
  echo 'CI validation failed: tests were skipped. Check PostgreSQL on localhost:5433.' >&2
  exit 1
fi
if ! grep -Eq '^[[:space:]]*--- PASS:' "$results"; then
  echo 'CI validation failed: no tests executed.' >&2
  exit 1
fi
