# Vision

`llm-extract-axi` exists so that an agent can pull only what it needs from a large file or a web page, without filling its context.
It serves agents first, and the people who run them second.
It turns outside content into something smaller, using the network or a model when it has to.
It owns exactly one thing: getting content and reducing it.

## Nothing reports success when it did not succeed

This rule sits above every other rule here.
A failed fetch or a failed model call is an error, never an empty result.
Empty extraction and failed extraction are never reported the same way.
A truncated or partial result says that it is partial.

## Costs are visible

This tool is the one that may spend money or reach the network, and it says so.
Every command states whether it uses the network, a model, or neither.
A caller can see the model and the size of what was sent.
The default model endpoint is any OpenAI-compatible URL, and vendor keys are a simple fallback.
Keys come from the environment, and no vendor is special.
A cache is off by default, and a cached result always says so.

## Plain code first

A command that can work without a model does.
`extract-links` ranks links with plain code, and a model only when `--context` is given.
`fetch` never calls a model.
A page that needs a browser is reported as that, not returned as an empty result.
It sends an honest user agent, and it fetches a single page, so it does not apply crawler rules such as `robots.txt`.

## The CLI is the product

`llm-extract` is the primary surface.
The MCP server shells out to the CLI, is a fallback, and holds no logic of its own.
Output follows the AXI principles, and TOON through `go-axi` is the format given to a model.

## Scope

It is not a crawler, a browser, a search engine or a chat tool.
An agent that needs several pages calls `fetch` several times.
It does not write to the files it reads.
Local deterministic facts belong to `llm-data-axi`, and code search belongs to `llm-semantic-axi`.
Plans and debt live in a private planning repo.

A change aligns when it gives an agent less to read, for a visible cost.
A change should be resisted when it hides a failure, hides a cost, or adds work that a sibling tool owns.
