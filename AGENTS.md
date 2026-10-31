# Working on llm-extract

House rules for an agent making changes **to this repository**. `VISION.md` says what the tool is for; this file says how to change it.

## What this is

A CLI that pulls only what an agent needs from a large file or a web page, built to the [AXI](https://axi.md) principles. It is the one tool in the family that may reach the network or spend money on a model, and it says so.

One rule sits above the rest:

**Nothing may report success when it did not succeed.**

A failed fetch or a failed model call is an error, never an empty result. An unknown command exits non-zero. A partial result says it is partial. If you are weighing a design choice, that is the tiebreaker.

## Commands

```bash
make build       # every ./cmd/* binary into ./build/ — required before the end-to-end tests
make lint        # go vet, gofmt -s and golangci-lint
make test-race   # builds, then the race detector — CI runs this
make cover-gate  # builds, measures coverage, and fails below 80%
```

CI runs `go vet`, `gofmt -s`, `golangci-lint`, then `make build`, then `go test -race` with coverage, then **fails the build below 80% coverage**. That gate is a hard fail, not a warning (`scripts/coverage-gate.sh`). It also fails on an empty profile, because an empty profile means nothing ran.

The build comes first because the end-to-end tests in `tests/e2e` exec the real binaries in `./build`. **They fail, not skip, when a binary is missing.** A skip is how an end-to-end suite stops running without anyone noticing.

### Measuring coverage honestly

Measure cold and with the LLM environment stripped, or the number lies:

```bash
make build && go clean -testcache && env -u OPENAI_API_KEY -u ANTHROPIC_API_KEY \
  -u OPENAI_BASE_URL -u OPENAI_MODEL go test -coverprofile=coverage.txt ./...
./scripts/coverage-gate.sh coverage.txt 80
```

A cached run writes a partial profile and reads low. An exported API key can push a test down a path CI never takes and read high.

## Versions

`make build` stamps the version with `-X` onto `commands.Version` in `internal/extract/commands`, not onto `main`, because the cobra root reads it there. The unstamped default is `dev`, and `tests/e2e` fails if a built binary still reports it.

## Testing

Write the failing test first. Every test carries a comment naming what it defends — usually the bug it prevents.

1. **Assert side effects, not exit codes.** A command test that only checks for exit 0 passes even when the command printed nothing. Check the payload.
2. **The root command is a package singleton.** `run` resets every flag before each call, because pflag leaves a flag untouched when it is absent. Do not add `t.Parallel()` to tests that call `run`, and restore any package global you set with `t.Cleanup`.
3. **No network in tests.** There is no LLM endpoint and no outside web page in CI. Test against `httptest` fakes. **Never write a test that `t.Skip`s on an environment variable** — that is what makes a local number and a CI number disagree.
4. **Prove a test can fail.** Break the code deliberately and confirm it goes red. A test that passes against sabotaged code is not defending anything.

## Technical debt and plans

Plans and debt live in the private planning repository, cloned at `.planning/`. Nothing under `.planning/` is committed here.

When you find something you are not fixing, file it there: severity, `file:line`, what is wrong, and how to reproduce. Severity is about consequence — **HIGH** means a caller can be misled about what happened.

## Scope

Prefer the smallest change that solves the problem. This tool owns getting content and reducing it. Local deterministic facts belong to `llm-data-axi`, and code search belongs to `llm-semantic-axi`; do not add work a sibling tool owns.

The MCP server shells out to the CLI and holds no logic of its own. A behaviour added to the server and not the CLI is in the wrong place.
