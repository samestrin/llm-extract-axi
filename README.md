# llm-extract

A CLI for agents that pulls only what is needed from a large file or a web page, without filling the agent's context.

[![test](https://github.com/samestrin/llm-extract-axi/actions/workflows/test.yml/badge.svg)](https://github.com/samestrin/llm-extract-axi/actions/workflows/test.yml)
[![release](https://img.shields.io/github/v/release/samestrin/llm-extract-axi)](https://github.com/samestrin/llm-extract-axi/releases)
[![license](https://img.shields.io/github/license/samestrin/llm-extract-axi)](LICENSE)

## Why

- Large pages and files do not fit an agent's context. `llm-extract` fetches the content and returns only the part that matters: the ranked links, or the passages relevant to a given context.
- It is the one tool in its family that may reach the network or spend money on a model, and it says which command does which before any call is made.
- A failed fetch or a failed model call is an error, never an empty result. A partial result says it is partial.
- `llm-extract` is the product. `llm-extract-mcp` is an MCP server that shells out to it, a fallback for environments that cannot run a CLI.

## Install

### Release binary

On Linux and macOS, one line, no sudo, no clone, no Go toolchain:

```bash
curl -fsSL https://raw.githubusercontent.com/samestrin/llm-extract-axi/main/install.sh | bash
```

This installs `llm-extract` and `llm-extract-mcp` into `$HOME/.local/bin` (set `INSTALL_DIR` to install elsewhere). The release assets are `llm-extract-<os>-<arch>` and `llm-extract-mcp-<os>-<arch>` for `darwin`/`linux` on `amd64`/`arm64`, plus `llm-extract-windows-amd64.exe` and `llm-extract-mcp-windows-amd64.exe` for Windows. The script downloads the release's `SHA256SUMS.txt` and verifies both binaries against it before either is installed; on an unsupported platform, a failed or stalled download, or a checksum mismatch it installs nothing and leaves an existing install untouched. Windows is not covered by the one-liner; install the `.exe` asset by hand as described below.

Pin a version instead of installing the latest release:

```bash
curl -fsSL https://raw.githubusercontent.com/samestrin/llm-extract-axi/main/install.sh | LLM_EXTRACT_VERSION=v1.0.1 bash
```

If `~/.local/bin` is not on your `PATH`, the installer says so and prints the `export` line to add.

To install by hand instead, download `llm-extract-<os>-<arch>` (or `llm-extract-windows-amd64.exe` on Windows) and `SHA256SUMS.txt` from the [latest release](https://github.com/samestrin/llm-extract-axi/releases/latest), verify with `sha256sum -c SHA256SUMS.txt --ignore-missing`, and copy the binary onto your `PATH`.

### go install

Requires Go 1.24 or later:

```bash
go install github.com/samestrin/llm-extract-axi/cmd/llm-extract@latest
```

This installs only the CLI; the release binary is the way to get `llm-extract-mcp`.

### From source

```bash
git clone https://github.com/samestrin/llm-extract-axi.git
cd llm-extract-axi
make build                    # binaries land in ./build/
./build/llm-extract --version
```

### Claude Code setup

Run `llm-extract setup` in a project (or with `--dir`) to write a SessionStart hook into `.claude/settings.json` and an Agent Skill into `.claude/skills/llm-extract/SKILL.md`. Other settings are kept, a second run changes nothing, and `--dry-run` shows what would be written. It never touches your home directory.

The same skill is also published for the `skills` CLI:

```bash
npx skills add samestrin/llm-extract-axi --skill llm-extract
```

### Upgrade

Re-run the install one-liner; it replaces the binaries only after the new download verifies.

### Uninstall

Delete `llm-extract` and `llm-extract-mcp` from `$HOME/.local/bin` (or your `INSTALL_DIR`). To remove the Claude Code integration, delete the hook entry `setup` added to `.claude/settings.json` and the `.claude/skills/llm-extract/` directory in the project.

## Quick start

```bash
curl -fsSL https://raw.githubusercontent.com/samestrin/llm-extract-axi/main/install.sh | bash
llm-extract fetch https://example.com
```

Real output (trimmed):

```
<!doctype html><html lang=en><head><meta charset=utf-8>...<title>Example Domain</title>...
```

Then ask for only what you need instead of the whole page:

```bash
llm-extract extract-links --url https://example.com/docs
llm-extract extract-relevant --path https://example.com/docs --context "installation steps"
```

## Usage

| Command | What it does |
| --- | --- |
| `llm-extract fetch <url>` | Download a URL with a direct HTTP GET and write the body to stdout |
| `llm-extract extract-links --url <url>` | Extract a page's links, ranked, each with text, score and section |
| `llm-extract extract-relevant --path <path> --context <text>` | Pull the parts of files, a directory or a URL relevant to the context, using a model |
| `llm-extract setup` | Install the Claude Code SessionStart hook and Agent Skill in a project |

Running `llm-extract` with no arguments prints the reach table below, the path of the binary, a one-line description and the configuration a run would use (whether an API key is set, the model, the base URL, the fetch size cap). It makes no network or model call.

```bash
llm-extract fetch https://example.com --output page.html   # keep a large page out of context
llm-extract fetch https://example.com --json               # metadata (status, bytes) around the body
llm-extract extract-links --url https://example.com --json
llm-extract extract-links --url https://example.com --context "rate limits"   # LLM ranks links by relevance
llm-extract extract-relevant --path ./src --context "API endpoint definitions"
llm-extract extract-relevant --path ./docs --context "configuration options" --concurrency 4 -o out.md
```

Every command takes `--help`, and the global `--axi` flag switches output to TOON on the home view, `extract-links` and `extract-relevant`.

## Configuration

The model client is configured entirely through environment variables:

| Name | Default | Meaning |
| --- | --- | --- |
| `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | unset | API key, required for `extract-relevant` and `extract-links --context` |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | Custom model endpoint |
| `OPENAI_MODEL` | `gpt-4o-mini` | Model selection |
| `FETCH_MAX_BYTES` | `16777216` (16 MiB) | Maximum response body size; a larger body is an error |
| `FETCH_TIMEOUT` | `30` | Per-attempt timeout in seconds for `fetch` |
| `FETCH_MAX_RETRIES` | `2` | Retries on connection errors and 5xx for `fetch` |
| `FETCH_USER_AGENT` | unset | Override the request User-Agent for `fetch` |

On `fetch`, the flags `--max-bytes`, `--timeout`, `--retries` and `--user-agent` override the matching variable.

## Output and exit codes

Output modes: human-readable text by default, `--json` for structured output, `--min` for a token-optimized form, and `--axi` for TOON. A successful run that finds nothing exits 0 with `total: 0` and a `message`; a failed run carries `error` and no total. When a model ran, the structured output reports `model` and `bytes_sent`.

| Exit code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | The command ran and failed (a failed fetch or model call, or a partial result) |
| 2 | The invocation itself is malformed (an unknown command or flag, a flag missing its argument) |

See [docs/output.md](docs/output.md) for the full behavior: partial results, where errors are printed under `--axi`, and what `llm-extract-mcp` does with a failed run.

## How it works

`extract-relevant` and `extract-links` are lifted from `llm-data` and behave as they did there. `fetch` is ported from `llm-tools`, direct tier only.

This is the one tool in the family that may go to the network or spend money on a model, and it says which:

| Command | Network | Model |
| --- | --- | --- |
| `fetch` | Always — fetches the URL | Never |
| `extract-links` | Always — fetches the URL | Only with `--context` (LLM link ranking) |
| `extract-relevant` | Only when `--path` is a URL | Always — `--context` is required |
| `setup` | Never | Never |

See [VISION.md](VISION.md) for what the tool is for and what it is not.

## Limits

- `fetch` is a direct HTTP GET only: there is no proxy or browser tier, so a page behind a JavaScript or Cloudflare challenge fails with the reason instead of returning the challenge page.
- `--format` accepts `raw` only; `text` and `markdown` are rejected.
- A response body larger than the size cap (`FETCH_MAX_BYTES`, default 16 MiB) is an error, not a truncated page.
- `fetch` prints the raw body and ignores `--axi`; TOON applies to the home view, `extract-links` and `extract-relevant`.
- `help[]` next steps appear only on the home view and on `extract-links` and `extract-relevant` results and errors under `--axi`.
- The AXI exceptions behind these limits, with their reasons, are recorded in [docs/axi-audit.md](docs/axi-audit.md).

## Documentation

- [docs/output.md](docs/output.md) — output modes, failure and partial-result behavior, exit codes
- [docs/axi-audit.md](docs/axi-audit.md) — principle-by-principle AXI conformance
- [benchmarks/README.md](benchmarks/README.md) — the token benchmark and how to rerun it
- [VISION.md](VISION.md) — what the tool is for and what it is not
- [AGENTS.md](AGENTS.md) — house rules for changing this repository

## Development

```bash
make build       # build every binary into ./build/
make test        # build, then go test ./...
make test-race   # build, then the race detector — CI runs this
make lint        # go vet, gofmt -s and golangci-lint
make cover-gate  # build, measure coverage, fail below 80%
make hooks       # opt in to the pre-commit hook
```

The test targets build first because the end-to-end tests run the real binary. CI fails below 80% coverage.

## Support

Support is best effort. Report bugs and request features as [GitHub issues](https://github.com/samestrin/llm-extract-axi/issues); there is no SLA and no guaranteed response time, and the project is maintained in spare time. Pull requests are welcome.

## Changelog

See [CHANGELOG.md](CHANGELOG.md).

## License

MIT — see [LICENSE](LICENSE).
