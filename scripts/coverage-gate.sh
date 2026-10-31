#!/usr/bin/env bash
# Fails when total statement coverage in a Go cover profile is below a floor.
#
# Usage: scripts/coverage-gate.sh <profile> <min-percent>
#
# This gate FAILS rather than warns. A gate that only prints a percentage is
# how coverage drifts unobserved.
set -euo pipefail

profile=${1:?usage: coverage-gate.sh <profile> <min-percent>}
min=${2:?usage: coverage-gate.sh <profile> <min-percent>}

if [ ! -s "$profile" ]; then
  echo "::error::coverage profile $profile is missing or empty"
  exit 1
fi

# A profile with no statement lines means nothing was measured. That is a
# failure here, not a pass: this repo always has code, so an empty profile
# means the tests did not run.
stmts=$(grep -vc '^mode:' "$profile" || true)
if [ "${stmts:-0}" -eq 0 ]; then
  echo "::error::coverage profile $profile holds no statements"
  exit 1
fi

total=$(go tool cover -func="$profile" | awk '/^total:/ {sub(/%/, "", $3); print $3}')
if [ -z "$total" ]; then
  echo "::error::could not read a total from $profile"
  exit 1
fi

echo "Total coverage: ${total}% across ${stmts} statement lines (floor ${min}%)"
if awk -v t="$total" -v m="$min" 'BEGIN { exit !(t < m) }'; then
  echo "::error::Coverage ${total}% is below the required ${min}%"
  exit 1
fi
