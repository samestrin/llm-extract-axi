package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const fixture = "../fixtures/docs-page.html"

// fixturesList is the readable package's committed fixture list: the 10 pages
// the Markdown benchmark measures (T1's fixtures, including docs-page.html).
const fixturesList = "../../internal/extract/readable/testdata/fixtures.json"

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
	switch os.Getenv("BENCH_FAKE_EXTRACT") {
	case "fail":
		fmt.Fprintln(os.Stderr, "fake extract-links failure")
		os.Exit(1)
	case "markdown":
		// A fake that always succeeds with a Markdown body, so the benchmark's
		// refusal of a no-main-content page that "succeeds" can be tested.
		fmt.Print("# fake\n\nmarkdown body\n")
		os.Exit(0)
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
	t.Setenv("FETCH_TIMEOUT", "1s")
	t.Setenv("BENCH_KEEP_ME", "1")
	env := envByName(childEnv())
	for _, banned := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_BASE_URL",
		"LLM_OPENAI_API_KEY", "MY_ANTHROPIC_API_KEY", "CORP_OPENAI_BASE_URL", "NO_PROXY", "FETCH_TIMEOUT"} {
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
	Markdown markdownResults `json:"markdown"`
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
	sumRaw, sumMD, measured := 0, 0, 0
	for _, f := range r.Markdown.Fixtures {
		if f.Error == "" {
			sumRaw += f.Raw
			sumMD += f.Markdown
			measured++
		}
	}
	mdSummary := ""
	if measured > 0 {
		mdSummary = fmt.Sprintf("Across the %d fixtures with main content: %d raw tokens vs %d Markdown tokens (%.1fx)",
			measured, sumRaw, sumMD, float64(sumRaw)/float64(sumMD))
	}
	for _, want := range []string{
		fmt.Sprintf("raw page      %d tokens", r.Counter.Raw),
		fmt.Sprintf("--axi output  %d tokens", r.Counter.AXI),
		"ratio         " + ratio,
		fmt.Sprintf("%d vs %d tokens with `cl100k_base`", r.Tiktoken.Cl100k.Raw, r.Tiktoken.Cl100k.AXI),
		fmt.Sprintf("%d vs %d with `o200k_base`", r.Tiktoken.O200k.Raw, r.Tiktoken.O200k.AXI),
		mdSummary,
	} {
		if want == "" {
			continue
		}
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

	// EST-8801: the committed Markdown counts must equal a fresh run on the
	// 10 fixtures, and the committed table must be the render of the committed
	// JSON. Before this, the Markdown figures had no committed source at all.
	md, err := measureMarkdown(builtBinary(t), fixturesList, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Markdown.Fixtures) != len(md.Fixtures) {
		t.Fatalf("results-tokens.json markdown has %d fixtures, a run measures %d; run `make bench-tokens`",
			len(r.Markdown.Fixtures), len(md.Fixtures))
	}
	for i, c := range r.Markdown.Fixtures {
		f := md.Fixtures[i]
		// Committed fixture paths are repository-relative; a test run resolves
		// them from benchmarks/tokens, two levels down.
		cPath := filepath.Clean(filepath.Join("..", "..", c.Fixture))
		if c.Name != f.Name || c.RawBytes != f.RawBytes || c.Raw != f.Raw ||
			c.Markdown != f.Markdown || c.Error != f.Error || cPath != filepath.Clean(f.Fixture) {
			t.Errorf("results-tokens.json markdown[%d] is %+v, a run gives %+v; run `make bench-tokens`", i, c, f)
		}
	}
	committed, err := os.ReadFile(filepath.Join("..", "results-tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rf resultsFile
	if err := json.Unmarshal(committed, &rf); err != nil {
		t.Fatal(err)
	}
	table, err := os.ReadFile(filepath.Join("..", "results-tokens.md"))
	if err != nil {
		t.Fatalf("results-tokens.md is missing (%v); run `make bench-tokens`", err)
	}
	if string(table) != renderResults(rf) {
		t.Error("benchmarks/results-tokens.md is not the render of results-tokens.json; run `make bench-tokens`, never edit the table by hand")
	}
}

// Defends the Markdown measurement (EST-8801): all 10 committed fixtures are
// measured, the two pages with no main content record the named error instead
// of a zero-count success, and the loopback origin is replaced before counting.
func TestMeasureMarkdownOnTheTenFixtures(t *testing.T) {
	md, err := measureMarkdown(builtBinary(t), fixturesList, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if len(md.Fixtures) != 10 {
		t.Fatalf("measured %d fixtures, the plan measures 10", len(md.Fixtures))
	}
	noMain := 0
	for _, f := range md.Fixtures {
		if f.Raw <= 0 || f.RawBytes <= 0 {
			t.Errorf("%s: raw page not counted: %+v", f.Name, f)
		}
		if f.Error != "" {
			noMain++
			if f.Error != "no main content" || f.Markdown != 0 {
				t.Errorf("%s: error row is wrong: %+v", f.Name, f)
			}
			continue
		}
		if f.Markdown <= 0 {
			t.Errorf("%s: Markdown not counted: %+v", f.Name, f)
		}
		if strings.Contains(f.output, "127.0.0.1") {
			t.Errorf("%s: the loopback origin was not replaced before counting:\n%s", f.Name, f.output)
		}
	}
	if noMain != 2 {
		t.Errorf("%d fixtures recorded no main content, the fixture list has 2", noMain)
	}
}

// Defends "reproducible" for the Markdown case: resolved links carry the
// loopback port, so two runs must count the same text and give the same numbers.
func TestMeasureMarkdownIsReproducible(t *testing.T) {
	bin := builtBinary(t)
	a, err := measureMarkdown(bin, fixturesList, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := measureMarkdown(bin, fixturesList, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two Markdown runs differ:\n%+v\n%+v", a, b)
	}
}

// Defends "nothing reports success when it did not succeed": a failed fetch is
// an error for the whole benchmark, never a table with a hole in it.
func TestMeasureMarkdownFailsWhenFetchFails(t *testing.T) {
	t.Setenv("BENCH_FAKE_EXTRACT", "fail")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := measureMarkdown(self, fixturesList, &strings.Builder{}); err == nil {
		t.Fatal("a failed fetch produced Markdown results")
	}
}

// Defends the error rows: a page the fixture list marks as having no main
// content must fail. A fetch that "succeeds" there means the fixture or the
// extraction changed, and a count from it would be a lie.
func TestMeasureMarkdownFailsWhenNoArticleSucceeds(t *testing.T) {
	t.Setenv("BENCH_FAKE_EXTRACT", "markdown")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = measureMarkdown(self, fixturesList, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "no-article") {
		t.Fatalf("err = %v, want the no-article fixture named", err)
	}
}

// Defends the inputs: a missing fixture list is an error, not zero fixtures.
func TestMeasureMarkdownFailsOnMissingFixtureList(t *testing.T) {
	if _, err := measureMarkdown(builtBinary(t), "no-such-fixtures.json", &strings.Builder{}); err == nil {
		t.Fatal("a missing fixture list produced results")
	}
}

// Defends the committed results on a failed run: nothing is written unless
// every measurement passed, so a failure leaves the committed files as they were.
func TestRunAllWritesNothingOnFailure(t *testing.T) {
	t.Setenv("BENCH_FAKE_EXTRACT", "fail")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	o := options{bin: self, fixture: fixture, fixtures: fixturesList,
		jsonPath: filepath.Join(dir, "results.json"), mdPath: filepath.Join(dir, "results.md")}
	for _, p := range []string{o.jsonPath, o.mdPath} {
		if err := os.WriteFile(p, []byte("sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := runAll(o, &strings.Builder{}); err == nil {
		t.Fatal("a failed run reported success")
	}
	for _, p := range []string{o.jsonPath, o.mdPath} {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != "sentinel" {
			t.Errorf("%s was touched by a failed run: %q (%v)", p, b, err)
		}
	}
}

// Defends the regeneration (EST-8801): a full run writes the JSON with fresh
// counts, preserves the hand-counted tiktoken section verbatim, and writes a
// table that is exactly the render of that JSON.
func TestRunAllWritesResultsAndTable(t *testing.T) {
	dir := t.TempDir()
	committed, err := os.ReadFile(filepath.Join("..", "results-tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	o := options{bin: builtBinary(t), fixture: fixture, fixtures: fixturesList,
		jsonPath: filepath.Join(dir, "results.json"), mdPath: filepath.Join(dir, "results.md")}
	if err := os.WriteFile(o.jsonPath, committed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runAll(o, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(o.jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var rf resultsFile
	if err := json.Unmarshal(b, &rf); err != nil {
		t.Fatal(err)
	}
	if rf.Version == "" || rf.Date == "" {
		t.Errorf("no version/date recorded: %+v", rf)
	}
	var old resultsFile
	if err := json.Unmarshal(committed, &old); err != nil {
		t.Fatal(err)
	}
	// The figures must survive; indentation may be normalized by the writer.
	var oldCompact, newCompact bytes.Buffer
	if err := json.Compact(&oldCompact, old.Tiktoken); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&newCompact, rf.Tiktoken); err != nil {
		t.Fatal(err)
	}
	if oldCompact.String() != newCompact.String() {
		t.Errorf("tiktoken section was not preserved: %s -> %s", oldCompact.String(), newCompact.String())
	}
	md, err := measureMarkdown(o.bin, fixturesList, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	// Compare the serialized sections: the fresh run also carries the counted
	// text in an unexported field, which the JSON file does not store.
	written, err := json.Marshal(rf.Markdown)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := json.Marshal(md)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(fresh) {
		t.Errorf("written markdown section differs from a fresh run:\n%s\n%s", written, fresh)
	}
	table, err := os.ReadFile(o.mdPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(table) != renderResults(rf) {
		t.Error("the written table is not the render of the written JSON")
	}
}

// Defends the tiktoken figures: they are counted by hand and cannot be
// re-measured offline, so a regeneration with no committed JSON to preserve
// them from is an error, not a file that silently drops them.
func TestRunAllFailsWhenResultsJSONMissing(t *testing.T) {
	dir := t.TempDir()
	o := options{bin: builtBinary(t), fixture: fixture, fixtures: fixturesList,
		jsonPath: filepath.Join(dir, "absent.json"), mdPath: filepath.Join(dir, "results.md")}
	if err := runAll(o, &strings.Builder{}); err == nil {
		t.Fatal("a run with no committed JSON to preserve tiktoken from reported success")
	}
	if _, err := os.Stat(o.mdPath); err == nil {
		t.Error("a failed run wrote the table")
	}
}

// Defends the same figures against a committed file that cannot carry them:
// corrupt JSON, or JSON with no tiktoken section, fails the regeneration
// instead of writing a results file without the hand-counted counts.
func TestRunAllFailsWhenTiktokenCannotBePreserved(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt":     "not json",
		"no-tiktoken": `{"counter": {"raw": 1, "axi": 1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			o := options{bin: builtBinary(t), fixture: fixture, fixtures: fixturesList,
				jsonPath: filepath.Join(dir, "results.json"), mdPath: filepath.Join(dir, "results.md")}
			if err := os.WriteFile(o.jsonPath, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := runAll(o, &strings.Builder{}); err == nil {
				t.Fatal("a run that cannot preserve tiktoken reported success")
			}
			b, err := os.ReadFile(o.jsonPath)
			if err != nil || string(b) != content {
				t.Errorf("the committed file was touched: %q (%v)", b, err)
			}
			if _, err := os.Stat(o.mdPath); err == nil {
				t.Error("a failed run wrote the table")
			}
		})
	}
}
