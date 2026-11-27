package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	goaxi "github.com/samestrin/go-axi"

	"github.com/samestrin/llm-extract-axi/v2/pkg/output"
)

// axiOffAfter restores the package-global AXI default a run() with --axi
// leaves on, so a later test that builds a command directly is not TOON.
func axiOffAfter(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		GlobalAXIOutput = false
		output.SetDefaultAXI(false)
	})
}

// These tests defend the five findings of the v1.1.0 release gate.

// Defends finding 2: pflag reads the argument after a value-taking flag as its
// value, so `--url --plain` sets url to "--plain" and leaves --plain off. The
// scan for the format flags on a failed parse must agree, or a TOON run gets
// text.
func TestRunValueFlagSwallowsAXI(t *testing.T) {
	axiOffAfter(t)
	for _, args := range [][]string{
		{"extract-links", "--url", "--plain", "--bogus"},
		{"extract-relevant", "-o", "--plain", "--bogus"},
		{"extract-relevant", "--context", "--axi=false", "--bogus"},
	} {
		code, stdout, stderr := runCLI(t, args...)
		if code != 2 || stderr != "" {
			t.Errorf("%v: exit %d, want 2; stderr %q", args, code, stderr)
		}
		axiError(t, stdout)
	}
	// A value given with = consumes nothing, so a following --plain still
	// counts, and so does --json: the error leaves TOON for text or JSON, on
	// stdout either way.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"extract-links", "--url=http://x", "--plain", "--bogus"}, "Error: "},
		{[]string{"extract-links", "--json", "--axi", "--bogus"}, "{"},
		{[]string{"extract-relevant", "-ofile", "--plain", "--bogus"}, "Error: "},
	} {
		code, stdout, stderr := runCLI(t, tc.args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2", tc.args, code)
		}
		if stderr != "" || !strings.HasPrefix(stdout, tc.want) {
			t.Errorf("%v: want the error on stdout starting %q, got stdout %q stderr %q", tc.args, tc.want, stdout, stderr)
		}
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
	axiOffAfter(t)
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

// Defends finding 5: a result built from a cut input must say so in every
// mode, and an uncut one must not. Since epic 12 the input is chunked, not
// cut at 30,000 bytes; what is left unread is the chunks past --max-chunks,
// so the 40,000-byte file is read with --max-chunks 1.
func TestExtractRelevantReportsTruncatedInput(t *testing.T) {
	axiOffAfter(t)
	pointExtractRelevantAt(t, fakeLLM(t, func(string) string { return "kept" }))
	big := filepath.Join(t.TempDir(), "big.md")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 40000)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, _ := runCLI(t, "extract-relevant", "--path", big, "--context", "c", "--max-chunks", "1", "--json")
	var r ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Truncated, "read 1 of at least 2 chunks") {
		t.Errorf("--json lacks the truncation note:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-relevant", "--path", big, "--context", "c", "--max-chunks", "1", "--json", "--min")
	if !strings.Contains(out, `"tr":`) {
		t.Errorf("--min lacks the truncation note:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-relevant", "--path", big, "--context", "c", "--max-chunks", "1", "--axi")
	doc, err := goaxi.Decode(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := doc.(map[string]any)["truncated"].(string); !strings.Contains(s, "read 1 of at least 2 chunks") {
		t.Errorf("--axi lacks the truncation note:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-relevant", "--path", big, "--context", "c", "--max-chunks", "1", "--plain")
	if !strings.HasPrefix(out, "TRUNCATED: ") {
		t.Errorf("text lacks the truncation note:\n%s", out)
	}
	// The plain -o summary is a success report too.
	_, out, _ = runCLI(t, "extract-relevant", "--path", big, "--context", "c", "--max-chunks", "1", "-o", filepath.Join(t.TempDir(), "o.txt"), "--plain")
	if !strings.Contains(out, "Truncated: read 1 of at least 2 chunks") {
		t.Errorf("-o summary lacks the truncation note:\n%s", out)
	}
	_, out, _ = runCLI(t, "extract-relevant", "--path", writeDoc(t), "--context", "c", "--json")
	if strings.Contains(out, "truncated") {
		t.Errorf("an uncut input claims truncation:\n%s", out)
	}
	// Under the default --max-chunks the same file is read whole.
	_, out, _ = runCLI(t, "extract-relevant", "--path", big, "--context", "c", "--json")
	if strings.Contains(out, "truncated") {
		t.Errorf("an uncut input claims truncation:\n%s", out)
	}
}

// Defends finding 5 on a partial directory run: a failed file does not hide
// that another file's input was cut, here at --max-chunks 1.
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
	code, out, _ := runCLI(t, "extract-relevant", "--path", dir, "--context", "c", "--max-chunks", "1", "--json")
	var r ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if code != 1 || r.Error == "" || len(r.ExtractedParts) != 1 {
		t.Fatalf("want a partial run, got exit %d: %s", code, out)
	}
	if !strings.Contains(r.Truncated, "big.md: read 1 of at least 2 chunks") {
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
	_, out, _ = runCLI(t, "extract-links", "--url", manyLinksPage(t, 120), "--context", "c", "--plain")
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
