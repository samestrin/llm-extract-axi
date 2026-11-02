#!/usr/bin/env bash
# Install llm-extract and llm-extract-mcp from a GitHub release. No sudo, no
# clone, no Go toolchain.
#
#   curl -fsSL https://raw.githubusercontent.com/samestrin/llm-extract-axi/main/install.sh | bash
#
# Downloads both binaries for this platform and SHA256SUMS.txt, verifies both
# against it, and only then installs them to $HOME/.local/bin, the CLI first:
# the MCP server runs the CLI, so it is never installed without it. On an
# unsupported platform, a failed or stalled download, or a checksum mismatch it
# installs nothing and exits non-zero; an existing binary is only ever replaced
# by one that verified.
#
# Environment:
#   INSTALL_DIR                  where to install (default: $HOME/.local/bin)
#   LLM_EXTRACT_VERSION          release tag, e.g. v1.0.0 (default: latest)
#   LLM_EXTRACT_BASE_URL         URL the assets are fetched from; overrides the
#                                version (used by the tests)
#   LLM_EXTRACT_DOWNLOAD_TIMEOUT seconds before a download is abandoned
#                                (default: 300)
set -euo pipefail

REPO="samestrin/llm-extract-axi"
BINS=(llm-extract llm-extract-mcp) # install order: CLI first
SUMS="SHA256SUMS.txt"

die() {
    echo "Error: $*" >&2
    echo "Nothing was installed." >&2
    exit 1
}

case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) die "unsupported OS $(uname -s); download a binary from https://github.com/$REPO/releases" ;;
esac
case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "unsupported architecture $(uname -m); download a binary from https://github.com/$REPO/releases" ;;
esac

version="${LLM_EXTRACT_VERSION:-latest}"
if [[ -n "${LLM_EXTRACT_BASE_URL:-}" ]]; then
    base="${LLM_EXTRACT_BASE_URL%/}"
elif [[ "$version" == "latest" ]]; then
    base="https://github.com/$REPO/releases/latest/download"
else
    base="https://github.com/$REPO/releases/download/$version"
fi
install_dir="${INSTALL_DIR:-$HOME/.local/bin}"
timeout="${LLM_EXTRACT_DOWNLOAD_TIMEOUT:-300}"
[[ "$timeout" =~ ^[1-9][0-9]*$ ]] || die "LLM_EXTRACT_DOWNLOAD_TIMEOUT must be a whole number of seconds, got '$timeout'"

if command -v curl >/dev/null 2>&1; then
    # A stalled connection must fail, not hang the install.
    fetch() { curl -fsSL --connect-timeout 15 --max-time "$timeout" -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
    # wget has no overall limit; --timeout bounds each connect and read stall.
    fetch() { wget -q --timeout="$timeout" --tries=1 -O "$2" "$1"; }
else
    die "neither curl nor wget is installed"
fi

if command -v sha256sum >/dev/null 2>&1; then
    sha256_check() { sha256sum -c --status -; }
elif command -v shasum >/dev/null 2>&1; then
    sha256_check() { shasum -a 256 -c --status -; }
else
    die "neither sha256sum nor shasum is installed, so the download cannot be verified"
fi

tmp="$(mktemp -d)"
staged=()
backup=""
cleanup() {
    rm -rf "$tmp"
    if ((${#staged[@]})); then rm -f "${staged[@]}"; fi
    if [[ -n "$backup" ]]; then rm -f "$backup"; fi
}
trap cleanup EXIT

# Download and verify everything before installing anything, so a failure on
# the second binary cannot leave the first one replaced.
echo "Downloading llm-extract ($version) from $base"
fetch "$base/$SUMS" "$tmp/$SUMS" || die "could not download $base/$SUMS"
for bin in "${BINS[@]}"; do
    asset="$bin-$os-$arch"
    fetch "$base/$asset" "$tmp/$asset" || die "could not download $base/$asset"

    # Exactly this asset's line. sha256sum writes "hash  name", or "hash *name"
    # in binary mode.
    line="$(awk -v n="$asset" '$2 == n || $2 == "*" n' "$tmp/$SUMS")"
    [[ -n "$line" ]] || die "$SUMS has no checksum for $asset"
    [[ "$(printf '%s\n' "$line" | wc -l)" -eq 1 ]] || die "$SUMS has more than one checksum for $asset"
    (cd "$tmp" && printf '%s\n' "$line" | sha256_check) ||
        die "checksum mismatch for $asset: the download does not match $SUMS"
done
echo "Checksums verified."

# Stage both beside the destination, then rename, so each replacement is
# atomic and neither happens until both are in place.
mkdir -p "$install_dir" || die "could not create $install_dir"
for bin in "${BINS[@]}"; do
    s="$install_dir/.$bin.tmp.$$"
    staged+=("$s")
    cp "$tmp/$bin-$os-$arch" "$s" || die "could not write to $install_dir"
    chmod 755 "$s"
done

# Keep the old CLI until the server is in place too, so a failed second
# rename can put it back rather than leave a mixed install.
cli="$install_dir/${BINS[0]}"
if [[ -e "$cli" ]]; then
    backup="$install_dir/.${BINS[0]}.old.$$"
    cp -p "$cli" "$backup" || die "could not back up $cli"
fi
mv -f "${staged[0]}" "$cli" || die "could not install to $cli"
if ! mv -f "${staged[1]}" "$install_dir/${BINS[1]}"; then
    if [[ -n "$backup" ]]; then mv -f "$backup" "$cli"; else rm -f "$cli"; fi
    die "could not install to $install_dir/${BINS[1]}"
fi

for bin in "${BINS[@]}"; do echo "Installed $install_dir/$bin"; done
case ":$PATH:" in
    *":$install_dir:"*) ;;
    *) echo "Note: $install_dir is not on your PATH. Add it, e.g.: export PATH=\"$install_dir:\$PATH\"" ;;
esac
