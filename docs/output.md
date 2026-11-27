# Output, failures and exit codes

How `llm-extract` reports results, failures and partial results, in each output mode.

## Output modes

- TOON (default): the AXI token-dense format, on the home view, `fetch`, `extract-links`, `extract-relevant` and `setup`, with a `help[]` list of next steps (`fetch` carries one when its body is cut or not text, and a failed fetch carries one too).
- `--plain`: human-readable text (the pre-2.0 default). `extract-links` prints a ranked list; `extract-relevant` prints the extracted passages. `--axi=false` is the same.
- `--axi`: still accepted, a no-op alias for the default.
- `--json`: structured output. `extract-links` rows carry `href`, `text`, `score` and `section` by default; `--fields` picks others, for example `--fields context,href`, and an unknown field exits 2 and lists the valid ones. `--json` carries no `help[]`.
- `--min`: a token-optimized form for agent consumption, independent of the encoding.

`--plain` with `--json` is a usage error (exit 2). A successful `fetch` prints a TOON envelope (see below); `--plain` prints the body bytes alone, still cut at `--head` unless `--full`, with a one-line size hint on stderr when the body was cut; `--json` prints the same capped envelope as JSON. With `--format markdown` the body is the page's main content as Markdown, and every cut, size and `--output` below is about that Markdown. `fetch --output FILE --plain` prints one line instead of body bytes, `Saved <n> bytes [HTTP <status> via <tier>, <format>] -> <file>`, because the body is in the file. `setup` prints a `written[]`/`unchanged[]`/`dry_run` document (see below); `completion` stays prose. `--json` and `--min` belong to the commands that list them in `--help`; on a command without them they are unknown flags (exit 2). A failed `fetch` prints its error on stdout in the active format, like every other failure.

When a model call answered, the structured output (TOON, `--json`, `--min`) reports `model` and `bytes_sent` (`m`/`bs` in `--min`): `bytes_sent` totals the prompts of the calls that answered. `extract-links` without `--context` omits both, because no model ran, and so does a run whose every call failed before an answer (an unreachable endpoint, a timeout).

## The home view

Running `llm-extract` with no arguments prints the path of the binary (home shown as `~`), a one-line description, the network/model reach table and the configuration a run would use: whether an API key is set (the key itself is never shown), why a key that is set cannot be used (`api_config_error`, for example an `ANTHROPIC_API_KEY` with no `OPENAI_BASE_URL`), the model, the base URL (with any user, password or query string removed) and the fetch size cap. It makes no network or model call. By default the view is one TOON document with a `help[]` list of next steps; `--plain` prints it as a table.

## The fetch envelope

A successful `fetch` prints one TOON document — the same document as JSON under `--json` — with `url`, `final_url`, `status`, `tier`, `format`, `content_type`, `bytes` (the size of the whole body), `truncated`, and `body`: the first 4096 bytes of the body, cut on a character boundary. When the body is cut, `truncated` is true and `help[]` names `--full` and `--output <file>`; `bytes` is the size hint.

`--format` is `raw` (the default) or `markdown`. Under `markdown` the body is the main content of an HTML page as Markdown, made without a model: `body`, `bytes`, `truncated`, `--head`, `--full`, `--output` and `--plain` all describe the Markdown, while `source_bytes` is the fetched HTML's size and `title` and `byline` come from the page. `--format text` is a usage error (exit 2). A page that is not HTML, or an HTML page with no main content (a login wall, or a page built by JavaScript), exits 1 with the reason and a `help[]` line that names running again without `--format markdown`; it is never an empty body.

- `--head N` sets the cut; `--full` puts the whole body in `body`. `--head` below 1, `--head` together with `--full`, or `--head` together with `--output` (which saves the whole body), is a usage error (exit 2).
- A body that is not UTF-8 text (an image, a PDF) is never printed: `body` is empty, `truncated` is true, and `help[]` points to `--output`.
- `--plain` prints the body bytes alone, still cut at `--head` unless `--full`; when it cuts, a one-line size hint goes to stderr and stdout holds only body bytes.
- `--output FILE` saves the whole body to FILE; stdout gets the envelope with `output` (the path) and without `body`. Under `--plain` stdout gets one line, `Saved <n> bytes [HTTP <status> via <tier>, <format>] -> <file>`, not body bytes.
- `--max-bytes` and `--timeout` below 1, and `--retries` below 0, are usage errors (exit 2), never swapped for the default.
- `--fields a,b` prints only the named envelope fields: `url`, `final_url`, `status`, `tier`, `format`, `content_type`, `bytes`, `source_bytes`, `truncated`, `title`, `byline`, `output`, `body`, `help`. An unknown field is a usage error (exit 2) listing the valid ones. It composes with `--json` and `--output`; `--fields` with `--plain` is a usage error, and a selection where no named field is present in the run is an error (exit 1) naming the fields, never an empty document. `source_bytes`, `title` and `byline` appear only with `--format markdown`.

## Saving to a file (`-o`)

`extract-relevant -o FILE` writes the extraction to FILE. FILE is checked before the first model call: a path that cannot be written (a missing directory, a directory, a FIFO, a device or anything else that is not a regular file, no permission) is a usage error (exit 2) and no call is made. What stdout carries depends on the mode:

- `--plain`: a three-line summary — `Extracted content written to: <file>`, `Files processed: <n>/<total>`, `Processing time: <seconds>`.
- TOON (default) and `--json`: one structured document and nothing else — the result without the passages, which are in FILE (keeping content out of the caller's context is what `-o` is for). It still carries `total`, `total_files`, `processed_files` and `processing_time_s`, `model` and `bytes_sent` when a model call answered, the `message` on an empty result, and `help[]` in TOON.
- A failed run with `-o` prints the error on stdout like any other failure: in TOON the single `error`/`message`/`help[]` document below, so stdout is always one decodable TOON document; under `--json` the result document carries the `error` field. stderr stays empty. A result written to FILE carries the error too; a failure before work starts (no API key) writes no file and prints the usual error document.

The prose summary never reaches stdout in TOON or `--json`: prose in front of a structured document makes the single stream decode as neither.

## Empty results

A successful run that finds nothing exits 0 and carries `total: 0` and a `message` ("no links found" / "no relevant content found"). A failed run carries `error` and exits non-zero. `error` is what marks a failure, not a missing `total`: a failed `extract-relevant` run and a failed `extract-links` fetch carry no total, but a failed `extract-links --context` ranking keeps the unranked links and their `total` beside the `error` (see partial results below). `total` without `error` means the run succeeded. `extract-relevant --path` on a directory with no text file to read (empty, or only binary files) is a failure, exit 1, with `error: no suitable files found in directory` and no `total`: nothing was searched, so `total: 0` would claim a search that did not happen.

A directory run skips dotfiles and dot directories (`.env`, `.npmrc`, `.aws/`), `*.env` files, files named like credentials (`credentials*`, `secret*`, `*token*`, `*.key`, `*.pem`, and `*service-account*`, `*service_account*` or `*serviceaccount*` files, case ignored), and the build and dependency directories (`node_modules`, `vendor`, `dist` and the like): they hold secrets or noise, and a directory run must not send them to a model unasked. It reads only regular files whose real path is inside `--path`: a symlink that leads out of it, a FIFO or a device is skipped, and a link inside it is read. A hard link inside it to a file outside is read too: it is the same file under another name, and no path check can tell. A file named as `--path` itself is read, but only if it is a regular file: a FIFO or a device (`/dev/zero`, or a link to one) is an error, and only the first 30,000 bytes are read, which is all the model sees. `--path` has no default; leaving it out is a usage error (exit 2). The rules go by name only: every other file is sent as it is, so a token pasted into `notes.txt` reaches the model. `.gitignore` is not read, so an ignored file that passes these rules is sent too.

Nothing is skipped silently. The result counts the skipped files by reason in `skipped` (`sk` in `--min`; a `SKIPPED:` line in `--plain` text): `dotfile` (a dotfile or dot directory), `excluded_dir`, `credential_name`, `not_text` (no text extension), `outside_path` (a link out of `--path`), `not_regular` (a FIFO, device or link to a directory) and `unreadable`. A failed run carries the counts too, so `no suitable files found in directory` says what was there.

## setup

`setup` prints one document: `written[]` (the files it wrote, or under `--dry-run` the files it would write), `unchanged[]` (the files already right) and `dry_run`, with `help[]` in TOON. `--json` prints the same document as JSON; `--plain` prints a `wrote <file>`, `would write <file>` or `unchanged <file>` line per file, and `already set up; no changes` when nothing was written. `--dir` must be an existing directory and not the home directory (symlinks resolved); either mistake is a usage error (exit 2) and nothing is written. So is a file that would land outside `--dir` through a symlink — a `.claude`, `.codex` or `.opencode` linked elsewhere (such as `~/.claude`), or a linked settings file — under `--dry-run` too. A settings file is merged in place: its keys keep their order, its numbers keep their digits and its indent is kept. `SKILL.md` and the OpenCode plugin are setup's own files, and a hand edit to either is overwritten on the next run.

## Failures

A failed fetch or a failed model call exits non-zero from both `extract-links` and `extract-relevant`, with the reason on stdout in the active format (see below). The output is still printed and carries the error: an `ERROR: <reason>` line under `--plain`, the `error` field in TOON and `--json`. An unusable model-key configuration is a usage error (exit 2) in both commands: no API key set, or only `ANTHROPIC_API_KEY` set with no `OPENAI_BASE_URL`. `extract-relevant` fails before doing any work; `extract-links --context` fetches the page, makes no model call and reports the error beside its unranked links. The client speaks the OpenAI-compatible API and never sends an Anthropic key to the OpenAI default.

A partial result is kept and says it is partial, but the exit is still non-zero:

- When `extract-links --context` ranking fails (for example no API key is set), every unranked heuristic link is printed with the error: an `ERROR: LLM ranking failed: <reason>` line under `--plain`, the `error` field beside the links in TOON and `--json`.
- When some files in an `extract-relevant` directory run fail, the extractions from the rest are printed and the error reads `<n> of <total> files failed (first: <file>: <reason>)`.

## Truncated model input

The two model commands cut what they send the model, and a result built from a cut input says so:

- `extract-relevant` cuts each prompt it sends at 30,000 bytes (the context line and the content together). Content past the cut is not read. In a directory run each file is one prompt.
- `extract-links --context` sends the model only the first 100 links, in a prompt cut at 25,000 bytes. Links past the first 100 are not in the result; links past the byte cut were not seen by the model and keep their page-position score.

When either happens the result carries a `truncated` note naming the limit and what was cut: the `truncated` field in TOON and `--json` (`tr` in `--min`), a `TRUNCATED:` line in `extract-relevant` `--plain` text (and a `Truncated:` line in its `-o` summary), and a `Truncated:` line in `extract-links` `--plain` text. The note is on partial results too, including a failed `extract-links --context` ranking whose input was cut: there every found link is kept, unranked, with its page-position score. A run whose model call succeeded still exits 0; the note is how a caller knows the model read only part of the input. These are fixed input limits on what the model reads, not output truncation: the model commands have no `--full`. Split a large file, or pass a narrower page. (`fetch`'s output cut is different: `--full` there prints the whole body.)

A failed `fetch` — a non-2xx status, or a page that needs a browser (a challenge page) — exits non-zero with the reason on stdout in the active format, never an empty result. `fetch URL > file` can therefore hold an error document after a failure; check the exit status. On success it holds the envelope, not the raw page; use `--output FILE` for the raw bytes. A response larger than the size cap (`--max-bytes` / `FETCH_MAX_BYTES`, default 16 MiB) is an error, not a truncated page, and no partial `--output` file is written. In TOON the error's `help[]` names the fix for that failure before the usage line: check the URL on a 4xx, try again later or with more `--retries` on a 5xx, open a challenge page in a browser, raise `--max-bytes` over the cap, and check the URL or raise `--timeout`/`--retries` when no response came back.

## Errors on stdout

Every error is on stdout in the format a success would have used, and stderr stays empty (it is left for diagnostics):

- TOON (default): an `error`/`message` object with a `help[]` list of next steps.
- `--json`: one `{"error": true, "message": ...}` document.
- `--plain` (or `--axi=false`): a single `Error: ...` line. An error carried inside a result (see partial results above) prints as that result's `ERROR: ...` line instead.

This covers a failure before work starts (no API key, a bad URL, a failed `fetch`), an unknown command (which lists the commands), an extra argument to a command that takes none (the wrong number of arguments, with the command's flags), and an unknown flag (named in the message, with the command's valid flags in `flags[]` in TOON, exit 2, before any network or model call). On a usage error, parsing may stop before the last format flag, so the format is read from the command line the way pflag reads it: `--plain` (or `--axi=false`) gives the plain `Error: ...` line, otherwise `--json` gives JSON, otherwise TOON. The last occurrence of each flag sets that flag, so `--json=false` undoes an earlier `--json`, but `--axi` is the no-op alias for the default and never undoes one: `extract-links --json --nope --axi` prints JSON. The argument after a flag that takes a value is that value, so in `extract-links --url --axi --bogus` the `--axi` is the URL and the error is TOON. `--plain` wins over `--json` whichever comes last, so the usage error for asking for both is always the plain line. It is deliberate: the caller asked for text, and a JSON or TOON document would give it a format it ruled out. An error a result already carries is not printed twice.

## Suggested commands in help[]

A `help[]` step that suggests running `extract-relevant` on a link, page URL or path never carries the value itself, on any platform. It names the result field that holds it, as in `--path <links[0].href>`, `--path <url>` or `--path <path>`, for the caller to fill in from the same output. The page controls a link, and no quoting is safe on every route to a shell: cmd.exe and PowerShell each break a different quoting, and TOON escaping doubles the backslash of a POSIX `'\''`, so a command copied off the terminal would run what the page put in the link. Placeholders such as `<what you need>` are always for the caller to replace.

## Exit codes

| Exit code | Meaning |
| --- | --- |
| 0 | Success, including a successful run that found nothing |
| 1 | The command ran and failed: a failed fetch or model call, a `fetch --format markdown` page that is not HTML or has no main content, or a partial result |
| 2 | The invocation itself is malformed: an unknown command or flag, a flag missing its argument, a missing required flag (such as `extract-relevant` with no `--path`), a rejected flag value (such as `fetch --format text`, `fetch --head 0`, `--max-bytes 0`, `--timeout 0`, `--retries -1`, `extract-relevant --concurrency 0`, `--head` with `--full` or with `--output`, `--fields` with `--plain` or an unknown `--fields` name, an `extract-relevant -o` path that cannot be written, a `setup --dir` that is missing or is the home directory, or a URL that is not http or https), the wrong number of arguments (such as `fetch` with no URL, or `setup extra`), `completion` with no shell or an unknown one, `--plain` with `--json`, or an unusable model-key configuration (no API key, or an `ANTHROPIC_API_KEY` with no `OPENAI_BASE_URL`) in `extract-relevant` or `extract-links --context` |

Retrying an exit 2 unchanged never helps.

## Through llm-extract-mcp

Through `llm-extract-mcp`, a non-zero exit from the CLI is an error result naming the subcommand and carrying the CLI's stdout — the error document, or the partial result — so the caller receives what was produced alongside the failure reason. Stderr diagnostics are included when present; with empty stderr the exit status names the failure. The server adds no logic of its own: a successful tool result is the CLI's TOON, `help[]` included. The `fetch` tool passes `format`, `head`, `full`, `output` and `fields` straight to the CLI flags of the same name, so `--format markdown` and the `--full` and `--output <file>` steps a cut or non-text body names can be followed over MCP too; an `output` path is relative to the server's working directory unless it is absolute.
