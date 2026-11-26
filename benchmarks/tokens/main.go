// Command tokens measures what extract-links and `fetch --format markdown`
// save an agent: the token count of a raw page against the token count of the
// tool's output on that same page.
//
// It makes no network call. Every page is a committed fixture, served from an
// in-process server on the loopback interface, and the tokenizer is built in,
// so there is nothing to download either.
//
// Two measurements:
//
//   - extract-links --axi on benchmarks/fixtures/docs-page.html, the original
//     single-fixture report.
//   - fetch --format markdown on the 10 fixtures of the readable package
//     (internal/extract/readable/testdata/fixtures.json). A page the list marks
//     as having no main content must fail with the named error; its row carries
//     the error, never a zero-count success.
//
// A successful run writes benchmarks/results-tokens.json (preserving the
// hand-counted tiktoken section, which cannot be re-measured offline) and
// renders benchmarks/results-tokens.md from it. A failed run writes nothing.
//
// The counter is cl100k_base's pre-tokenizer: the split pattern GPT-4's BPE
// applies before any merge. BPE never merges across those pieces, so each count
// is a lower bound on the cl100k_base token count of the same text. The ratio of
// the two is what this reports; see benchmarks/README.md for what that means.
//
// Usage, from the repository root after `make build` (or just `make bench-tokens`):
//
//	go run ./benchmarks/tokens [-bin build/llm-extract] [-fixture benchmarks/fixtures/docs-page.html]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// placeholderOrigin replaces the loopback origin in the --axi output before it
// is counted. Every resolved link carries that origin, and its port changes from
// run to run; the placeholder makes the count the same for every reader.
const placeholderOrigin = "https://docs.example.com"

// deadProxy is where the child sends any request Go would proxy. Go never
// proxies a loopback request, so the fixture still loads, while any request to
// another host would go to a closed port and fail rather than leave the machine.
const deadProxy = "http://127.0.0.1:9"

// linksHeader is the TOON header of a non-empty links array.
var linksHeader = regexp.MustCompile(`(?m)^links\[[1-9][0-9]*\]:`)

// axiCommand is the extract-links command recorded in the results file, with
// the placeholder origin already in place.
const axiCommand = "llm-extract extract-links --url " + placeholderOrigin + "/docs/getting-started/ --axi"

// mdCommand is the fetch command the Markdown benchmark runs on each fixture.
const mdCommand = "llm-extract fetch <url> --format markdown --plain --full"

// noMainContentText is readable.ErrNoMainContent's message. The benchmark
// imports only the standard library, so the text is repeated here; the
// readable package's tests defend the message itself.
const noMainContentText = "no main content found on the page"

// result is one measurement.
type result struct {
	Raw    int    // tokens in the raw page
	AXI    int    // tokens in the extract-links --axi output
	Bytes  int    // size of the raw page in bytes
	Output string // the --axi output that was counted
}

// options are the command-line flags.
type options struct {
	bin, fixture, fixtures, jsonPath, mdPath string
}

func main() {
	var o options
	flag.StringVar(&o.bin, "bin", filepath.Join("build", "llm-extract"), "llm-extract binary to measure")
	flag.StringVar(&o.fixture, "fixture", filepath.Join("benchmarks", "fixtures", "docs-page.html"), "HTML page for the extract-links case")
	flag.StringVar(&o.fixtures, "fixtures", filepath.Join("internal", "extract", "readable", "testdata", "fixtures.json"), "fixture list for the Markdown case")
	flag.StringVar(&o.jsonPath, "json", filepath.Join("benchmarks", "results-tokens.json"), "counts, written by a successful run")
	flag.StringVar(&o.mdPath, "md", filepath.Join("benchmarks", "results-tokens.md"), "the table rendered from -json")
	flag.Parse()
	if err := runAll(o, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "benchmark failed:", err)
		os.Exit(1)
	}
}

// runAll takes every measurement and only then writes the results file and
// the table. Any failure returns before a file is touched, so a failed run
// leaves the committed results as they were.
func runAll(o options, w io.Writer) error {
	res, err := run(o.bin, o.fixture, w)
	if err != nil {
		return err
	}
	md, err := measureMarkdown(o.bin, o.fixtures, w)
	if err != nil {
		return err
	}
	return writeResults(o, res, md)
}

// run serves fixture on loopback, runs `bin extract-links --axi` against it and
// writes the report to w. Any failure is returned before a ratio is printed.
func run(bin, fixture string, w io.Writer) (result, error) {
	page, err := os.ReadFile(fixture)
	if err != nil {
		return result{}, err
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = rw.Write(page)
	}))
	defer srv.Close()

	url := srv.URL + "/docs/getting-started/"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "extract-links", "--url", url, "--axi")
	cmd.Env = childEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return result{}, fmt.Errorf("extract-links: %v: %s", err, strings.TrimSpace(stderr.String()+stdout.String()))
	}
	out := strings.ReplaceAll(stdout.String(), srv.URL, placeholderOrigin)
	// A page with no links gives a ratio that measures nothing. --axi prints
	// the list as "links[N]:" and omits it when there are none.
	if !linksHeader.MatchString(out) {
		return result{}, errors.New("extract-links found no links on the page:\n" + out)
	}

	res := result{Raw: countTokens(string(page)), AXI: countTokens(out), Bytes: len(page), Output: out}
	_, _ = fmt.Fprintf(w, "fixture:   %s (%d bytes)\n", fixture, len(page))
	_, _ = fmt.Fprintf(w, "command:   llm-extract extract-links --url %s --axi\n", strings.Replace(url, srv.URL, placeholderOrigin, 1))
	_, _ = fmt.Fprintf(w, "tokenizer: cl100k_base pre-tokenizer (offline; a lower bound on cl100k_base tokens)\n\n")
	_, _ = fmt.Fprintf(w, "raw page      %d tokens\n", res.Raw)
	_, _ = fmt.Fprintf(w, "--axi output  %d tokens\n", res.AXI)
	_, _ = fmt.Fprintf(w, "ratio         %.1fx (raw / --axi)\n", float64(res.Raw)/float64(res.AXI))
	return res, nil
}

// childEnv is the environment minus model credentials, FETCH_* tuning and
// proxy settings, with every proxy pointed at deadProxy. Neither command
// calls a model here; dropping the keys makes sure of it.
func childEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name := strings.ToUpper(kv[:strings.IndexByte(kv+"=", '=')])
		if modelVar(name) || strings.HasPrefix(name, "FETCH_") || strings.HasSuffix(name, "_PROXY") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HTTP_PROXY="+deadProxy, "HTTPS_PROXY="+deadProxy)
}

// modelVar reports whether the upper-cased variable name could configure a
// model call: anything under OPENAI_ or ANTHROPIC_, or a prefixed key or
// endpoint such as LLM_OPENAI_API_KEY that another tool exports.
func modelVar(name string) bool {
	return strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "ANTHROPIC_") ||
		strings.HasSuffix(name, "OPENAI_API_KEY") || strings.HasSuffix(name, "ANTHROPIC_API_KEY") ||
		strings.HasSuffix(name, "OPENAI_BASE_URL")
}

// mdFixture is one entry of the readable package's fixture list. Only the
// fields the benchmark needs are decoded.
type mdFixture struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	NoMainContent bool   `json:"no_main_content"`
}

// mdResult is one fixture's Markdown measurement. A page with no main content
// carries Error and no Markdown count: an error is never a zero-count success.
type mdResult struct {
	Name     string `json:"name"`
	Fixture  string `json:"fixture"`
	RawBytes int    `json:"raw_bytes"`
	Raw      int    `json:"raw"`
	Markdown int    `json:"markdown,omitempty"`
	Error    string `json:"error,omitempty"`

	output string // the counted Markdown, loopback origin replaced; not stored
}

// markdownResults is the markdown section of the results file.
type markdownResults struct {
	Command  string     `json:"command"`
	Fixtures []mdResult `json:"fixtures"`
}

// counts is a raw/output token pair.
type counts struct {
	Raw int `json:"raw"`
	AXI int `json:"axi"`
}

// resultsFile is benchmarks/results-tokens.json. Tiktoken is kept as raw JSON:
// those counts are made by hand with the tiktoken package and cannot be
// re-measured offline, so a regeneration preserves them unchanged.
type resultsFile struct {
	Version      string          `json:"version"`
	Date         string          `json:"date"`
	Note         string          `json:"note"`
	Fixture      string          `json:"fixture"`
	FixtureBytes int             `json:"fixture_bytes"`
	Command      string          `json:"command"`
	Counter      counts          `json:"counter"`
	Tiktoken     json.RawMessage `json:"tiktoken"`
	Markdown     markdownResults `json:"markdown"`
}

// measureMarkdown runs `fetch --format markdown --plain --full` on every
// fixture in the list and counts the raw page against the Markdown. Fixture
// paths resolve against the list's directory, as in the readable tests. A
// fixture marked no_main_content must fail with the named error; any other
// outcome, on any fixture, fails the whole measurement.
func measureMarkdown(bin, fixturesPath string, w io.Writer) (markdownResults, error) {
	b, err := os.ReadFile(fixturesPath)
	if err != nil {
		return markdownResults{}, err
	}
	var list []mdFixture
	if err := json.Unmarshal(b, &list); err != nil {
		return markdownResults{}, fmt.Errorf("%s: %w", fixturesPath, err)
	}
	if len(list) == 0 {
		return markdownResults{}, fmt.Errorf("%s lists no fixtures", fixturesPath)
	}
	md := markdownResults{Command: mdCommand}
	_, _ = fmt.Fprintf(w, "\nfetch --format markdown on %d fixtures (%s)\n", len(list), fixturesPath)
	for _, f := range list {
		path := filepath.Clean(filepath.Join(filepath.Dir(fixturesPath), f.Path))
		page, err := os.ReadFile(path)
		if err != nil {
			return markdownResults{}, err
		}
		res, err := measureOneMarkdown(bin, f, path, page)
		if err != nil {
			return markdownResults{}, err
		}
		md.Fixtures = append(md.Fixtures, res)
		if res.Error != "" {
			_, _ = fmt.Fprintf(w, "%-20s raw %6d tokens  markdown: %s\n", res.Name, res.Raw, res.Error)
		} else {
			_, _ = fmt.Fprintf(w, "%-20s raw %6d tokens  markdown %6d tokens  ratio %.1fx\n",
				res.Name, res.Raw, res.Markdown, float64(res.Raw)/float64(res.Markdown))
		}
	}
	return md, nil
}

// measureOneMarkdown serves one fixture on loopback and measures it. The
// loopback origin is replaced with the placeholder before counting, so the
// count does not depend on the port the server happened to get.
func measureOneMarkdown(bin string, f mdFixture, path string, page []byte) (mdResult, error) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = rw.Write(page)
	}))
	defer srv.Close()

	url := srv.URL + "/" + f.Name + ".html"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "fetch", url, "--format", "markdown", "--plain", "--full")
	cmd.Env = childEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	res := mdResult{Name: f.Name, Fixture: filepath.ToSlash(path), RawBytes: len(page), Raw: countTokens(string(page))}
	if f.NoMainContent {
		if runErr == nil {
			return mdResult{}, fmt.Errorf("%s is marked as having no main content, but fetch --format markdown succeeded:\n%s", f.Name, stdout.String())
		}
		combined := stderr.String() + stdout.String()
		if !strings.Contains(combined, noMainContentText) {
			return mdResult{}, fmt.Errorf("%s: fetch failed without naming the missing main content: %v: %s", f.Name, runErr, strings.TrimSpace(combined))
		}
		res.Error = "no main content"
		return res, nil
	}
	if runErr != nil {
		return mdResult{}, fmt.Errorf("%s: fetch --format markdown: %v: %s", f.Name, runErr, strings.TrimSpace(stderr.String()+stdout.String()))
	}
	out := strings.ReplaceAll(stdout.String(), srv.URL, placeholderOrigin)
	res.Markdown = countTokens(out)
	if res.Markdown == 0 {
		return mdResult{}, fmt.Errorf("%s: fetch --format markdown printed an empty body", f.Name)
	}
	res.output = out
	return res, nil
}

// writeResults writes the results file and renders the table from it. The
// committed JSON must exist: it carries the hand-counted tiktoken figures,
// which a regeneration preserves rather than drops.
func writeResults(o options, res result, md markdownResults) error {
	committed, err := os.ReadFile(o.jsonPath)
	if err != nil {
		return fmt.Errorf("cannot regenerate results (the committed file carries the hand-counted tiktoken figures): %w", err)
	}
	var old resultsFile
	if err := json.Unmarshal(committed, &old); err != nil {
		return fmt.Errorf("%s: %w", o.jsonPath, err)
	}
	if len(old.Tiktoken) == 0 {
		return fmt.Errorf("%s has no tiktoken section to preserve", o.jsonPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.bin, "--version")
	cmd.Env = childEnv()
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("--version: %w", err)
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return errors.New("--version printed nothing")
	}

	r := resultsFile{
		Version:      version,
		Date:         time.Now().UTC().Format("2006-01-02"),
		Note:         "regenerated by `make bench-tokens`; counter and markdown measured offline, tiktoken re-counted by hand (see benchmarks/README.md)",
		Fixture:      filepath.ToSlash(o.fixture),
		FixtureBytes: res.Bytes,
		Command:      axiCommand,
		Counter:      counts{Raw: res.Raw, AXI: res.AXI},
		Tiktoken:     old.Tiktoken,
		Markdown:     md,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep "<url>" readable in the committed file
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return err
	}
	if err := os.WriteFile(o.jsonPath, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.WriteFile(o.mdPath, []byte(renderResults(r)), 0o644)
}

// renderResults renders benchmarks/results-tokens.md. It is a pure function of
// the results file, so the test can check that the committed table is exactly
// this render of the committed JSON.
func renderResults(r resultsFile) string {
	var b strings.Builder
	b.WriteString("# Token benchmark results\n\n")
	b.WriteString("Generated by `make bench-tokens` from [`results-tokens.json`](results-tokens.json); never edit by hand.\n\n")
	fmt.Fprintf(&b, "- Version: `llm-extract %s`\n", r.Version)
	fmt.Fprintf(&b, "- Measured: %s\n", r.Date)
	b.WriteString("- Counter: cl100k_base pre-tokenizer (offline; a lower bound on cl100k_base tokens)\n\n")
	b.WriteString("## extract-links --axi\n\n")
	fmt.Fprintf(&b, "Fixture: `%s` (%d bytes). Command: `%s`.\n\n", r.Fixture, r.FixtureBytes, r.Command)
	b.WriteString("```\n")
	fmt.Fprintf(&b, "raw page      %d tokens\n", r.Counter.Raw)
	fmt.Fprintf(&b, "--axi output  %d tokens\n", r.Counter.AXI)
	fmt.Fprintf(&b, "ratio         %.1fx (raw / --axi)\n", float64(r.Counter.Raw)/float64(r.Counter.AXI))
	b.WriteString("```\n\n")
	b.WriteString("## fetch --format markdown\n\n")
	fmt.Fprintf(&b, "Command: `%s`, each fixture served on loopback. A page with no main content is an error, not an empty result; its row carries the error instead of a count.\n\n", r.Markdown.Command)
	b.WriteString("| Fixture | Raw HTML tokens | Markdown tokens | Ratio (raw / Markdown) |\n")
	b.WriteString("|---|---:|---:|---:|\n")
	sumRaw, sumMD, measured := 0, 0, 0
	for _, f := range r.Markdown.Fixtures {
		if f.Error != "" {
			fmt.Fprintf(&b, "| `%s` | %d | — (%s) | — |\n", f.Fixture, f.Raw, f.Error)
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %d | %d | %.1fx |\n", f.Fixture, f.Raw, f.Markdown, float64(f.Raw)/float64(f.Markdown))
		sumRaw += f.Raw
		sumMD += f.Markdown
		measured++
	}
	if measured > 0 {
		fmt.Fprintf(&b, "\nAcross the %d fixtures with main content: %d raw tokens vs %d Markdown tokens (%.1fx).\n",
			measured, sumRaw, sumMD, float64(sumRaw)/float64(sumMD))
	}
	return b.String()
}

// countTokens counts the pieces cl100k_base's split pattern cuts s into:
//
//	(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}{1,3}|
//	 ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+
//
// Go's regexp has no lookahead, so the alternatives are matched by hand, in
// the pattern's order.
func countTokens(s string) int {
	n := 0
	for i := 0; i < len(s); n++ {
		i += piece(s[i:])
	}
	return n
}

// piece returns the byte length of the piece at the start of s (len(s) > 0).
func piece(s string) int {
	r, size := utf8.DecodeRuneInString(s)

	// (?i:'s|'t|'re|'ve|'m|'ll|'d)
	if r == '\'' {
		lower := strings.ToLower(s[1:min(len(s), 3)])
		for _, c := range []string{"s", "t", "re", "ve", "m", "ll", "d"} {
			if strings.HasPrefix(lower, c) {
				return 1 + len(c)
			}
		}
	}

	// [^\r\n\p{L}\p{N}]?\p{L}+
	if unicode.IsLetter(r) {
		return size + runLen(s[size:], unicode.IsLetter, -1)
	}
	if r != '\r' && r != '\n' && !unicode.IsNumber(r) {
		if k := runLen(s[size:], unicode.IsLetter, -1); k > 0 {
			return size + k
		}
	}

	// \p{N}{1,3}
	if unicode.IsNumber(r) {
		return runLen(s, unicode.IsNumber, 3)
	}

	//  ?[^\s\p{L}\p{N}]+[\r\n]*
	start := 0
	if r == ' ' {
		start = 1
	}
	if k := runLen(s[start:], isPunct, -1); k > 0 {
		end := start + k
		return end + runLen(s[end:], isNewline, -1)
	}

	// The rest all start with whitespace: r is a space here.
	ws := runLen(s, unicode.IsSpace, -1)

	// \s*[\r\n]+ : through the last newline in the whitespace run.
	if last := strings.LastIndexAny(s[:ws], "\r\n"); last >= 0 {
		return last + 1
	}

	// \s+(?!\S) : the run, less its last character when a non-space follows.
	if ws == len(s) {
		return ws
	}
	_, lastSize := utf8.DecodeLastRuneInString(s[:ws])
	if ws > lastSize {
		return ws - lastSize
	}
	// \s+ : a single space before a non-space.
	return ws
}

// runLen is the byte length of the leading run of runes in s matching f, of at
// most max runes (max < 0 for no limit).
func runLen(s string, f func(rune) bool, max int) int {
	n, count := 0, 0
	for n < len(s) && count != max {
		r, size := utf8.DecodeRuneInString(s[n:])
		if !f(r) {
			break
		}
		n += size
		count++
	}
	return n
}

func isPunct(r rune) bool {
	return !unicode.IsSpace(r) && !unicode.IsLetter(r) && !unicode.IsNumber(r)
}

func isNewline(r rune) bool { return r == '\r' || r == '\n' }
