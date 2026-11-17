package commands

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// These tests defend the EST-6933 AC5 rerun findings (EST-6950): places where
// README.md, docs/output.md, docs/axi-audit.md or the release checklist said
// something the binary does not do, and one exit code the contract got wrong.

// Defends item 4: a URL that is not http or https is a rejected flag or
// argument value, so it exits 2 like `fetch --format text`; it exited 1,
// telling an agent a retry of the same invocation might help. An empty --url
// or --context is the same malformed invocation. The specific advice survives:
// help[] still says what a valid URL looks like.
func TestBadURLIsUsageError(t *testing.T) {
	noAPIKey(t)
	for _, tc := range []struct {
		args []string
		msg  string
		help string
	}{
		{[]string{"fetch", "not-a-url"}, "invalid URL", "http://"},
		{[]string{"extract-links", "--url", "not-a-url"}, "invalid URL", "http://"},
		{[]string{"extract-links", "--url", "ftp://x"}, "invalid URL", "http://"},
		{[]string{"extract-links", "--url", ""}, "--url is required", "extract-links --help"},
		{[]string{"extract-relevant", "--context", ""}, "--context is required", "extract-relevant --help"},
	} {
		code, doc, out := runDoc(t, tc.args...)
		if code != 2 {
			t.Errorf("%q exited %d, want 2:\n%s", tc.args, code, out)
		}
		if doc["error"] != true || !strings.Contains(out, tc.msg) || !strings.Contains(out, tc.help) {
			t.Errorf("%q: want an error document with %q and help naming %q:\n%s", tc.args, tc.msg, tc.help, out)
		}
	}
}

// Defends items 1 and 2: the docs said the last `--plain`, `--axi` or `--json`
// decides a usage error's format, and that `--url --axi --bogus` prints the
// plain error. --axi is a no-op alias, so it never undoes an earlier --json,
// and in `--url --axi --bogus` it is --url's value, leaving the TOON default.
// The probes run first, so a doc fails only while the code still disagrees.
func TestDocsStateTheFormatPrecedence(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"extract-links", "--json", "--nope", "--axi"}, &out, &errb); code != 2 || !json.Valid(out.Bytes()) {
		t.Fatalf("--json --nope --axi: exit %d, want 2 and JSON; the probe, not the docs, is wrong:\n%s", code, out.String())
	}
	code, doc, s := runDoc(t, "extract-links", "--url", "--axi", "--bogus")
	if code != 2 || doc["error"] != true {
		t.Fatalf("--url --axi --bogus: exit %d, want 2 and a TOON error; the probe, not the docs, is wrong:\n%s", code, s)
	}
	for _, path := range []string{"README.md", "docs/output.md", "docs/axi-audit.md"} {
		d := readRepoFile(t, path)
		for _, stale := range []string{
			"the last `--plain`, `--axi` or `--json` on the command line decides",
			"the last `--axi`, `--plain` or `--json` wins",
			"is the last `--plain`, `--axi` or `--json`",
			"`--url --axi --bogus` prints the plain error",
		} {
			if strings.Contains(d, stale) {
				t.Errorf("%s claims %q, which the binary contradicts", path, stale)
			}
		}
	}
}

// Defends item 3: README.md and the release checklist named the demo's second
// command without the --plain the tape runs, so a reader re-running it got
// TOON, not the text in the GIF. Every place that names it must match the tape.
func TestDocsNameTheDemoCommandLikeTheTape(t *testing.T) {
	const cmd = "extract-links --fields href,score --plain"
	if !strings.Contains(readRepoFile(t, "docs/demo/llm-extract.tape"), cmd) {
		t.Fatalf("the tape no longer runs %q; update this test with the tape", cmd)
	}
	for _, path := range []string{"README.md", "docs/release-checklist.md", "docs/demo/README.md"} {
		d := readRepoFile(t, path)
		if !strings.Contains(d, cmd) {
			t.Errorf("%s does not name the demo command %q", path, cmd)
		}
		if strings.Contains(d, "`extract-links --fields href,score`") {
			t.Errorf("%s still names the demo command without --plain", path)
		}
	}
}

// Defends item 5: the house style is one line per paragraph, and the demo
// README and release checklist were hard-wrapped. Outside a code fence, a line
// that follows a non-blank line and is not a heading, list item, table row or
// fence continues the paragraph above it: a wrap.
func TestDocsAreNotHardWrapped(t *testing.T) {
	block := regexp.MustCompile("^\\s*(#|\\||```|[-*]\\s|\\d+\\.\\s)")
	for _, path := range []string{"docs/demo/README.md", "docs/release-checklist.md"} {
		lines := strings.Split(readRepoFile(t, path), "\n")
		fenced := false
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				fenced = !fenced
				continue
			}
			if fenced || i == 0 || strings.TrimSpace(line) == "" || block.MatchString(line) {
				continue
			}
			prev := lines[i-1]
			if strings.TrimSpace(prev) == "" || strings.HasPrefix(strings.TrimSpace(prev), "#") || strings.HasPrefix(strings.TrimSpace(prev), "|") || strings.HasPrefix(strings.TrimSpace(prev), "```") {
				continue
			}
			t.Errorf("%s:%d continues the line before it; write one line per paragraph:\n%s", path, i+1, line)
		}
	}
}
