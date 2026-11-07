#!/usr/bin/env bash
# Tests the failure branches of scripts/coverage-gate.sh.
#
# What it defends (technical-debt row, EST-5246): the coverage gate is a HARD
# fail, not a warning, but its exit-1 branches (empty/missing profile, a profile
# with no statements, a profile from which no total can be read, and a total
# below the floor) had no automated test. If a regression turned the gate into a
# silent pass, coverage would drift unobserved and nothing would go red. This
# feeds each bad shape and asserts a non-zero exit AND the error it names, plus a
# real at/above-floor profile that must exit 0 — so a gate that always fails
# would also be caught.
#
# No network: the "real" profile is generated from a two-function throwaway
# module on disk, so `go tool cover` can parse it. Nothing reaches outside.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
gate="$here/coverage-gate.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fails=0

# expect_nonzero <label> <substring-in-output> <rc> <output>
expect_nonzero() {
  local label=$1 sub=$2 rc=$3 out=$4
  if [ "$rc" -eq 0 ]; then
    echo "FAIL [$label]: expected non-zero exit, got 0"
    fails=$((fails + 1))
    return
  fi
  case "$out" in
    *"$sub"*) : ;;
    *)
      echo "FAIL [$label]: output did not contain '$sub'"
      echo "----- actual output -----"
      echo "$out"
      echo "---------------------------"
      fails=$((fails + 1))
      ;;
  esac
}

# expect_zero <label> <substring-in-output> <rc> <output>
expect_zero() {
  local label=$1 sub=$2 rc=$3 out=$4
  if [ "$rc" -ne 0 ]; then
    echo "FAIL [$label]: expected exit 0, got $rc"
    echo "----- actual output -----"
    echo "$out"
    echo "---------------------------"
    fails=$((fails + 1))
    return
  fi
  case "$out" in
    *"$sub"*) : ;;
    *)
      echo "FAIL [$label]: output did not contain '$sub'"
      echo "----- actual output -----"
      echo "$out"
      echo "---------------------------"
      fails=$((fails + 1))
      ;;
  esac
}

# run_gate <profile> <floor> [cwd] -> prints "rc<TAB>output". When a cwd is
# given the gate runs from there, which `go tool cover` needs to resolve the
# package paths recorded in a profile generated from a separate module.
run_gate() {
  local out rc
  if [ "$#" -ge 3 ]; then
    out=$(cd "$3" && "$gate" "$1" "$2" 2>&1)
  else
    out=$("$gate" "$1" "$2" 2>&1)
  fi
  rc=$?
  printf '%s\t%s' "$rc" "$out"
}

# expect_nonzero_only <label> <rc> <output> — for a bad profile the gate must
# reject without asserting its exact wording (e.g. a profile go tool cover
# cannot read, which the gate kills via `set -e` before its own message).
expect_nonzero_only() {
  local label=$1 rc=$2 out=$3
  if [ "$rc" -eq 0 ]; then
    echo "FAIL [$label]: expected non-zero exit, got 0"
    echo "----- actual output -----"
    echo "$out"
    echo "---------------------------"
    fails=$((fails + 1))
  fi
}

# --- Branch 1: missing profile file ------------------------------------------
res=$(run_gate "$work/does-not-exist.txt" 80); rc=${res%%$'\t'*}; out=${res#*$'\t'}
expect_nonzero "missing profile" "is missing or empty" "$rc" "$out"

# --- Branch 1: present but empty file ----------------------------------------
: > "$work/empty.txt"
res=$(run_gate "$work/empty.txt" 80); rc=${res%%$'\t'*}; out=${res#*$'\t'}
expect_nonzero "empty profile" "is missing or empty" "$rc" "$out"

# --- Branch 2: a profile with a header line but no statement lines -----------
printf 'mode: set\n' > "$work/nostmt.txt"
res=$(run_gate "$work/nostmt.txt" 80); rc=${res%%$'\t'*}; out=${res#*$'\t'}
expect_nonzero "no-statement profile" "holds no statements" "$rc" "$out"

# --- Branch 3: statement lines present, but no total is readable -------------
# A profile whose blocks point at a package that is not on disk: stmts > 0 so it
# clears branch 2, but `go tool cover -func` cannot resolve a total, so the gate
# must still exit non-zero rather than pass on an unreadable profile.
printf 'mode: set\nno_such_pkg/no_such_file.go:1.1,2.1 1 1\n' > "$work/nototal.txt"
res=$(run_gate "$work/nototal.txt" 80); rc=${res%%$'\t'*}; out=${res#*$'\t'}
expect_nonzero_only "unreadable profile" "$rc" "$out"

# --- A real, parseable profile: one covered and one uncovered function -------
mkdir -p "$work/mod"
cat > "$work/mod/a.go" <<'GO'
package covprobetest

func Covered() int { return 1 }

func Uncovered() int { return 2 }
GO
cat > "$work/mod/a_test.go" <<'GO'
package covprobetest

import "testing"

func TestCovered(t *testing.T) {
	if Covered() != 1 {
		t.Fatal("Covered() changed")
	}
}
GO
if ! (cd "$work/mod" && GOWORK=off GOFLAGS= go mod init covprobetest >/dev/null 2>&1 \
	&& GOWORK=off GOFLAGS= go test -coverprofile="$work/real.txt" ./... >/dev/null 2>&1); then
	echo "FAIL [setup]: could not generate a real coverage profile in $work/mod"
	fails=$((fails + 1))
fi

if [ -s "$work/real.txt" ]; then
	# go tool cover resolves the recorded package paths against the module, so
	# the gate must run from inside the throwaway module for these two cases.
	# Branch 4: real total (50%) below the floor (100%) -> non-zero.
	res=$(run_gate "$work/real.txt" 100 "$work/mod"); rc=${res%%$'\t'*}; out=${res#*$'\t'}
	expect_nonzero "below-floor profile" "is below the required" "$rc" "$out"

	# Pass branch: floor at or below the measured total -> exit 0, reports total.
	res=$(run_gate "$work/real.txt" 1 "$work/mod"); rc=${res%%$'\t'*}; out=${res#*$'\t'}
	expect_zero "at/above-floor profile" "Total coverage:" "$rc" "$out"
fi

if [ "$fails" -gt 0 ]; then
	echo "coverage-gate_test.sh: $fails check(s) failed"
	exit 1
fi
echo "coverage-gate_test.sh: all checks passed"
exit 0
