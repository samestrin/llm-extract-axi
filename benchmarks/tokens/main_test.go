package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fixture = "../fixtures/docs-page.html"

// builtBinary returns ../../build/llm-extract, failing (not skipping) when it is
// absent: a skipped benchmark test is one nobody notices has stopped running.
func builtBinary(t *testing.T) string {
	t.Helper()
	name := "llm-extract"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "build", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s is not built (%v); run `make build` before `go test`", path, err)
	}
	return path
}

// TestMain lets the test binary stand in for a failing llm-extract, so the
// non-zero-exit path is tested without a shell script per platform.
func TestMain(m *testing.M) {
	if os.Getenv("BENCH_FAKE_EXTRACT") == "fail" {
		fmt.Fprintln(os.Stderr, "fake extract-links failure")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// Defends the counter's definition: the README calls each count a lower bound
// on cl100k_base tokens, which holds only if the pieces are exactly the ones
// cl100k's split pattern produces. Each case is that pattern's split.
func TestCountTokensFollowsCl100kSplit(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"Hello world", 2},           // "Hello", " world"
		{"I'm", 2},                   // "I", "'m"
		{"WE'LL", 2},                 // contractions are case-insensitive
		{"12345", 2},                 // "123", "45"
		{`<a href="x">`, 5},          // "<a", " href", "=\"", "x", "\">"
		{"a  b", 3},                  // "a", " ", " b"
		{"a\n\n  b", 4},              // "a", "\n\n", " ", " b"
		{"x !!\n", 2},                // "x", " !!\n"
		{"end   ", 2},                // "end", "   "
		{"naïve café", 2},            // letters are Unicode letters
		{"\tword", 1},                // one non-letter prefix joins the word
		{"<div class=\"nav\">", 5},   // "<div", " class", "=\"", "nav", "\">"
		{"https://example.com/a", 5}, // "https", "://", "example", ".com", "/a"
		{"0.5 - 10x", 7},             // "0", ".", "5", " -", " ", "10", "x"
		{"'sam", 2},                  // the contraction "'s" wins, then "am"
	}
	for _, c := range cases {
		if got := countTokens(c.in); got != c.want {
			t.Errorf("countTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// Defends the deliverable: one run prints the raw-page count, the --axi count
// and their ratio, measured from the real binary against the committed
// fixture. Checks the payload, not just that it ran.
func TestRunPrintsRawAXIAndRatio(t *testing.T) {
	var out strings.Builder
	res, err := run(builtBinary(t), fixture, &out)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if res.Raw <= 0 || res.AXI <= 0 {
		t.Fatalf("counts not positive: %+v", res)
	}
	if res.AXI >= res.Raw {
		t.Errorf("--axi output (%d) is not smaller than the raw page (%d)", res.AXI, res.Raw)
	}
	// The --axi output really is the extract-links result for this fixture.
	if !strings.Contains(res.Output, "/docs/getting-started/install") {
		t.Errorf("--axi output does not carry a fixture link:\n%s", res.Output)
	}
	text := out.String()
	for _, want := range []string{
		fmt.Sprintf("raw page      %d", res.Raw),
		fmt.Sprintf("--axi output  %d", res.AXI),
		fmt.Sprintf("ratio         %.1fx", float64(res.Raw)/float64(res.AXI)),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

// Defends "reproducible": the loopback port is in every resolved link, so the
// count must not depend on which port the server happened to get.
func TestRunIsReproducible(t *testing.T) {
	bin := builtBinary(t)
	a, err := run(bin, fixture, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := run(bin, fixture, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Raw != b.Raw || a.AXI != b.AXI || a.Output != b.Output {
		t.Errorf("two runs differ: %d/%d vs %d/%d", a.Raw, a.AXI, b.Raw, b.AXI)
	}
	if strings.Contains(a.Output, "127.0.0.1") {
		t.Errorf("the loopback origin was not replaced before counting:\n%s", a.Output)
	}
}

// Defends "no network": the child gets no model credentials, and any proxied
// (that is, non-loopback) request is pointed at a closed port.
func TestChildEnvCannotReachOut(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant")
	t.Setenv("OPENAI_BASE_URL", "https://api.example.com")
	t.Setenv("NO_PROXY", "*")
	env := strings.Join(childEnv(), "\n")
	for _, banned := range []string{"OPENAI_API_KEY=", "ANTHROPIC_API_KEY=", "OPENAI_BASE_URL=", "NO_PROXY=*"} {
		if strings.Contains(env, banned) {
			t.Errorf("child env carries %s", banned)
		}
	}
	for _, want := range []string{"HTTP_PROXY=" + deadProxy, "HTTPS_PROXY=" + deadProxy} {
		if !strings.Contains(env, want) {
			t.Errorf("child env lacks %s", want)
		}
	}
}

// Defends "nothing reports success when it did not succeed": a failed
// extract-links is an error, never a ratio computed from its error output.
func TestRunFailsWhenExtractFails(t *testing.T) {
	t.Setenv("BENCH_FAKE_EXTRACT", "fail")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	_, err = run(self, fixture, &out)
	if err == nil || !strings.Contains(err.Error(), "fake extract-links failure") {
		t.Fatalf("run err = %v, want the child's stderr", err)
	}
	if strings.Contains(out.String(), "ratio") {
		t.Errorf("a ratio was printed for a failed run:\n%s", out.String())
	}
}

// Defends against a meaningless ratio: a page with no links is an error.
func TestRunFailsOnPageWithNoLinks(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.html")
	if err := os.WriteFile(empty, []byte("<html><body><p>no links</p></body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(builtBinary(t), empty, &strings.Builder{}); err == nil {
		t.Fatal("a page with no links produced a ratio")
	}
}

func TestRunFailsOnMissingFixtureOrBinary(t *testing.T) {
	// Defends both inputs: a missing file is an error, not a zero count.
	if _, err := run(builtBinary(t), "no-such-fixture.html", &strings.Builder{}); err == nil {
		t.Error("missing fixture did not fail")
	}
	if _, err := run("no-such-binary", fixture, &strings.Builder{}); err == nil {
		t.Error("missing binary did not fail")
	}
}
