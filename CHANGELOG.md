# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- A terminal demo in the README shows `llm-extract --version`, `extract-links --axi` and `extract-links --fields href,score` against a local fixture. The session is recorded from `docs/demo/llm-extract.tape`; `docs/demo/README.md` explains how to render the GIF and replay the commands without contacting an outside host.
- `./benchmarks/perf.sh` measures a release binary offline in three cases: cold start, a typical `extract-links --axi` command and a 5 MB input. It saves every raw timing to `benchmarks/results-performance.json` and generates the Markdown results table from that data, so failed runs do not overwrite the committed results.
- The README's new Performance section publishes the generated benchmark table with the measured version, date, machine and method. It reports median, mean, minimum and maximum wall-clock times for the same three offline cases, with no hand-entered figures.

### Fixed

- The `help[]` next step under `--axi` no longer lets a page inject a shell command. `extract-links` and `extract-relevant` single-quoted the link, URL or path without escaping an apostrophe, so a link such as `/safe?q=';id;'` produced a suggested command that ran `id` when pasted into a shell. On Linux and macOS the argument is now quoted for a POSIX shell (`'\''` for each apostrophe), and an ordinary link prints exactly as before. On Windows, where single quotes do not quote in cmd.exe and no one quoting is safe in both cmd.exe and PowerShell, a link such as `https://example.com/?a=1&whoami&rem` would still have run `whoami`; there the command now carries none of the value and names the result field that holds it instead (`--path <links[0].href>`, `<url>` or `<path>`). `docs/output.md` describes both.
- `--url --axi --bogus` now prints the plain error on stderr, as pflag reads it: the argument after a flag that takes a value is that value, so `--axi` there is the URL, not the flag. The scan for `--axi` after a failed parse used to count it and print TOON.
- A usage error now follows the last `--axi` on the command line, as a clean parse would. `extract-links --axi --bogus --axi=false` printed TOON and `extract-links --axi=false --bogus --axi` printed the plain error, because an `--axi` read before the bad flag was taken over the ones after it.
- `extract-links --fields score` prints a score of 0 in `--json` and `--axi` instead of dropping it as empty.
- `extract-relevant` and `extract-links --context` now say when the model read a cut-down input. `extract-relevant` cuts each prompt at 30,000 bytes, and `extract-links --context` ranks only the first 100 links in a prompt cut at 25,000 bytes; a result built from a cut input, partial or not, now carries a `truncated` note — including a failed `extract-links --context` ranking, which keeps every link unranked and says what the failed ranking was sent (`tr` in `--min`, a `TRUNCATED:`/`Truncated:` line in text mode and in the `-o` summary). The limits are unchanged. `docs/axi-audit.md` Exception 2, the catalog entry, the README and `docs/output.md` no longer claim that no command truncates content.
- The docs no longer claim that `error` and `total` never mix: a failed `extract-links --context` ranking keeps the unranked links and their `total` beside the `error`. `error` is what marks a failure.
- `extract-relevant -o FILE` no longer mixes prose into a structured stdout stream. Under `--axi` a failed run printed the three plain summary lines (`Extracted content written to:`, `Files processed:`, `Processing time:`) before the TOON error document, so a caller reading the single stdout stream could not decode it. The summary is now plain-mode only: under `--axi` and `--json` stdout carries one structured document — the result without the passages, which stay in the file — and a failed run under `--axi` carries the single `error`/`message`/`help[]` document, with stderr still empty. A successful plain run prints the summary as before.
- `./benchmarks/perf.sh -fixture` no longer panics on a malformed page. A fixture whose `</body>` comes before its `<body`, such as `</body><body>`, crashed the run with a slice-bounds error, and a `<body` tag with no `>` was repeated into the 5 MB page as raw tag text; both now stop the run with an error naming what is wrong.
- `./benchmarks/perf.sh` and `scripts/release-build.sh` now run on stock macOS, which has `shasum` but no `sha256sum`: the release build writes and checks `SHA256SUMS.txt` with `shasum -a 256` when `sha256sum` is missing, and fails naming both tools when neither is installed.
- `docs/axi-audit.md` and `docs/axi-catalog-entry.yaml` are re-pinned to `a886d18`, the commit the v1.1.0 release gate ran on, after the release-gate fixes moved code several `file:line` pointers cited; each pointer now names the symbol it points at. The audit and the entry also describe the Windows `help[]` placeholder, the structured `-o` output under `--axi` and `--json`, and the last `--axi` deciding a usage error's format.

## [1.1.0] - 2026-11-11

### Added

- `docs/axi-audit.md`: a principle-by-principle audit against the ten AXI principles, each claim with a `file:line` or a named test, carrying the five stated exceptions (`fetch` is not TOON, `fetch` has no `--full`, plain-mode errors stay on stderr, `setup` has no Codex or OpenCode target and no session-end hook, `help[]` is `--axi` only).
- `extract-links --fields <list>` chooses which of `href`, `text`, `score`, `section` and `context` each link carries, in text, `--json` and `--axi`. An unknown field is a usage error (exit 2) that lists the valid ones.
- The Agent Skill is now committed at `integrations/claude-code/skill/SKILL.md`, so `npx skills add samestrin/llm-extract-axi --skill llm-extract` works. A test keeps it identical to the skill `llm-extract setup` writes.

- `extract-links` and `extract-relevant` now cap how much of a page body is read into memory, the same way `fetch` already does. A body larger than the cap is an error (`response body exceeds size cap`), never a truncated page reported as success. The cap is set with `FETCH_MAX_BYTES` and defaults to 16 MiB (on `fetch` it is also set with `--max-bytes`).

### Changed

- `README.md` was rewritten to the standard layout: install covers the release binary with the `SHA256SUMS.txt` checksum step, `go install`, a source build, `llm-extract setup` and `npx skills add`; the AXI conformance table moved to `docs/axi-audit.md` and the long output and failure prose to `docs/output.md`. No content was dropped.
- `extract-links` and the page fetch inside `extract-relevant` now stop on cancellation instead of running to completion, so an interrupted run returns promptly rather than finishing a download nobody is waiting for.
- `llm-extract` with no arguments now reports the configuration a run would use — whether an API key is set, the model, the base URL (with any userinfo, query or fragment dropped, where a token may sit) and the fetch size cap — alongside the command reach table. Under `--axi` the home view is one TOON document with a `help[]` list of next steps. It still makes no network or model call.
- Under `--axi`, `extract-links` and `extract-relevant` now carry a `help[]` list of next steps: a concrete next command on an empty result, recovery steps on an error, and one next step after a successful result. `--json` output is unchanged.
- `extract-links` rows now hold four fields by default (`href`, `text`, `score`, `section`); `context` is dropped from the default and stays available with `--fields context`.
- `-v`, `-V` and `--version` now print the bare version (`1.0.1`, without a leading `v` or the program name) and exit 0 on both `llm-extract` and `llm-extract-mcp`. `llm-extract-mcp` previously printed nothing, and `-V` was rejected as an unknown shorthand flag.
- Under `--axi`, errors are now printed on stdout as a TOON document (`error`, `message` and a `help[]` list of next steps) instead of on stderr, and stderr stays empty. This covers a missing API key, a bad URL, a failed `fetch`, an unknown command (which also lists the commands) and an unknown flag (which is named, lists the command's valid flags in `flags[]`, and exits 2 before any network or model call). Without `--axi`, errors stay on stderr as before.

### Fixed

- Documentation: the README `Limits` section now lists every exception from the AXI catalog entry (adding plain-mode errors staying on stderr, and `setup` having no Codex or OpenCode target and no session-end hook); `docs/axi-audit.md` and `docs/axi-catalog-entry.yaml` now use one exception numbering (1-5, in principle order) and drop internal wording from the public docs.

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
