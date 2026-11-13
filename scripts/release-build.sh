#!/usr/bin/env bash
# Build the release assets: llm-extract and llm-extract-mcp for every platform,
# stamped with the given version, and SHA256SUMS.txt over all of them.
#
#   scripts/release-build.sh <version> <out-dir>
#
# release.yml runs this; tests/release runs it for the host. It checks its own
# output rather than trusting the loop: one binary per name and platform, one
# checksum line each, and every line verifies.
#
# RELEASE_PLATFORMS overrides the platform list (used by the tests).
set -euo pipefail

version="${1:-}"
out="${2:-}"
[[ -n "$version" && -n "$out" ]] || { echo "usage: $0 <version> <out-dir>" >&2; exit 2; }
# "dev" is the unstamped default; a release claiming it is indistinguishable
# from a build that failed to stamp.
[[ "$version" != "dev" ]] || { echo "Error: refusing to stamp the unstamped default \"dev\"" >&2; exit 1; }

cd "$(dirname "$0")/.."
MODULE="github.com/samestrin/llm-extract-axi"
# The CLI version lives on commands.Version, not on main: the cobra root reads
# it there, so stamping main would leave `llm-extract --version` saying "dev".
CLI_LDFLAGS="-s -w -X $MODULE/internal/extract/commands.Version=$version"
MCP_LDFLAGS="-s -w -X main.serverVersion=$version"
BINS=(llm-extract llm-extract-mcp)
PLATFORMS="${RELEASE_PLATFORMS:-darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64}"

# Stock macOS ships shasum, not sha256sum, and ./benchmarks/perf.sh builds
# through this script. Both write and check the same "hash  name" lines.
if command -v sha256sum >/dev/null 2>&1; then
    sha256() { sha256sum "$@"; }
elif command -v shasum >/dev/null 2>&1; then
    sha256() { shasum -a 256 "$@"; }
else
    echo "Error: neither sha256sum nor shasum is installed, so SHA256SUMS.txt cannot be written" >&2
    exit 1
fi

rm -rf "$out"
mkdir -p "$out"
count=0
for platform in $PLATFORMS; do
    os=${platform%/*}; arch=${platform#*/}
    ext=""; [[ "$os" == "windows" ]] && ext=".exe"
    for bin in "${BINS[@]}"; do
        flags="$MCP_LDFLAGS"; [[ "$bin" == "llm-extract" ]] && flags="$CLI_LDFLAGS"
        echo "Building $bin $os/$arch ($version)"
        CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$flags" -o "$out/$bin-$os-$arch$ext" "./cmd/$bin"
        count=$((count + 1))
    done
done

cd "$out"
sha256 -- * > SHA256SUMS.txt
cat SHA256SUMS.txt
test "$(find . -maxdepth 1 -type f -name 'llm-extract*' | wc -l)" -eq "$count"
test "$(wc -l < SHA256SUMS.txt)" -eq "$count"
sha256 -c --quiet SHA256SUMS.txt
echo "Built $count binaries and SHA256SUMS.txt in $out"
