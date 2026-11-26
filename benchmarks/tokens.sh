#!/usr/bin/env bash
#
# Token benchmark: a raw documentation page against `extract-links --axi`, and
# raw HTML against `fetch --format markdown` on the readable package's 10
# fixtures. Offline: every page is a committed fixture served on loopback,
# and the tokenizer is built into benchmarks/tokens, which imports only the Go
# standard library. A successful run writes benchmarks/results-tokens.json and
# renders benchmarks/results-tokens.md; `make bench-tokens` does the same after
# a build.
#
# Usage:  ./benchmarks/tokens.sh [-fixture path/to/page.html]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [ ! -x build/llm-extract ]; then
  echo "building build/llm-extract first..." >&2
  make build >/dev/null
fi

exec go run ./benchmarks/tokens "$@"
