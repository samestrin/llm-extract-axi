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

`extract-relevant`, which always needs a model, fails with an error when no API key is set or the model call fails. `extract-links --context` behaves differently: when LLM ranking fails (for example no API key is set), it prints an `ERROR: LLM ranking failed: <reason>` line and exits 0. In `--json` mode the output also carries the unranked heuristic links alongside the error; in text mode the error line is the whole output. Either way the failure is said aloud — never an empty success.

### Setting keys

The model client is configured entirely through environment variables:

- `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` — the API key (required for model use)
- `OPENAI_BASE_URL` — custom endpoint (default `https://api.openai.com/v1`)
- `OPENAI_MODEL` — model selection (default `gpt-4o-mini`)

```bash
llm-extract fetch https://example.com                   # body to stdout
llm-extract fetch https://example.com --output page.html
llm-extract fetch https://example.com --json
```

A failed fetch, a non-2xx status, or a page that needs a browser (a challenge page) exits non-zero with the reason on stderr, never an empty result. There is no proxy or FlareSolverr tier, and `--format` accepts `raw` only; `text` and `markdown` are rejected.

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

## License

MIT
