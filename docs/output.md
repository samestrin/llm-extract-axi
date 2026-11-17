# Output, failures and exit codes

How `llm-extract` reports results, failures and partial results, in each output mode.

## Output modes

- TOON (default): the AXI token-dense format, on the home view, `extract-links` and `extract-relevant`, with a `help[]` list of next steps.
- `--plain`: human-readable text (the pre-2.0 default). `extract-links` prints a ranked list; `extract-relevant` prints the extracted passages. `--axi=false` is the same.
- `--axi`: still accepted, a no-op alias for the default.
- `--json`: structured output, unchanged. `extract-links` rows carry `href`, `text`, `score` and `section` by default; `--fields` picks others, for example `--fields context,href`, and an unknown field exits 2 and lists the valid ones. `--json` carries no `help[]`.
- `--min`: a token-optimized form for agent consumption, independent of the encoding.

`--plain` with `--json` is a usage error (exit 2). A successful `fetch` prints the raw body in every mode except `--json`, which keeps its metadata envelope (status, bytes) around the body; `setup` and `completion` stay prose. `--json` and `--min` belong to the commands that list them in `--help`; on a command without them they are unknown flags (exit 2). A failed `fetch` prints its error on stdout in the active format, like every other failure.

When a model ran, the structured output (TOON, `--json`, `--min`) reports `model` and `bytes_sent` (`m`/`bs` in `--min`); `extract-links` without `--context` omits both, because no model ran.

## The home view

Running `llm-extract` with no arguments prints the path of the binary (home shown as `~`), a one-line description, the network/model reach table and the configuration a run would use: whether an API key is set (the key itself is never shown), why a key that is set cannot be used (`api_config_error`, for example an `ANTHROPIC_API_KEY` with no `OPENAI_BASE_URL`), the model, the base URL (with any user, password or query string removed) and the fetch size cap. It makes no network or model call. By default the view is one TOON document with a `help[]` list of next steps; `--plain` prints it as a table.

## Saving to a file (`-o`)

`extract-relevant -o FILE` writes the extraction to FILE. What stdout carries depends on the mode:

- `--plain`: a three-line summary — `Extracted content written to: <file>`, `Files processed: <n>/<total>`, `Processing time: <seconds>`.
- TOON (default) and `--json`: one structured document and nothing else — the result without the passages, which are in FILE (keeping content out of the caller's context is what `-o` is for). It still carries `total`, `total_files`, `processed_files` and `processing_time_s`, `model` and `bytes_sent` when a model ran, the `message` on an empty result, and `help[]` in TOON.
- A failed run with `-o` prints the error on stdout like any other failure: in TOON the single `error`/`message`/`help[]` document below, so stdout is always one decodable TOON document; under `--json` the result document carries the `error` field. stderr stays empty. A result written to FILE carries the error too; a failure before work starts (no API key) writes no file and prints the usual error document.

The prose summary never reaches stdout in TOON or `--json`: prose in front of a structured document makes the single stream decode as neither.

## Empty results

A successful run that finds nothing exits 0 and carries `total: 0` and a `message` ("no links found" / "no relevant content found"). A failed run carries `error` and exits non-zero. `error` is what marks a failure, not a missing `total`: a failed `extract-relevant` run and a failed `extract-links` fetch carry no total, but a failed `extract-links --context` ranking keeps the unranked links and their `total` beside the `error` (see partial results below). `total` without `error` means the run succeeded. `extract-relevant --path` on a directory with no text file to read (empty, or only binary or over-100 KB files) is a failure, exit 1, with `error: no suitable files found in directory` and no `total`: nothing was searched, so `total: 0` would claim a search that did not happen.

## Failures

A failed fetch or a failed model call exits non-zero from both `extract-links` and `extract-relevant`, with the reason on stdout in the active format (see below). The output is still printed and carries the error: an `ERROR: <reason>` line under `--plain`, the `error` field in TOON and `--json`. An unusable model-key configuration is a usage error (exit 2) in both commands: no API key set, or only `ANTHROPIC_API_KEY` set with no `OPENAI_BASE_URL`. `extract-relevant` fails before doing any work; `extract-links --context` fetches the page, makes no model call and reports the error beside its unranked links. The client speaks the OpenAI-compatible API and never sends an Anthropic key to the OpenAI default.

A partial result is kept and says it is partial, but the exit is still non-zero:

- When `extract-links --context` ranking fails (for example no API key is set), every unranked heuristic link is printed with the error: an `ERROR: LLM ranking failed: <reason>` line under `--plain`, the `error` field beside the links in TOON and `--json`.
- When some files in an `extract-relevant` directory run fail, the extractions from the rest are printed and the error reads `<n> of <total> files failed (first: <file>: <reason>)`.

## Truncated model input

The two model commands cut what they send the model, and a result built from a cut input says so:

- `extract-relevant` cuts each prompt it sends at 30,000 bytes (the context line and the content together). Content past the cut is not read. In a directory run each file is one prompt.
- `extract-links --context` sends the model only the first 100 links, in a prompt cut at 25,000 bytes. Links past the first 100 are not in the result; links past the byte cut were not seen by the model and keep their page-position score.

When either happens the result carries a `truncated` note naming the limit and what was cut: the `truncated` field in TOON and `--json` (`tr` in `--min`), a `TRUNCATED:` line in `extract-relevant` `--plain` text (and a `Truncated:` line in its `-o` summary), and a `Truncated:` line in `extract-links` `--plain` text. The note is on partial results too, including a failed `extract-links --context` ranking whose input was cut: there every found link is kept, unranked, with its page-position score. A run whose model call succeeded still exits 0; the note is how a caller knows the model read only part of the input. The limits are fixed and there is no `--full`: split a large file, or pass a narrower page.

A failed `fetch` — a non-2xx status, or a page that needs a browser (a challenge page) — exits non-zero with the reason on stdout in the active format, never an empty result. `fetch URL > file` can therefore hold an error document after a failure; check the exit status. A response larger than the size cap (`--max-bytes` / `FETCH_MAX_BYTES`, default 16 MiB) is an error, not a truncated page, and no partial `--output` file is written.

## Errors on stdout

Every error is on stdout in the format a success would have used, and stderr stays empty (it is left for diagnostics):

- TOON (default): an `error`/`message` object with a `help[]` list of next steps.
- `--json`: one `{"error": true, "message": ...}` document.
- `--plain` (or `--axi=false`): a single `Error: ...` line.

This covers a failure before work starts (no API key, a bad URL, a failed `fetch`), an unknown command (which lists the commands), and an unknown flag (named in the message, with the command's valid flags in `flags[]` in TOON, exit 2, before any network or model call). On a usage error, parsing may stop before the last format flag, so the format is read from the command line the way pflag reads it: `--plain` (or `--axi=false`) gives the plain `Error: ...` line, otherwise `--json` gives JSON, otherwise TOON. The last occurrence of each flag sets that flag, so `--json=false` undoes an earlier `--json`, but `--axi` is the no-op alias for the default and never undoes one: `extract-links --json --nope --axi` prints JSON. The argument after a flag that takes a value is that value, so in `extract-links --url --axi --bogus` the `--axi` is the URL and the error is TOON. `--plain` wins over `--json` whichever comes last, so the usage error for asking for both is always the plain line. It is deliberate: the caller asked for text, and a JSON or TOON document would give it a format it ruled out. An error a result already carries is not printed twice.

## Suggested commands in help[]

A `help[]` step that suggests running `extract-relevant` on a link, page URL or path never carries the value itself, on any platform. It names the result field that holds it, as in `--path <links[0].href>`, `--path <url>` or `--path <path>`, for the caller to fill in from the same output. The page controls a link, and no quoting is safe on every route to a shell: cmd.exe and PowerShell each break a different quoting, and TOON escaping doubles the backslash of a POSIX `'\''`, so a command copied off the terminal would run what the page put in the link. Placeholders such as `<what you need>` are always for the caller to replace.

## Exit codes

| Exit code | Meaning |
| --- | --- |
| 0 | Success, including a successful run that found nothing |
| 1 | The command ran and failed: a failed fetch or model call, or a partial result |
| 2 | The invocation itself is malformed: an unknown command or flag, a flag missing its argument, a rejected flag value (such as `fetch --format text`, or a URL that is not http or https), the wrong number of arguments (such as `fetch` with no URL), `completion` with no shell or an unknown one, `--plain` with `--json`, or an unusable model-key configuration (no API key, or an `ANTHROPIC_API_KEY` with no `OPENAI_BASE_URL`) in `extract-relevant` or `extract-links --context` |

Retrying an exit 2 unchanged never helps.

## Through llm-extract-mcp

Through `llm-extract-mcp`, a non-zero exit from the CLI is an error result naming the subcommand and carrying the CLI's stdout — the error document, or the partial result — so the caller receives what was produced alongside the failure reason. Stderr diagnostics are included when present; with empty stderr the exit status names the failure. The server adds no logic of its own: a successful tool result is the CLI's TOON, `help[]` included.
