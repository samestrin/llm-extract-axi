---
name: llm-extract
description: Pull only what is needed from a large file or a web page instead of reading it whole. Use when a page, document or directory is too big to read in full.
---

# llm-extract

Run `llm-extract` with no arguments to see where the binary is and which commands reach outside.

- `llm-extract fetch <url>` fetches a URL. Network, no model.
- `llm-extract extract-links --url <url> [--context <text>]` lists and ranks links. Network; a model only with --context.
- `llm-extract extract-relevant --path <file|dir|url> --context <text>` reduces content to what the context needs. Always a model; network only for a URL.

A failed fetch or model call exits non-zero with the reason on stderr. Treat that as a failure, never as an empty result.
