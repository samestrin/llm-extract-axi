---
name: llm-extract
description: Pull only what is needed from a large file or a web page instead of reading it whole. Use when a page, document or directory is too big to read in full.
---

# llm-extract

Run `llm-extract` with no arguments to see where the binary is and which commands reach outside. If `llm-extract` is not on PATH, install it first: `curl -fsSL https://raw.githubusercontent.com/samestrin/llm-extract-axi/main/install.sh | bash` puts it in `~/.local/bin`; this skill only calls it by name.

- `llm-extract fetch <url>` fetches a URL. Network, no model. Prints a TOON envelope: `bytes` is the whole-body size, `truncated` says the body was cut, and `body` holds the first 4096 bytes. `--head N` sets the cut, `--full` returns the whole body, `--fields a,b` selects envelope fields, `--output <file>` saves the whole body to a file. `--format markdown` makes the body the main content of an HTML page as Markdown, with no model: `bytes` and `truncated` then describe the Markdown, `source_bytes` the fetched HTML, and `title`/`byline` come from the page. A page that is not HTML, or has no main content, is an exit-1 error whose `help` line names running again without `--format markdown`, never an empty body. A body that is not UTF-8 text (an image, a PDF) comes back as `body: ""` with `truncated: true`; save its bytes with `--output <file>`.
- `llm-extract extract-links --url <url> [--context <text>]` lists and ranks links. Network; a model only with --context.
- `llm-extract extract-relevant --path <file|dir|url> --context <text>` reduces content to what the context needs. Always a model; network only for a URL. Large inputs are read in chunks, one model call each; `--max-chunks` (default 8) bounds the chunks read per input and `model_calls` counts the requests. A `truncated` note means chunks past the bound were not read: raise `--max-chunks` to read more.

Output is TOON by default; `--plain` prints human-readable text (for `fetch`, the body bytes alone, still cut unless `--full`; with `--output <file>`, the line `Saved <n> bytes [HTTP <status> via <tier>, <format>] -> <file>`).

A failed fetch or model call exits non-zero with `error` and the reason on stdout in the active output format; it is never reported as an empty result. A failed run may still carry results beside `error` (the unranked links when `extract-links --context` has no API key, the files a directory run finished): treat them as partial. Exit 2 means the command or its configuration is wrong (a bad flag, no API key): fix that before running again. Exit 1 is a run that failed, such as a fetch the server refused; the `help` lines name the next step.
