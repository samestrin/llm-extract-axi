#!/usr/bin/env bash
# Tests benchmarks/perf.sh against a stub llm-extract.
#
# What it defends: the performance table in benchmarks/results-performance.md is
# quoted in the README, so it must never show a figure that was not measured.
# A case that records zero timings (its process failed, printed no links, or
# was given no runs) must fail the run and leave the committed results alone,
# rather than writing a table with an empty or made-up row. And the table must
# be generated from the JSON, so a hand edit is overwritten by the next run.
#
# No network: the stub never fetches, and perf.sh serves its pages on loopback.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
perf="$here/perf.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fails=0

fail() {
  echo "FAIL [$1]: $2"
  if [ "$#" -ge 3 ]; then
    echo "----- actual output -----"
    echo "$3"
    echo "---------------------------"
  fi
  fails=$((fails + 1))
}

# expect <label> <want-rc: 0|nonzero> <substring-in-output> <rc> <output>
expect() {
  local label=$1 want=$2 sub=$3 rc=$4 out=$5
  if [ "$want" = 0 ] && [ "$rc" -ne 0 ]; then
    fail "$label" "expected exit 0, got $rc" "$out"
    return
  fi
  if [ "$want" != 0 ] && [ "$rc" -eq 0 ]; then
    fail "$label" "expected non-zero exit, got 0" "$out"
    return
  fi
  case "$out" in
    *"$sub"*) : ;;
    *) fail "$label" "output did not contain '$sub'" "$out" ;;
  esac
}

# The stub stands in for llm-extract. STUB_MODE picks how extract-links
# behaves: "ok" prints one link as --axi does, "fail" exits 1, "nolinks" exits
# 0 with no links list.
stub="$work/llm-extract"
cat > "$stub" <<'SH'
#!/usr/bin/env bash
case "${1:-}" in
  --version) echo "9.9.9-stub" ;;
  extract-links)
    case "${STUB_MODE:-ok}" in
      ok) printf 'links[1]{href,text,score,section}:\n  https://docs.example.com/a,A,1,Main\n' ;;
      fail) echo "fetch failed" >&2; exit 1 ;;
      nolinks) echo "count: 0" ;;
    esac ;;
  *) echo "unknown command" >&2; exit 2 ;;
esac
SH
chmod +x "$stub"

json="$work/results.json"
md="$work/results.md"

# run_perf <args...> -> sets rc and out
run_perf() {
  out=$("$perf" -bin "$stub" -json "$json" -md "$md" "$@" 2>&1)
  rc=$?
}

# --- A good run writes both files, with the stamp lines ----------------------
STUB_MODE=ok run_perf -warmup 1 -runs 3
expect "good run" 0 "" "$rc" "$out"
for want in "9.9.9-stub" "**Date:**" "**Machine:**" "threads" "RAM" "kernel" \
  "**Method:**" "1 warmup" "3 timed runs" "wall-clock" "Cold start" "Typical command" "Large input"; do
  grep -qF -- "$want" "$md" 2>/dev/null || fail "stamp lines" "results.md has no '$want'" "$(cat "$md" 2>/dev/null)"
done
if [ "$(grep -o '"timings_ms"' "$json" 2>/dev/null | wc -l)" -ne 3 ]; then
  fail "raw timings" "results.json does not hold three cases' timings" "$(cat "$json" 2>/dev/null)"
fi

# --- One command regenerates the table from the JSON; a hand edit is lost ----
cp "$md" "$work/generated.md"
echo "| Hand-typed | 0.1 ms |" >> "$md"
out=$("$perf" -render -json "$json" -md "$md" 2>&1); rc=$?
expect "render" 0 "" "$rc" "$out"
cmp -s "$md" "$work/generated.md" || fail "render" "-render did not rewrite the hand-edited table" "$(diff "$work/generated.md" "$md")"

# --- A second run rewrites the table from what the JSON says -----------------
sed 's/9\.9\.9-stub/9.9.9-second/' "$json" > "$work/edited.json" && mv "$work/edited.json" "$json"
out=$("$perf" -render -json "$json" -md "$md" 2>&1); rc=$?
expect "rerender" 0 "" "$rc" "$out"
grep -qF "9.9.9-second" "$md" || fail "rerender" "the table did not follow the JSON" "$(cat "$md")"

# --- A case whose process fails records zero timings: the run fails ----------
cp "$json" "$work/before.json"; cp "$md" "$work/before.md"
STUB_MODE=fail run_perf -warmup 0 -runs 2
expect "failing case" nonzero "Typical command" "$rc" "$out"
cmp -s "$json" "$work/before.json" || fail "failing case" "results.json was rewritten by a failed run"
cmp -s "$md" "$work/before.md" || fail "failing case" "results.md was rewritten by a failed run"

# --- A case that exits 0 but finds no links measured nothing: the run fails --
STUB_MODE=nolinks run_perf -warmup 0 -runs 2
expect "no links" nonzero "no links" "$rc" "$out"
cmp -s "$json" "$work/before.json" || fail "no links" "results.json was rewritten by a failed run"

# --- No timed runs at all is zero timings: the run fails ---------------------
STUB_MODE=ok run_perf -warmup 0 -runs 0
expect "zero runs" nonzero "zero timings" "$rc" "$out"
cmp -s "$json" "$work/before.json" || fail "zero runs" "results.json was rewritten by a failed run"

# --- A JSON with a case of zero timings cannot be rendered into a table ------
cat > "$work/zero.json" <<'JSON'
{
  "version": "9.9.9-stub",
  "date": "2026-11-13",
  "machine": {"cpu": "Stub CPU", "threads": 4, "ram_bytes": 8589934592, "os": "Stub OS", "kernel": "1.0", "arch": "amd64"},
  "method": {"warmup": 1, "runs": 3, "clock": "wall-clock per process"},
  "cases": [
    {"name": "Cold start", "command": "llm-extract --version", "timings_ms": [1.5, 1.6, 1.7]},
    {"name": "Large input", "command": "llm-extract extract-links", "input_bytes": 5242880, "timings_ms": []}
  ]
}
JSON
out=$("$perf" -render -json "$work/zero.json" -md "$md" 2>&1); rc=$?
expect "zero-timing JSON" nonzero "zero timings" "$rc" "$out"
cmp -s "$md" "$work/before.md" || fail "zero-timing JSON" "results.md was rewritten from a JSON with zero timings"

if [ "$fails" -gt 0 ]; then
  echo "perf_test.sh: $fails check(s) failed"
  exit 1
fi
echo "perf_test.sh: all checks passed"
exit 0
