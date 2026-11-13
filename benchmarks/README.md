# Benchmarks

Two benchmarks, both offline: [tokens](#token-benchmark) (what `extract-links` saves an agent in tokens) and [performance](#performance-benchmark) (how long `llm-extract` takes).

# Token benchmark

How much less an agent reads when it asks `extract-links` for a page's links instead of reading the page.

## Result

On the committed fixture, a 15,411-byte documentation page:

```
raw page      4240 tokens
--axi output  1435 tokens
ratio         3.0x (raw / --axi)
```

`llm-extract extract-links --axi`, in its default heuristic mode, gives an agent every link on the page, ranked, with its text, score and section, in **3.0x fewer tokens** than the page itself.

Measured on 2026-11-11 with the counter described below. Before `context` left the default row, checked separately against tiktoken: 4503 vs 1733 tokens with `cl100k_base`, 4527 vs 1740 with `o200k_base`, a 2.60 ratio both times.

## Rerun it

From the repository root:

```bash
./benchmarks/tokens.sh
```

It runs `make build` if `build/llm-extract` is missing, then `go run ./benchmarks/tokens`. To measure another page, pass `-fixture path/to/page.html`; to measure another binary, `-bin path/to/llm-extract`.

The same code runs on every `go test ./...` (`benchmarks/tokens/main_test.go`), so CI fails if the benchmark stops working.

## No network

- The page is `fixtures/docs-page.html`, served from a server inside the benchmark process on `127.0.0.1`.
- `llm-extract` runs without `--context`, so it calls no model. Its `OPENAI_*` and `ANTHROPIC_*` variables are removed as well.
- Its `HTTP_PROXY` and `HTTPS_PROXY` point at a closed local port. Go never proxies a loopback request, so the fixture loads; a request to any other host would fail instead of leaving the machine.
- The token counter is built in. Nothing is downloaded, not even a tokenizer vocabulary.

A failed `extract-links`, or a page with no links, is an error with a non-zero exit, never a ratio.

## The counter

The count is the number of pieces `cl100k_base`'s split pattern cuts the text into, the step GPT-4's tokenizer takes before its BPE merges. A merge never crosses a piece boundary, so each count is a **lower bound** on the real `cl100k_base` count. The real tokenizers read about 6% more on the raw page and 5% more on the `--axi` output, so the ratio barely moves (2.57 here against 2.60 from tiktoken). The ratio is the figure to quote; the absolute counts depend on the tokenizer. Claude's tokenizer is not public, so it is not used.

The loopback address and port appear in every resolved link. They are replaced with `https://docs.example.com` before counting, so every run counts the same text.

## The fixture

`fixtures/docs-page.html` is a made-up "getting started" page, built like a typical documentation site: inline CSS and scripts, a header nav, a sidebar, an on-this-page list and a footer, with 50 links that `extract-links` keeps once in-page anchors, `mailto:` and duplicates are dropped. A page with more markup per link scores a higher ratio, and a bare list of links a lower one. One page is one data point, not an average.

# Performance benchmark

How long `llm-extract` takes, from the release binary, in three cases:

- **Cold start:** `llm-extract --version`.
- **Typical command:** `extract-links --axi` on `fixtures/docs-page.html`.
- **Large input:** `extract-links --axi` on a page of at least 5 MB, built by repeating the fixture's `<body>` (pass `-large page.html` to use another). Its links repeat too and `extract-links` drops duplicates, so this case times reading and parsing a large page, not printing a long list.

The result is [`results-performance.md`](results-performance.md), generated from every raw timing in [`results-performance.json`](results-performance.json). Its header names the version, the date, the machine and the method. It compares `llm-extract` with no other tool.

## Rerun it

From the repository root:

```bash
./benchmarks/perf.sh            # build the release binary for this machine, time it, write both files
./benchmarks/perf.sh -render    # regenerate the table from the JSON, timing nothing
```

`perf.sh` builds with `scripts/release-build.sh`, stamped with `git describe`, then runs `go run ./benchmarks/perf`. Pass `-bin path/to/llm-extract` to time a binary you already have, such as a downloaded release asset, and `-warmup N` or `-runs N` to change the counts (3 and 20). Never edit the table by hand: the next run overwrites it.

Each timing is the wall-clock time of one `llm-extract` process, from start to exit, with the pages served by the benchmark on `127.0.0.1`. No outside host is contacted or timed: that would measure the host, not the tool. The environment is the same as the token benchmark's: no model variables, no `FETCH_*` tuning, and every proxy pointed at a closed local port.

A case that records zero timings fails the run, and so does any process that exits non-zero or finds no links. A failed run writes nothing, so the committed results stay as they were. `perf_test.sh` proves this against a stub binary and runs in CI as part of `make test-scripts`.
