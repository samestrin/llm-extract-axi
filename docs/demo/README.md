# Demo

`llm-extract.gif` in this directory is a real terminal session, recorded from
the VHS tape beside it, `llm-extract.tape`. The GIF is a file in this
repository: there is no hosted recording link to go stale or disappear.

The session shows three commands against the release binary:

| Command | What it shows |
| --- | --- |
| `llm-extract --version` | the version the binary reports |
| `llm-extract extract-links --axi --url ...` | one page's links as a single TOON document |
| `llm-extract extract-links --fields href,score --url ...` | the same page cut down to two fields per link |

## What the session talks to

Nothing outside the machine. A hidden step in the tape builds
`docs/demo/serve-fixture` and serves the committed fixture
`benchmarks/fixtures/docs-page.html` on `http://127.0.0.1:8765/docs-page.html`,
so the demo is the same on every machine and in CI. The address in the tape is
the fixture, not a public page.

The tape also puts `./build` first on `PATH`, so `llm-extract` in the recording
is the binary `make build` just built. No installed copy is used.

## Why the output is piped through `head`

The fixture has 50 links. Printed in full, `extract-links` scrolls the screen
several times and the recording spends its time on the tail of one list instead
of the three commands. `| head -9` and `| head -12` stop the display after a few
rows so each command's output stays on screen. The command itself runs in full;
only what is displayed is cut. The section is in the tape, so the replay check
below runs the same pipeline.

## Render it

Requires `vhs`, which records through `ttyd` and `ffmpeg`:

```bash
# macOS
brew install vhs ttyd ffmpeg
# Debian/Ubuntu
sudo apt install ttyd ffmpeg && go install github.com/charmbracelet/vhs@latest
```

Then, from the repository root:

```bash
make build
vhs docs/demo/llm-extract.tape
```

That rewrites `docs/demo/llm-extract.gif`. Commit it with the tape change.
The session is under 30 seconds; if a change pushes it past that, trim a
`Sleep`, not a command.

Re-render when a command the demo shows, its output, or the fixture changes.
`docs/release-checklist.md` makes that part of each release.

## Check it without vhs

`vhs` is not installed on the build hosts, and a recording cannot be diffed.
`tests/e2e/demo_test.go` reads the tape, replays every `Type` line of it in one
bash session (hidden setup included) against `./build/llm-extract`, and checks
the output: the version line, the `links[50]:` header, `Total Links: 50` and a
resolved link. It also fails when the tape stops writing
`docs/demo/llm-extract.gif`, or when its timings add up to 30 seconds or more.

```bash
make build
go test ./tests/e2e -run TestDemoTape -v
```

Because the check reads the `Type` lines literally, a line in the tape must not
contain a double quote, and commands must be written as
`Type "..." Enter` on one line.