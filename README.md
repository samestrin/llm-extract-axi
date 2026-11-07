# llm-extract

> Pull only what an agent needs from a large file or a web page, without filling its context.

![Go](https://img.shields.io/github/go-mod/go-version/samestrin/llm-extract-axi)
![License](https://img.shields.io/github/license/samestrin/llm-extract-axi)

A CLI built to the [AXI](https://axi.md) principles. It turns outside content into something smaller, using the network or a model only when it has to, and it says when it does. See [VISION.md](VISION.md) for what it is for and what it is not.

`llm-extract` is the product. `llm-extract-mcp` is an MCP server that shells out to it, a fallback for environments that cannot run a CLI.

## Status

`extract-relevant` and `extract-links` are lifted from `llm-data` and behave as they did there. `fetch` is ported from `llm-tools`, direct tier only.

## Which commands reach outside

This is the one tool in the family that may go to the network or spend money on a model, and it says which:

| Command | Network | Model |
| --- | --- | --- |
| `fetch` | Always — fetches the URL | Never |
| `extract-links` | Always — fetches the URL | Only with `--context` (LLM link ranking) |
| `extract-relevant` | Only when `--path` is a URL | Always — `--context` is required |
| `setup` | Never | Never |

Run `llm-extract` with no arguments to see this table, the path of the binary (home shown as `~`) and a one-line description; it makes no network or model call. `llm-extract --help` prints the full help.

A failed fetch or a failed model call exits non-zero from both `extract-links` and `extract-relevant`, with the reason on stderr. The output is still printed and carries the error: an `ERROR: <reason>` line in text mode, the `error` field in `--json`. `extract-relevant` with no API key set fails before doing any work. A partial result is kept and says it is partial, but the exit is still non-zero: when `extract-links --context` ranking fails (for example no API key is set), every unranked heuristic link is printed after `ERROR: LLM ranking failed: <reason>`; when some files in an `extract-relevant` directory run fail, the extractions from the rest are printed and the error reads `<n> of <total> files failed (first: <file>: <reason>)`. Through `llm-extract-mcp` a non-zero exit is an error result carrying stderr and, when non-empty, the partial stdout, so the caller receives what was produced alongside the failure reason.

A successful run that finds nothing exits 0 and carries `total: 0` and a `message` ("no links found" / "no relevant content found"); a failed run carries `error` and no total. When a model ran, the `--json`, `--min` and `--axi` output reports `model` and `bytes_sent` (`m`/`bs` in `--min`); `extract-links` without `--context` omits both.

Exit codes: `0` on success, `1` when a command ran and failed (a failed fetch or model call, a partial result), `2` when the invocation itself is malformed (an unknown command or flag, a flag missing its argument). Retrying an exit 2 unchanged never helps.

## AXI conformance

This tool is built to the ten [AXI](https://axi.md) principles. The table records each as met or with a stated exception; the exceptions are deliberate, with the reason given.

| # | Principle | Status |
| --- | --- | --- |
| 1 | Token-efficient output (TOON) | Met, with an exception: `--axi` emits TOON for `extract-links` and `extract-relevant`, but `fetch` ignores the flag and prints the raw body. Fetching is a byte pipe; TOON would wrap the page, not reduce it. |
| 2 | Minimal default schemas (3–4 fields) | Exception: `extract-links` rows have five fields (`context`, `href`, `score`, `section`, `text`). All five are used for ranking, and there is no `--fields` flag. |
| 3 | Content truncation with a size hint and `--full` | Exception: `fetch` prints the whole body by design — that is what it is for — and `--output FILE` is the way to keep a large page out of context. There is no truncation, size hint or `--full`. |
| 4 | Pre-computed aggregates | Met: `extract-links` reports `total`; `extract-relevant` reports `total`, `total_files` and `processed_files`, and a partial directory run reports `<n> of <total> files failed`. |
| 5 | Definitive empty states | Met: an empty success carries `total: 0` and a `message` ("no links found" / "no relevant content found"); a failure carries `error` and no total. |
| 6 | Structured errors & exit codes | Met, with an exception: exit codes are 0/1/2 as documented above, and mid-run failures of `extract-links` and `extract-relevant` carry the `error` field on stdout. Failures before work starts (no API key, a bad URL, a failed `fetch --json`) report on stderr only. |
| 7 | Ambient context (setup hook + Agent Skill) | Met: `llm-extract setup` installs a Claude Code SessionStart hook and Agent Skill (below). |
| 8 | Content first | Met: a bare run shows the binary's path (`~` for home), a one-sentence description and the network/model table above. |
| 9 | Contextual disclosure (`help[]` next steps) | Exception: no output carries `help[]` next-step lines. The commands are few and `--help` covers them; the next step is obvious from the output. |
| 10 | Consistent way to get help | Met: `--help` on the root and every command, each listing the global `--axi` flag. |

### Claude Code setup

`llm-extract setup` (run in a project, or with `--dir`) writes a SessionStart hook into `.claude/settings.json` and an Agent Skill into `.claude/skills/llm-extract/SKILL.md`. Other settings are kept, a second run changes nothing, and `--dry-run` shows what would be written. It never touches your home directory.

### Setting keys

The model client is configured entirely through environment variables:

- `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` — the API key (required for model use)
- `OPENAI_BASE_URL` — custom endpoint (default `https://api.openai.com/v1`)
- `OPENAI_MODEL` — model selection (default `gpt-4o-mini`)

### Examples

```bash
llm-extract fetch https://example.com                   # body to stdout
llm-extract fetch https://example.com --output page.html
llm-extract fetch https://example.com --json
```

A failed fetch, a non-2xx status, or a page that needs a browser (a challenge page) exits non-zero with the reason on stderr, never an empty result. There is no proxy or FlareSolverr tier, and `--format` accepts `raw` only; `text` and `markdown` are rejected.

## Install

One line, no sudo, no clone, no Go toolchain:

```bash
curl -fsSL https://raw.githubusercontent.com/samestrin/llm-extract-axi/main/install.sh | bash
```

This installs `llm-extract` and `llm-extract-mcp` into `$HOME/.local/bin` (set `INSTALL_DIR` to install elsewhere). Both binaries are verified against the release's `SHA256SUMS.txt` before either is installed; on an unsupported platform, a failed or stalled download, or a checksum mismatch it installs nothing and leaves an existing install untouched.

Pin a version instead of installing the latest release:

```bash
curl -fsSL https://raw.githubusercontent.com/samestrin/llm-extract-axi/main/install.sh | LLM_EXTRACT_VERSION=v1.0.0 bash
```

If `~/.local/bin` is not on your `PATH`, the installer says so and prints the `export` line to add.

## Build

```bash
git clone https://github.com/samestrin/llm-extract-axi.git
cd llm-extract-axi
make build                    # binaries land in ./build/
./build/llm-extract --version
```

## Development

```bash
make build       # build every binary into ./build/
make test        # build, then go test ./...
make test-race   # build, then the race detector — CI runs this
make lint        # go vet, gofmt -s and golangci-lint
make cover-gate  # build, measure coverage, fail below 80%
make hooks       # opt in to the pre-commit hook
```

The test targets build first because the end-to-end tests run the real binary. CI fails below 80% coverage. See [AGENTS.md](AGENTS.md) for the house rules.

## Support

Support is best effort. Report bugs and request features as [GitHub issues](https://github.com/samestrin/llm-extract-axi/issues); there is no SLA and no guaranteed response time, and the project is maintained in spare time. Pull requests are welcome.

## License

MIT
