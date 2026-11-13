#!/usr/bin/env bash
#
# Performance benchmark: times llm-extract in three offline cases (cold start,
# a typical extract-links, and extract-links on a page of at least 5 MB), writes
# every raw timing to benchmarks/results-performance.json, and generates
# benchmarks/results-performance.md from that JSON. The pages are served on
# 127.0.0.1 inside benchmarks/perf; no outside host is contacted or timed.
#
# Usage:  ./benchmarks/perf.sh [-bin path/to/llm-extract] [-warmup N] [-runs N] [-large page.html]
#         ./benchmarks/perf.sh -render     # regenerate the table from the JSON only
#
# Without -bin it builds the release binary for this machine with
# scripts/release-build.sh, stamped with `git describe`, and times that.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

build=1
for arg in "$@"; do
  case "$arg" in
    -bin | --bin | -bin=* | --bin=* | -render | --render | -render=* | --render=*) build=0 ;;
  esac
done

if [ "$build" = 1 ]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  os=$(go env GOOS)
  arch=$(go env GOARCH)
  ext=""
  [ "$os" = windows ] && ext=".exe"
  version=$(git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-unknown")
  echo "building the release binary ($version, $os/$arch)..." >&2
  RELEASE_PLATFORMS="$os/$arch" scripts/release-build.sh "$version" "$tmp/dist" >/dev/null
  set -- -bin "$tmp/dist/llm-extract-$os-$arch$ext" "$@"
fi

go run ./benchmarks/perf "$@"
