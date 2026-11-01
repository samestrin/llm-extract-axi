# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- `extract-links` and `extract-relevant` exit non-zero when a fetch or model call fails; before, they printed the error and exited 0. The output still carries the error. Partial results are kept and labelled: `extract-links --context` prints every heuristic link (not only the first 100) after the ranking error, in text mode as well as `--json`, and an `extract-relevant` directory run keeps the files that succeeded and reports how many failed, where before a failed file was dropped silently. An `extract-relevant` run on a directory with no suitable files, already reported as an error, now also exits non-zero.

### Added

- README section "Which commands reach outside": every command labelled network, model, or neither, plus how the model keys are set (`OPENAI_API_KEY`/`ANTHROPIC_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`).

- `llm-extract-mcp`: a thin MCP server (stdio) with `fetch`, `extract_links` and `extract_relevant` tools. Each call runs the `llm-extract` CLI (found via `LLM_EXTRACT_BIN`, next to the server, or on `PATH`) and returns its stdout; a non-zero exit is an error result carrying stderr, never an empty success.
- `llm-extract fetch <url>`: ported from `llm-tools`, direct tier only (no proxy or FlareSolverr). A failed fetch, a non-2xx status, or a challenge page that needs a browser exits non-zero with the reason on stderr, never an empty result. `--format` accepts `raw` only; `text` and `markdown` are rejected until ported.
- Project skeleton on the `llm-data-axi` pattern: Go module `github.com/samestrin/llm-extract-axi` and an `llm-extract` root command whose version is stamped at build time. An unknown command exits non-zero.
- `Makefile` with `build`, `build-all`, `test`, `test-race`, `test-cover`, `cover-gate`, `lint`, `vet`, `fmt`, `golangci`, `install`, `clean` and `hooks` targets. `build` covers every binary under `cmd/`, and the test targets build first.
- CI: `lint` (`go vet`, `gofmt -s`, `golangci-lint` v2.12.2) → `make build` → `go test -race` with coverage → an 80% coverage gate that fails the build, plus a five-platform cross-compile matrix with a 20MB binary size gate.
- `tests/e2e` runs the built binary, and fails rather than skips when it has not been built.
- `AGENTS.md`, `README.md` and `.golangci.yml`.
- `extract-relevant` and `extract-links`, lifted from `llm-data` with their tests, plus the `pkg/llmapi` model client and the `pkg/output` formatter. The commands behave as they did in `llm-data`; the user agent now reads `llm-extract/1.0`.
- A root `--axi` flag that selects TOON output for every command.
