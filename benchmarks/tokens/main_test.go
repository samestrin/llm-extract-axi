package main

import (
	"encoding/json"
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
		{"a  1", 4},                  // "a", " ", " ", "1": a digit takes no space
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
// (that is, non-loopback) request is pointed at a closed port. A prefixed
// credential such as LLM_OPENAI_API_KEY used to reach the child, and the
// check matched substrings, so such a machine failed it; names are compared
// whole now.
func TestChildEnvCannotReachOut(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant")
	t.Setenv("OPENAI_BASE_URL", "https://api.example.com")
	t.Setenv("LLM_OPENAI_API_KEY", "sk-prefixed")
	t.Setenv("MY_ANTHROPIC_API_KEY", "sk-ant-prefixed")
	t.Setenv("CORP_OPENAI_BASE_URL", "https://corp.example.com")
	t.Setenv("NO_PROXY", "*")
	t.Setenv("BENCH_KEEP_ME", "1")
	env := envByName(childEnv())
	for _, banned := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_BASE_URL",
		"LLM_OPENAI_API_KEY", "MY_ANTHROPIC_API_KEY", "CORP_OPENAI_BASE_URL", "NO_PROXY"} {
		if v, ok := env[banned]; ok {
			t.Errorf("child env carries %s=%s", banned, v)
		}
	}
	for name, want := range map[string]string{"HTTP_PROXY": deadProxy, "HTTPS_PROXY": deadProxy, "BENCH_KEEP_ME": "1"} {
		if env[name] != want {
			t.Errorf("child env has %s=%q, want %q", name, env[name], want)
		}
	}
}

// envByName splits NAME=value entries into a map keyed by the whole name.
func envByName(entries []string) map[string]string {
	m := map[string]string{}
	for _, kv := range entries {
		name, value, _ := strings.Cut(kv, "=")
		m[name] = value
	}
	return m
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

// tokenResults is benchmarks/results-tokens.json: the committed source of
// every token figure the README and the catalog entry quote.
type tokenResults struct {
	Version  string `json:"version"`
	Date     string `json:"date"`
	Counter  counts `json:"counter"`
	Tiktoken struct {
		Cl100k counts `json:"cl100k_base"`
		O200k  counts `json:"o200k_base"`
	} `json:"tiktoken"`
}

type counts struct {
	Raw int `json:"raw"`
	AXI int `json:"axi"`
}

func readResults(t *testing.T) tokenResults {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "results-tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r tokenResults
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("results-tokens.json: %v", err)
	}
	return r
}

// Defends the quoted token counts (EST-8204 item 12). The v2.1.0 tag shipped
// 1435 in the catalog entry after the output had shrunk to 1432, because the
// figures lived only in prose and the test checked only axi < raw. Now the
// committed results must equal a fresh run, and every place that quotes them
// must quote the committed results.
func TestQuotedTokenCountsMatchARun(t *testing.T) {
	res, err := run(builtBinary(t), fixture, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	r := readResults(t)
	if r.Counter.Raw != res.Raw || r.Counter.AXI != res.AXI {
		t.Errorf("results-tokens.json counter is %d/%d, a run gives %d/%d; re-run ./benchmarks/tokens.sh and update it",
			r.Counter.Raw, r.Counter.AXI, res.Raw, res.AXI)
	}
	ratio := fmt.Sprintf("%.1fx", float64(r.Counter.Raw)/float64(r.Counter.AXI))
	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		fmt.Sprintf("raw page      %d tokens", r.Counter.Raw),
		fmt.Sprintf("--axi output  %d tokens", r.Counter.AXI),
		"ratio         " + ratio,
		fmt.Sprintf("%d vs %d tokens with `cl100k_base`", r.Tiktoken.Cl100k.Raw, r.Tiktoken.Cl100k.AXI),
		fmt.Sprintf("%d vs %d with `o200k_base`", r.Tiktoken.O200k.Raw, r.Tiktoken.O200k.AXI),
	} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("benchmarks/README.md does not quote %q from results-tokens.json", want)
		}
	}
	entry, err := os.ReadFile(filepath.Join("..", "..", "docs", "axi-catalog-entry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	comma := func(n int) string {
		s := fmt.Sprint(n)
		if len(s) > 3 {
			s = s[:len(s)-3] + "," + s[len(s)-3:]
		}
		return s
	}
	for _, want := range []string{
		fmt.Sprintf("%s vs %s", comma(r.Counter.AXI), comma(r.Counter.Raw)),
		fmt.Sprintf("%d tokens against %d", r.Counter.AXI, r.Counter.Raw),
		fmt.Sprintf("%d raw-page tokens", r.Counter.Raw),
		fmt.Sprintf("vs %d `extract-links --axi` tokens", r.Counter.AXI),
		ratio,
	} {
		if !strings.Contains(string(entry), want) {
			t.Errorf("docs/axi-catalog-entry.yaml does not quote %q from results-tokens.json", want)
		}
	}
}
