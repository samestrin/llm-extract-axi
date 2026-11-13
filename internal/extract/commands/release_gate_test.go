package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	goaxi "github.com/samestrin/go-axi"
)

// These tests defend the five findings of the v1.1.0 release gate.

// hostileHref is a link a page controls: an apostrophe closes a naively
// single-quoted argument and the rest runs as shell.
const hostileHref = "https://example.com/safe?q=';id;'"

// runHelpCommand runs the command a help[] line suggests through sh, with
// llm-extract replaced by a function that prints each argument on its own
// line. A properly quoted command yields exactly the intended arguments; an
// injected one yields extra commands' output or a different split.
func runHelpCommand(t *testing.T, line, prefix string) []string {
	t.Helper()
	cmdline, ok := strings.CutPrefix(line, prefix)
	if !ok {
		t.Fatalf("help line %q lacks prefix %q", line, prefix)
	}
	cmdline = strings.Replace(cmdline, "llm-extract ", "llm_extract ", 1)
	script := `llm_extract() { for a in "$@"; do printf '%s\n' "$a"; done; }; ` + cmdline
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("sh failed on %q: %v\n%s", cmdline, err, out)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

// Defends finding 1: a link whose href carries an apostrophe must not break
// out of its quoting in the suggested next command, and an ordinary link must
// print exactly as before.
func TestExtractLinksHelpQuotesHostileLink(t *testing.T) {
	for _, href := range []string{hostileHref, "https://example.com/it's", "https://example.com/docs"} {
		h := extractLinksHelp(ExtractLinksResult{URL: "https://example.com", Links: []LinkInfo{{Href: href}}})
		got := runHelpCommand(t, h[0], "Read the top link: ")
		want := []string{"extract-relevant", "--path", href, "--context", "<what you need>"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("href %q: sh read %q, want %q", href, got, want)
		}
	}
	h := extractLinksHelp(ExtractLinksResult{URL: "https://example.com", Links: []LinkInfo{{Href: "https://example.com/docs"}}})
	if want := "Read the top link: llm-extract extract-relevant --path 'https://example.com/docs' --context '<what you need>'"; h[0] != want {
		t.Errorf("ordinary link changed:\n got %s\nwant %s", h[0], want)
	}
	// The empty-result line quotes the page URL the same way.
	h = extractLinksHelp(ExtractLinksResult{URL: hostileHref})
	got := runHelpCommand(t, h[0], "Read the page text instead: ")
	if len(got) != 5 || got[2] != hostileHref {
		t.Errorf("empty-result line not quoted: sh read %q", got)
	}
}

// Defends finding 1 end to end: a page serving the hostile link yields a help
// line that sh reads as the link itself.
func TestExtractLinksAXIHelpHostileLinkFromPage(t *testing.T) {
	page := helpPageServer(t, `<html><body><h1><a href="/safe?q=';id;'">x</a></h1></body></html>`)
	withAXI(t)
	h := helpOf(t, linksOut(t, "--url", page.URL))
	if len(h) != 1 {
		t.Fatalf("help = %v", h)
	}
	got := runHelpCommand(t, h[0].(string), "Read the top link: ")
	if len(got) != 5 || got[2] != page.URL+"/safe?q=';id;'" {
		t.Errorf("sh read %q", got)
	}
}

// Defends finding 1 on extract-relevant, whose help[] quotes the path the same
// way.
func TestExtractRelevantHelpQuotesPath(t *testing.T) {
	for _, r := range []ExtractRelevantResult{
		{Path: "/tmp/it's here.md"},
		{Path: "/tmp/a.md", ExtractedParts: []string{"x"}},
		{URL: hostileHref, ExtractedParts: []string{"x"}},
	} {
		h := extractRelevantHelp(r)
		prefix := "Broaden the context: "
		if len(r.ExtractedParts) > 0 {
			prefix = "Narrow and save it: "
		}
		got := runHelpCommand(t, h[0], prefix)
		target := r.Path + r.URL
		if len(got) < 3 || got[2] != target {
			t.Errorf("%q: sh read %q", target, got)
		}
	}
}

// Defends finding 2: pflag reads the argument after a value-taking flag as its
// value, so `--url --axi` sets url to "--axi" and leaves --axi off. The scan
// for --axi on a failed parse must agree, or a plain run gets TOON.
func TestRunValueFlagSwallowsAXI(t *testing.T) {
	for _, args := range [][]string{
		{"extract-links", "--url", "--axi", "--bogus"},
		{"extract-relevant", "-o", "--axi", "--bogus"},
		{"extract-relevant", "--context", "--axi", "--bogus"},
	} {
		code, stdout, stderr := runCLI(t, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if stdout != "" || !strings.HasPrefix(stderr, "Error: ") {
			t.Errorf("%v: want the plain error on stderr, got stdout %q stderr %q", args, stdout, stderr)
		}
	}
	// A value given with = consumes nothing, so a following --axi still counts.
	for _, args := range [][]string{
		{"extract-links", "--url=http://x", "--axi", "--bogus"},
		{"extract-links", "--json", "--axi", "--bogus"},
		{"extract-relevant", "-ofile", "--axi", "--bogus"},
	} {
		code, stdout, stderr := runCLI(t, args...)
		if code != 2 || stderr != "" {
			t.Errorf("%v: exit %d stderr %q", args, code, stderr)
		}
		axiError(t, stdout)
	}
}

// rankingLLM answers every ranking call with score for every index it was
// sent, so a link the model scored 0 reaches the output.
func rankingLLM(t *testing.T, score int) *httptest.Server {
	t.Helper()
	return fakeLLM(t, func(prompt string) string {
		var parts []string
		for i := 0; i < 200; i++ {
			if strings.Contains(prompt, fmt.Sprintf(`"index":%d,`, i)) {
				parts = append(parts, fmt.Sprintf(`{"index":%d,"score":%d}`, i, score))
			}
		}
		return `{"rankings":[` + strings.Join(parts, ",") + `]}`
	})
}

// Defends finding 3: a score of 0 is a score. --fields score must print it in
// --json, --min and --axi rather than drop it as empty.
func TestExtractLinksScoreZeroIsPrinted(t *testing.T) {
	pointExtractRelevantAt(t, rankingLLM(t, 0))
	page := linksFieldsServer(t)
	_, out, _ := runCLI(t, "extract-links", "--url", page, "--context", "c", "--fields", "href,score", "--json")
	if !strings.Contains(out, `"score": 0`) {
		t.Errorf("--json dropped score 0:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-links", "--url", page, "--context", "c", "--fields", "href,score", "--json", "--min")
	if !strings.Contains(out, `"s":0`) {
		t.Errorf("--min dropped score 0:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-links", "--url", page, "--context", "c", "--fields", "href,score", "--axi")
	if !strings.Contains(out, "{href,score}") {
		t.Errorf("--axi dropped score 0:\n%s", out)
	}
	// Not asked for, still absent.
	_, out, _ = runCLI(t, "extract-links", "--url", page, "--context", "c", "--fields", "href", "--json")
	if strings.Contains(out, "score") {
		t.Errorf("--fields href printed a score:\n%s", out)
	}
}

// Defends finding 4: a failed --context ranking keeps the heuristic links and
// their total next to the error. The docs used to say error and total never
// mix; this pins the behaviour they now describe.
func TestExtractLinksRankingFailureCarriesErrorAndTotal(t *testing.T) {
	failingLLM(t, 500)
	code, out, _ := runCLI(t, "extract-links", "--url", linksPage(t), "--context", "c", "--json")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	var r ExtractLinksResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.Error == "" || r.Total == nil || *r.Total != 2 {
		t.Errorf("want error and total 2, got %+v", r)
	}
}

// Defends finding 4 on the docs: no document may claim error and total never
// mix, since the partial ranking result above carries both.
func TestDocsDoNotClaimErrorAndTotalNeverMix(t *testing.T) {
	for _, f := range []string{"README.md", "docs/output.md", "docs/axi-audit.md", "docs/axi-catalog-entry.yaml"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, claim := range []string{"never mix", "a failed run carries `error` and no total", "a failure carries an error and no total", "A failure carries `error` and no total"} {
			if strings.Contains(s, claim) {
				t.Errorf("%s still claims %q", f, claim)
			}
		}
	}
}

// Defends finding 5: extract-relevant cuts the model input at 30,000 bytes.
// A result built from a cut input must say so in every mode, and an uncut one
// must not.
func TestExtractRelevantReportsTruncatedInput(t *testing.T) {
	pointExtractRelevantAt(t, fakeLLM(t, func(string) string { return "kept" }))
	big := filepath.Join(t.TempDir(), "big.md")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 40000)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, _ := runCLI(t, "extract-relevant", "--path", big, "--context", "c", "--json")
	var r ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Truncated, "30000 bytes") {
		t.Errorf("--json lacks the truncation note:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-relevant", "--path", big, "--context", "c", "--json", "--min")
	if !strings.Contains(out, `"tr":`) {
		t.Errorf("--min lacks the truncation note:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-relevant", "--path", big, "--context", "c", "--axi")
	doc, err := goaxi.Decode(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := doc.(map[string]any)["truncated"].(string); !strings.Contains(s, "30000 bytes") {
		t.Errorf("--axi lacks the truncation note:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-relevant", "--path", big, "--context", "c")
	if !strings.HasPrefix(out, "TRUNCATED: ") {
		t.Errorf("text lacks the truncation note:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-relevant", "--path", writeDoc(t), "--context", "c", "--json")
	if strings.Contains(out, "truncated") {
		t.Errorf("an uncut input claims truncation:\n%s", out)
	}
}

// Defends finding 5 on a partial directory run: a failed file does not hide
// that another file's input was cut.
func TestExtractRelevantPartialDirectoryReportsTruncation(t *testing.T) {
	ok := fakeLLM(t, func(string) string { return "kept" })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf strings.Builder
		_, _ = io.Copy(&buf, r.Body)
		if strings.Contains(buf.String(), "FAILME") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(buf.String()))
		ok.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	pointExtractRelevantAt(t, srv)
	noRetryDelay(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.md"), []byte(strings.Repeat("x", 40000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.md"), []byte("FAILME"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI(t, "extract-relevant", "--path", dir, "--context", "c", "--json")
	var r ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if code != 1 || r.Error == "" || len(r.ExtractedParts) != 1 {
		t.Fatalf("want a partial run, got exit %d: %s", code, out)
	}
	if !strings.Contains(r.Truncated, "1 of 2") {
		t.Errorf("directory run lacks the per-input count: %q", r.Truncated)
	}
}

// manyLinksPage serves n links with long hrefs, so the ranking prompt passes
// both the link cap and the byte cut.
func manyLinksPage(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<p><a href="/%03d/%s">link %d</a></p>`, i, strings.Repeat("p", 300), i)
	}
	b.WriteString("</body></html>")
	srv := helpPageServer(t, b.String())
	return srv.URL
}

// Defends finding 5 on extract-links: the ranking input is cut at 25,000
// bytes and at 100 links, and the result must say so; a small page must not.
func TestExtractLinksReportsTruncatedRankingInput(t *testing.T) {
	pointExtractRelevantAt(t, rankingLLM(t, 50))
	_, out, _ := runCLI(t, "extract-links", "--url", manyLinksPage(t, 120), "--context", "c", "--json")
	var r ExtractLinksResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Truncated, "25000 bytes") || !strings.Contains(r.Truncated, "100 of 120") {
		t.Errorf("--json truncation note = %q", r.Truncated)
	}
	_, out, _ = runCLI(t, "extract-links", "--url", manyLinksPage(t, 120), "--context", "c", "--json", "--min")
	if !strings.Contains(out, `"tr":`) {
		t.Errorf("--min lacks the truncation note:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-links", "--url", manyLinksPage(t, 120), "--context", "c")
	if !strings.Contains(out, "Truncated: ") {
		t.Errorf("text lacks the truncation note:\n%.400s", out)
	}
	_, out, _ = runCLI(t, "extract-links", "--url", linksPage(t), "--context", "c", "--json")
	if strings.Contains(out, "truncated") {
		t.Errorf("a small page claims truncation:\n%s", out)
	}
	// Without --context no model runs and nothing is cut.
	_, out, _ = runCLI(t, "extract-links", "--url", manyLinksPage(t, 120), "--json")
	if strings.Contains(out, "truncated") {
		t.Errorf("heuristic run claims truncation")
	}
}
