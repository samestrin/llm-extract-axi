#!/usr/bin/env bash
#
# Token benchmark: a raw documentation page against `extract-links --axi` on
# the same page. Offline: the page is a committed fixture served on loopback,
# and the tokenizer is built into benchmarks/tokens, which imports only the Go
# standard library.
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
