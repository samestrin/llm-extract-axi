# Output, failures and exit codes

How `llm-extract` reports results, failures and partial results, in each output mode.

## Output modes

- Text (default): human-readable. `extract-links` prints a ranked list; `extract-relevant` prints the extracted passages.
- `--json`: structured output. `extract-links` rows carry `href`, `text`, `score` and `section` by default; `--fields` picks others, for example `--fields context,href`, and an unknown field exits 2 and lists the valid ones.
- `--min`: a token-optimized form for agent consumption.
- `--axi`: TOON (AXI token-dense format), on the home view, `extract-links` and `extract-relevant`. `fetch` ignores the flag and prints the raw body.

When a model ran, the `--json`, `--min` and `--axi` output reports `model` and `bytes_sent` (`m`/`bs` in `--min`); `extract-links` without `--context` omits both, because no model ran.

## The home view

Running `llm-extract` with no arguments prints the path of the binary (home shown as `~`), a one-line description, the network/model reach table and the configuration a run would use: whether an API key is set (the key itself is never shown), the model, the base URL (with any user, password or query string removed) and the fetch size cap. It makes no network or model call. `llm-extract --axi` prints the same view as TOON, with a `help[]` list of next steps.

## Saving to a file (`-o`)

`extract-relevant -o FILE` writes the extraction to FILE. What stdout carries depends on the mode:

- Text (default): a three-line summary — `Extracted content written to: <file>`, `Files processed: <n>/<total>`, `Processing time: <seconds>`.
- `--json` and `--axi`: one structured document and nothing else — the result without the passages, which are in FILE (keeping content out of the caller's context is what `-o` is for). It still carries `total`, `total_files`, `processed_files` and `processing_time_s`, `model` and `bytes_sent` when a model ran, the `message` on an empty result, and `help[]` under `--axi`.
- A failed run with `-o` under `--axi` prints the single `error`/`message`/`help[]` document below instead of the result, so stdout is always one decodable TOON document and stderr stays empty. The error is also written to FILE. Under `--json` the result document carries the `error` field and the reason is on stderr, as without `-o`.

The prose summary never reaches stdout under `--axi` or `--json`: prose in front of a structured document makes the single stream decode as neither.

## Empty results

A successful run that finds nothing exits 0 and carries `total: 0` and a `message` ("no links found" / "no relevant content found"). A failed run carries `error` and exits non-zero. `error` is what marks a failure, not a missing `total`: a failed `extract-relevant` run and a failed `extract-links` fetch carry no total, but a failed `extract-links --context` ranking keeps the unranked links and their `total` beside the `error` (see partial results below). `total` without `error` means the run succeeded.

## Failures

A failed fetch or a failed model call exits non-zero from both `extract-links` and `extract-relevant`, with the reason on stderr (on stdout under `--axi`, see below). The output is still printed and carries the error: an `ERROR: <reason>` line in text mode, the `error` field in `--json`. `extract-relevant` with no API key set fails before doing any work.

A partial result is kept and says it is partial, but the exit is still non-zero:

- When `extract-links --context` ranking fails (for example no API key is set), every unranked heuristic link is printed after `ERROR: LLM ranking failed: <reason>`.
- When some files in an `extract-relevant` directory run fail, the extractions from the rest are printed and the error reads `<n> of <total> files failed (first: <file>: <reason>)`.

## Truncated model input

The two model commands cut what they send the model, and a result built from a cut input says so:

- `extract-relevant` cuts each prompt it sends at 30,000 bytes (the context line and the content together). Content past the cut is not read. In a directory run each file is one prompt.
- `extract-links --context` sends the model only the first 100 links, in a prompt cut at 25,000 bytes. Links past the first 100 are not in the result; links past the byte cut were not seen by the model and keep their page-position score.

When either happens the result carries a `truncated` note naming the limit and what was cut: the `truncated` field in `--json` and `--axi` (`tr` in `--min`), a `TRUNCATED:` line in `extract-relevant` text (and a `Truncated:` line in its `-o` summary), and a `Truncated:` line in `extract-links` text. The note is on partial results too. A run whose model call succeeded still exits 0; the note is how a caller knows the model read only part of the input. The limits are fixed and there is no `--full`: split a large file, or pass a narrower page.

A failed `fetch` — a non-2xx status, or a page that needs a browser (a challenge page) — exits non-zero with the reason on stderr, never an empty result. A response larger than the size cap (`--max-bytes` / `FETCH_MAX_BYTES`, default 16 MiB) is an error, not a truncated page, and no partial `--output` file is written.

## Errors under --axi

Under `--axi`, every error is on stdout as a TOON `error`/`message` object with a `help[]` list of next steps, and stderr stays empty. This covers a failure before work starts (no API key, a bad URL, a failed `fetch`), an unknown command (which lists the commands), and an unknown flag (named in the message, with the command's valid flags in `flags[]`, exit 2, before any network or model call). Without `--axi`, those errors stay on stderr.

## Exit codes

| Exit code | Meaning |
| --- | --- |
| 0 | Success, including a successful run that found nothing |
| 1 | The command ran and failed: a failed fetch or model call, or a partial result |
| 2 | The invocation itself is malformed: an unknown command or flag, or a flag missing its argument |

Retrying an exit 2 unchanged never helps.

## Through llm-extract-mcp

Through `llm-extract-mcp`, a non-zero exit from the CLI is an error result carrying stderr and, when non-empty, the partial stdout, so the caller receives what was produced alongside the failure reason.
