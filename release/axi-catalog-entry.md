# AXI catalog entry for llm-extract

One row, in the catalog's table shape, for the `kunchenguid/axi` community catalog:

```
| [`llm-extract-axi`](https://github.com/samestrin/llm-extract-axi) | samestrin | Extraction | Agent-ergonomic extraction CLI for AI agents: fetch a page or file and pull only the links or relevant content a model needs, one static Go binary, TOON output, AXI-conformant with documented exceptions. |
```

Hand-off: in the `kunchenguid/axi` repo, the owner adds an entry to the `community` list in `catalog.yaml` (name: `llm-extract-axi`, url: `https://github.com/samestrin/llm-extract-axi`, author: `samestrin`, domain: `Extraction`, description as above) and runs `pnpm run docs:gen` to regenerate the tables — `catalog.yaml` is the single source of truth there, the README/docs tables are generated, and `docs-check` fails on hand-edited generated regions. The owner commits `catalog.yaml`, `README.md` and `docs/index.html` together in that PR. No CI, tag or release action follows from this file; that PR is the whole task.
