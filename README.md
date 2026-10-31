# llm-extract

> Pull only what an agent needs from a large file or a web page, without filling its context.

![Go](https://img.shields.io/github/go-mod/go-version/samestrin/llm-extract-axi)
![License](https://img.shields.io/github/license/samestrin/llm-extract-axi)

A CLI built to the [AXI](https://axi.md) principles. It turns outside content into something smaller, using the network or a model only when it has to, and it says when it does. See [VISION.md](VISION.md) for what it is for and what it is not.

`llm-extract` is the product. An MCP server that shells out to it is planned as a fallback for environments that cannot run a CLI.

## Status

`fetch` is here (direct tier only); `extract-relevant` and `extract-links` are not yet.

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
