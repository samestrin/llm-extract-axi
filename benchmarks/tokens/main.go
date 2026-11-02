// Command tokens measures what extract-links saves an agent: the token count of
// a raw documentation page against the token count of `extract-links --axi`
// run on that same page.
//
// It makes no network call. The page is a committed fixture, served from an
// in-process server on the loopback interface, and the tokenizer is built in,
// so there is nothing to download either.
//
// The counter is cl100k_base's pre-tokenizer: the split pattern GPT-4's BPE
// applies before any merge. BPE never merges across those pieces, so each count
// is a lower bound on the cl100k_base token count of the same text. The ratio of
// the two is what this reports; see benchmarks/README.md for what that means.
//
// Usage, from the repository root after `make build`:
//
//	go run ./benchmarks/tokens [-bin build/llm-extract] [-fixture benchmarks/fixtures/docs-page.html]
package main

import (
	"bytes"
	"context"
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

// result is one measurement.
type result struct {
	Raw    int    // tokens in the raw page
	AXI    int    // tokens in the extract-links --axi output
	Output string // the --axi output that was counted
}

func main() {
	bin := flag.String("bin", filepath.Join("build", "llm-extract"), "llm-extract binary to measure")
	fixture := flag.String("fixture", filepath.Join("benchmarks", "fixtures", "docs-page.html"), "HTML page to serve")
	flag.Parse()
	if _, err := run(*bin, *fixture, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "benchmark failed:", err)
		os.Exit(1)
	}
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

	res := result{Raw: countTokens(string(page)), AXI: countTokens(out), Output: out}
	_, _ = fmt.Fprintf(w, "fixture:   %s (%d bytes)\n", fixture, len(page))
	_, _ = fmt.Fprintf(w, "command:   llm-extract extract-links --url %s --axi\n", strings.Replace(url, srv.URL, placeholderOrigin, 1))
	_, _ = fmt.Fprintf(w, "tokenizer: cl100k_base pre-tokenizer (offline; a lower bound on cl100k_base tokens)\n\n")
	_, _ = fmt.Fprintf(w, "raw page      %d tokens\n", res.Raw)
	_, _ = fmt.Fprintf(w, "--axi output  %d tokens\n", res.AXI)
	_, _ = fmt.Fprintf(w, "ratio         %.1fx (raw / --axi)\n", float64(res.Raw)/float64(res.AXI))
	return res, nil
}

// childEnv is the environment minus model credentials and proxy settings, with
// every proxy pointed at deadProxy. extract-links without --context calls no
// model; dropping the keys makes sure of it.
func childEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name := strings.ToUpper(kv[:strings.IndexByte(kv+"=", '=')])
		if strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "ANTHROPIC_") ||
			strings.HasSuffix(name, "_PROXY") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HTTP_PROXY="+deadProxy, "HTTPS_PROXY="+deadProxy)
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
