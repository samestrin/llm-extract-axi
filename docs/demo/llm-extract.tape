# VHS tape for the llm-extract demo.
#
# Render it from the repository root:
#
#     make build
#     vhs docs/demo/llm-extract.tape
#
# It writes docs/demo/llm-extract.gif, which is committed, and the session runs
# in under 30 seconds. No outside host is contacted: the hidden setup below
# builds a loopback fixture server (docs/demo/serve-fixture) and serves the
# committed benchmarks/fixtures/docs-page.html on 127.0.0.1, then kills it after
# the last command.
#
# tests/e2e/demo_test.go replays every Type line below without vhs, so
# `go test ./tests/e2e` fails when a command here stops working. That is also
# why no Type line may contain a double quote: the replay reads the text between
# the first and last quote on the line. docs/demo/README.md explains the
# recording, and why the output is piped through head.
#
# `llm-extract` resolves to ./build/llm-extract, because the hidden setup puts
# ./build first on PATH: the demo runs the binary `make build` just built.

Output docs/demo/llm-extract.gif

Set Shell "bash"
Set FontFamily "JetBrains Mono"
Set FontSize 18
Set Width 1000
Set Height 620
Set Padding 24
Set MarginFill "#1e1e2e"
Set BorderRadius 8
Set Theme "Catppuccin Mocha"
Set TypingSpeed 30ms
Set PlaybackSpeed 1.5

Hide
Type "cd $(git rev-parse --show-toplevel)" Enter
Type "export PATH=$PWD/build:$PATH PS1='llm-extract demo $ '" Enter
Type "go build -o build/serve-fixture ./docs/demo/serve-fixture" Enter
Type "build/serve-fixture &" Enter
Type "SERVER=$!" Enter
Type "for i in $(seq 100); do llm-extract fetch http://127.0.0.1:8765/docs-page.html >/dev/null 2>&1 && break; sleep 0.1; done" Enter
Type "clear" Enter
Show

Type "llm-extract --version" Enter
Sleep 2s

Type "llm-extract extract-links --axi --url http://127.0.0.1:8765/docs-page.html | head -9" Enter
Sleep 4s

Type "llm-extract extract-links --fields href,score --url http://127.0.0.1:8765/docs-page.html | head -12" Enter
Sleep 4s

Hide
Type "kill $SERVER" Enter