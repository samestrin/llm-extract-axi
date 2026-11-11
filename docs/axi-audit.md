# AXI conformance

`llm-extract` is built to the ten [AXI](https://axi.md) principles. The table records each as met or with a stated exception; the exceptions are deliberate, with the reason given.

| # | Principle | Status |
| --- | --- | --- |
| 1 | Token-efficient output (TOON) | Met, with an exception: `--axi` emits TOON for the home view, `extract-links` and `extract-relevant`, but `fetch` ignores the flag and prints the raw body. Fetching is a byte pipe; TOON would wrap the page, not reduce it. |
| 2 | Minimal default schemas (3–4 fields) | `extract-links` rows have four fields by default (`href`, `text`, `score`, `section`). `--fields` picks others, e.g. `--fields context,href`; an unknown field exits 2 and lists the valid ones. |
| 3 | Content truncation with a size hint and `--full` | Exception: `fetch` prints the whole body by design — that is what it is for — up to a configurable size cap (`--max-bytes` / `FETCH_MAX_BYTES`, default 16 MiB); a body larger than the cap is an error, not a truncated page. `--output FILE` is the way to keep a large page out of context. There is no truncation, size hint or `--full`. |
| 4 | Pre-computed aggregates | Met: `extract-links` reports `total`; `extract-relevant` reports `total`, `total_files` and `processed_files`, and a partial directory run reports `<n> of <total> files failed`. |
| 5 | Definitive empty states | Met: an empty success carries `total: 0` and a `message` ("no links found" / "no relevant content found"); a failure carries `error` and no total. |
| 6 | Structured errors & exit codes | Met, with an exception: exit codes are 0/1/2 as documented in [output.md](output.md), and mid-run failures of `extract-links` and `extract-relevant` carry the `error` field on stdout. Under `--axi`, every other error is on stdout too, as a TOON `error`/`message` object with `help[]` and nothing on stderr: a failure before work starts (no API key, a bad URL, a failed `fetch`), an unknown command (which lists the commands), and an unknown flag (named in the message, with the command's valid flags in `flags[]`, exit 2, before any network or model call). Without `--axi`, those errors stay on stderr. |
| 7 | Ambient context (setup hook + Agent Skill) | Met: `llm-extract setup` installs a Claude Code SessionStart hook and Agent Skill. |
| 8 | Content first | Met: a bare run shows the binary's path (`~` for home), a one-sentence description, the network/model reach table and the live configuration (API key set or not, model, base URL, fetch size cap). |
| 9 | Contextual disclosure (`help[]` next steps) | Met: under `--axi`, the home view carries a `help[]` list of next steps, and `extract-links` and `extract-relevant` carry `help[]` on empty results, on errors and after a successful result. `--json` output is unchanged. |
| 10 | Consistent way to get help | Met: `--help` on the root and every command, each listing the global `--axi` flag. |
