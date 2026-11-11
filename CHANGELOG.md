# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The Agent Skill is now committed at `integrations/claude-code/skill/SKILL.md`, so `npx skills add samestrin/llm-extract-axi --skill llm-extract` works. A test keeps it identical to the skill `llm-extract setup` writes.

### Changed

- `-v`, `-V` and `--version` now print the bare version (`1.0.1`, without a leading `v` or the program name) and exit 0 on both `llm-extract` and `llm-extract-mcp`. `llm-extract-mcp` previously printed nothing, and `-V` was rejected as an unknown shorthand flag.

## [1.0.1] - 2026-11-10

### Added

- `llm-extract fetch` now caps how much of a response body is read into memory. A body larger than the cap is an error (`response body exceeds size cap`), never a truncated page reported as success. The cap defaults to 16 MiB and is set with `--max-bytes` or the `FETCH_MAX_BYTES` environment variable; a non-positive value falls back to the default. A blocked or non-2xx response still reports its HTTP status or challenge error rather than the size cap, and no partial `--output` file is written.

### Fixed

- `llm-extract-mcp`: a non-zero CLI exit now includes partial stdout in the error result when non-empty, so an MCP caller receives partial results (e.g. kept heuristic links or successful directory files) alongside the error reason. Previously only stderr was returned.
- The `make hooks` pre-commit hook no longer rewrites Go files with `gofmt -s -w` or stages them with `git add`; it only checks formatting and `go vet` and fails, so committing can no longer sweep in edits or files the author never staged. Fix formatting yourself with `gofmt -s -w <file>`.
- `make cover-gate` now measures coverage the way CI and AGENTS.md prescribe — cold (`go clean -testcache`) and with the LLM environment (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`) stripped — so the gated number can no longer be inflated by an exported API key or deflated by a cached partial run.
- `extract-relevant` on a directory now honours its run context: a cancellation or a whole-run deadline reaches every model call instead of being ignored, and the per-file `--timeout` still bounds each call individually. `extract-relevant -o <file> --json` no longer writes the file and exits 0 when the output fails to render; a format error is now reported and the file is left untouched.

## [1.0.0] - 2026-11-03

### Fixed

- A malformed invocation (an unknown command or flag, a flag missing its argument or given a bad value, a required flag not set) exits 2; a command that ran and failed still exits 1. Both used to exit 1, so a caller could not tell a typo from a failure.
- `extract-links` and `extract-relevant` exit non-zero when a fetch or model call fails, instead of printing the error and exiting 0. This includes an `extract-relevant` directory with no suitable files, already reported as an error.
- When `extract-links --context` ranking fails, every heuristic link is still printed after the error, in text mode too, not only the first 100.
- An `extract-relevant` directory run with some failed files keeps the others' extractions and reports how many failed, where before a failed file was dropped silently.

### Added

- Offline token benchmark, `./benchmarks/tokens.sh`: on a committed 15 KB documentation page, `extract-links --axi` prints 1652 tokens against the page's 4240, a 2.6x reduction. The page is served on loopback, no model is called and the tokenizer is built in, so it makes no network call; `benchmarks/README.md` says how to rerun it and how the tokens are counted.
- README section "Install": the one-line `install.sh` command (no sudo, no clone), how to pin a version with `LLM_EXTRACT_VERSION`, and `INSTALL_DIR` for installing elsewhere.
- README section "Support": a best-effort support statement pointing at GitHub issues, with no SLA.
- `install.sh`: installs `llm-extract` and then `llm-extract-mcp` from a GitHub release into `$HOME/.local/bin` (or `INSTALL_DIR`), with no sudo, no clone and no Go toolchain. `LLM_EXTRACT_VERSION` pins a tag (default: latest). It verifies both downloads against `SHA256SUMS.txt` before installing either; on an unsupported OS or architecture, a failed download, a download that stalls past `LLM_EXTRACT_DOWNLOAD_TIMEOUT` (default 300 seconds) or a checksum mismatch it installs nothing, leaves an existing install untouched and exits non-zero.
- Release workflow: a pushed `v*` tag runs the tests, builds both binaries for five platforms (darwin/amd64, darwin/arm64, linux/amd64, linux/arm64, windows/amd64) stamped with the tag, writes `SHA256SUMS.txt`, and publishes them as a GitHub release. A manual run, or a pull request changing the workflow or `scripts/release-build.sh`, builds the same assets and publishes nothing.
- `llm-extract setup`: writes a SessionStart hook (running `llm-extract`) into the project's `.claude/settings.json`, merging with existing keys and hooks, and an Agent Skill at `.claude/skills/llm-extract/SKILL.md`. It prints each path, supports `--dry-run` and `--dir`, changes nothing on a second run, never prompts, and refuses to overwrite a settings file it cannot parse.
- `llm-extract` with no arguments prints the binary's path (home shown as `~`), a one-sentence description and which commands use the network or a model, instead of the full help. It makes no network or model call. `--help` still prints help.
- `--json`/`--axi`/`--min` output of `extract-links --context` and `extract-relevant` now carries `model` and `bytes_sent` (`m`/`bs` in `--min`) when a model was called; heuristic runs omit them. Both commands also print `total` and a `message` ("no links found" / "no relevant content found") on a successful empty run, so it can be told from a failed run, which carries an `error` and no total. A lone "NONE" model answer now counts as no result rather than an extracted part.
- README section "Which commands reach outside": every command labelled network, model, or neither, plus how the model keys are set (`OPENAI_API_KEY`/`ANTHROPIC_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`).
- README section "AXI conformance": all ten AXI principles listed as met or with a stated exception and its reason — `fetch` ignoring `--axi` (1), the five-field `extract-links` schema (2), no truncation or `--full` on `fetch` (3), early failures reporting on stderr only (6), and no `help[]` next-step lines (9).

- `llm-extract-mcp`: a thin MCP server (stdio) with `fetch`, `extract_links` and `extract_relevant` tools. Each call runs the `llm-extract` CLI (found via `LLM_EXTRACT_BIN`, next to the server, or on `PATH`) and returns its stdout; a non-zero exit is an error result carrying stderr, never an empty success.
- `llm-extract fetch <url>`: ported from `llm-tools`, direct tier only (no proxy or FlareSolverr). A failed fetch, a non-2xx status, or a challenge page that needs a browser exits non-zero with the reason on stderr, never an empty result. `--format` accepts `raw` only; `text` and `markdown` are rejected until ported.
- Project skeleton on the `llm-data-axi` pattern: Go module `github.com/samestrin/llm-extract-axi` and an `llm-extract` root command whose version is stamped at build time. An unknown command exits non-zero.
- `Makefile` with `build`, `build-all`, `test`, `test-race`, `test-cover`, `cover-gate`, `lint`, `vet`, `fmt`, `golangci`, `install`, `clean` and `hooks` targets. `build` covers every binary under `cmd/`, and the test targets build first.
- CI: `lint` (`go vet`, `gofmt -s`, `golangci-lint` v2.12.2) → `make build` → `go test -race` with coverage → an 80% coverage gate that fails the build, plus a five-platform cross-compile matrix with a 20MB binary size gate.
- `tests/e2e` runs the built binary, and fails rather than skips when it has not been built.
- `AGENTS.md`, `README.md` and `.golangci.yml`.
- `extract-relevant` and `extract-links`, lifted from `llm-data` with their tests, plus the `pkg/llmapi` model client and the `pkg/output` formatter. The commands behave as they did in `llm-data`; the user agent now reads `llm-extract/1.0`.
- A root `--axi` flag that selects TOON output for every command.
