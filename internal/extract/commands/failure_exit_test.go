package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-extract-axi/pkg/llmapi"
)

// These tests defend the rule that nothing reports success when it did not
// succeed: a failed fetch or a failed model call must exit non-zero, whatever
// the output carries. They go through run(), which is what sets the exit code.

// failingLLM stands in for a model endpoint that answers every call with
// status. noRetryDelay keeps a retryable status from costing the client's
// 2s/4s/8s backoff.
func failingLLM(t *testing.T, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"model exploded"}}`))
	}))
	t.Cleanup(srv.Close)
	pointExtractRelevantAt(t, srv)
	noRetryDelay(t)
}

func noRetryDelay(t *testing.T) {
	t.Helper()
	orig := newLLMClient
	t.Cleanup(func() { newLLMClient = orig })
	newLLMClient = func(c *llmapi.APIConfig) *llmapi.LLMClient {
		client := orig(c)
		client.RetryDelay = 0
		return client
	}
}

func noAPIKey(t *testing.T) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
}

func notFoundPage(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func linksPage(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1><a href="/start">Start here</a></h1><p><a href="/more">More</a></p></body></html>`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// Defends against a 404 page printing an error field and exiting 0.
func TestExtractLinksFetchErrorExitsNonZero(t *testing.T) {
	code, out, stderr := runCLI(t, "extract-links", "--url", notFoundPage(t), "--json")
	if code == 0 {
		t.Fatalf("a 404 exited 0:\n%s", out)
	}
	var got ExtractLinksResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !strings.Contains(got.Error, "404") {
		t.Errorf("the JSON does not carry the 404: %+v", got)
	}
	if !strings.Contains(stderr, "404") {
		t.Errorf("stderr does not carry the reason: %q", stderr)
	}
}

// Defends against --context ranking failing on a missing key and the
// heuristic links being passed off as a ranked success.
func TestExtractLinksRankingWithNoKeyExitsNonZeroAndKeepsLinks(t *testing.T) {
	noAPIKey(t)
	code, out, stderr := runCLI(t, "extract-links", "--url", linksPage(t), "--context", "start", "--json")
	if code == 0 {
		t.Fatalf("ranking with no API key exited 0:\n%s", out)
	}
	var got ExtractLinksResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !strings.Contains(got.Error, "LLM ranking failed") || !strings.Contains(got.Error, "API key") {
		t.Errorf("the JSON does not say ranking failed for want of a key: %q", got.Error)
	}
	if got.Total != 2 || len(got.Links) != 2 {
		t.Errorf("the heuristic links were dropped: %+v", got)
	}
	if !strings.Contains(stderr, "LLM ranking failed") {
		t.Errorf("stderr does not carry the reason: %q", stderr)
	}
}

// Defends against a model 500 during ranking exiting 0.
func TestExtractLinksRankingAPIErrorExitsNonZero(t *testing.T) {
	failingLLM(t, http.StatusInternalServerError)
	code, out, _ := runCLI(t, "extract-links", "--url", linksPage(t), "--context", "start", "--json")
	if code == 0 {
		t.Fatalf("a model 500 exited 0:\n%s", out)
	}
	var got ExtractLinksResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !strings.Contains(got.Error, "500") || got.Total != 2 {
		t.Errorf("want the 500 and the two heuristic links, got %+v", got)
	}
}

// Defends against text mode hiding the heuristic links behind the error line,
// so the partial result is visible and labelled as partial.
func TestExtractLinksRankingFailureTextShowsErrorAndLinks(t *testing.T) {
	noAPIKey(t)
	code, out, _ := runCLI(t, "extract-links", "--url", linksPage(t), "--context", "start")
	if code == 0 {
		t.Fatalf("ranking with no API key exited 0:\n%s", out)
	}
	if !strings.HasPrefix(out, "ERROR: LLM ranking failed") {
		t.Errorf("text output does not lead with the error:\n%s", out)
	}
	if !strings.Contains(out, "/start") || !strings.Contains(out, "/more") {
		t.Errorf("text output dropped the heuristic links:\n%s", out)
	}
}

// Defends against a missing key exiting 0 (already true; kept so the four
// failure paths are tested side by side at the exit-code level).
func TestExtractRelevantNoKeyExitsNonZero(t *testing.T) {
	noAPIKey(t)
	code, _, stderr := runCLI(t, "extract-relevant", "--path", t.TempDir(), "--context", "x")
	if code == 0 || !strings.Contains(stderr, "API key") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

// Defends against a model 500 on a single file printing ERROR and exiting 0.
func TestExtractRelevantAPIErrorExitsNonZero(t *testing.T) {
	failingLLM(t, http.StatusInternalServerError)
	src := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCLI(t, "extract-relevant", "--path", src, "--context", "x", "--json")
	if code == 0 {
		t.Fatalf("a model 500 exited 0:\n%s", out)
	}
	got := decodeExtractResult(t, out)
	if !strings.Contains(got.Error, "500") {
		t.Errorf("the JSON does not carry the 500: %+v", got)
	}
	if !strings.Contains(stderr, "500") {
		t.Errorf("stderr does not carry the reason: %q", stderr)
	}
}

// Defends against a 404 URL printing an error field and exiting 0.
func TestExtractRelevantFetchErrorExitsNonZero(t *testing.T) {
	srv := fakeLLM(t, func(string) string { return "x" })
	pointExtractRelevantAt(t, srv)
	code, out, _ := runCLI(t, "extract-relevant", "--path", notFoundPage(t), "--context", "x", "--json")
	if code == 0 {
		t.Fatalf("a 404 exited 0:\n%s", out)
	}
	if got := decodeExtractResult(t, out); !strings.Contains(got.Error, "404") {
		t.Errorf("the JSON does not carry the 404: %+v", got)
	}
}

// Defends against a directory run silently dropping files whose model call
// failed: before this, a run where every call failed printed "No relevant
// content found." and exited 0.
func TestExtractRelevantDirectoryWithAFailedFileExitsNonZeroAndKeepsTheRest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		if strings.Contains(body.String(), "bad.md") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"good part"}}]}`))
	}))
	t.Cleanup(srv.Close)
	pointExtractRelevantAt(t, srv)
	noRetryDelay(t)

	dir := t.TempDir()
	for _, name := range []string{"good.md", "bad.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ := runCLI(t, "extract-relevant", "--path", dir, "--context", "x", "--json")
	if code == 0 {
		t.Fatalf("a directory with a failed model call exited 0:\n%s", out)
	}
	got := decodeExtractResult(t, out)
	if !strings.Contains(got.Error, "1 of 2 files failed") || !strings.Contains(got.Error, "bad.md") {
		t.Errorf("the error does not say which file failed: %q", got.Error)
	}
	if got.ProcessedFiles != 1 || len(got.ExtractedParts) != 1 || !strings.Contains(got.ExtractedParts[0], "good part") {
		t.Errorf("the good file's extraction was not kept: %+v", got)
	}
}

// Defends against -o writing the error into the file and exiting 0.
func TestExtractRelevantOutputFileFailureExitsNonZero(t *testing.T) {
	failingLLM(t, http.StatusInternalServerError)
	dir := t.TempDir()
	src := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.md")
	code, _, _ := runCLI(t, "extract-relevant", "--path", src, "--context", "x", "-o", dst)
	if code == 0 {
		t.Fatal("a model 500 with -o exited 0")
	}
	body, err := os.ReadFile(dst)
	if err != nil || !strings.HasPrefix(string(body), "ERROR:") {
		t.Errorf("the output file does not carry the error: %q (%v)", body, err)
	}
}

// Defends the partial result's size: rankLinksWithLLM sends at most 100 links
// to the model, so returning its slice on failure dropped every link past the
// 100th and reported the smaller total as the page's.
func TestExtractLinksRankingFailureKeepsEveryHeuristicLink(t *testing.T) {
	failingLLM(t, http.StatusUnauthorized)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString("<html><body>")
		for i := 0; i < 150; i++ {
			fmt.Fprintf(&b, `<p><a href="/p%d">Page %d</a></p>`, i, i)
		}
		b.WriteString("</body></html>")
		_, _ = w.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)

	code, out, _ := runCLI(t, "extract-links", "--url", srv.URL, "--context", "x", "--json")
	if code == 0 {
		t.Fatalf("a model 401 exited 0")
	}
	var got ExtractLinksResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if got.Total != 150 || len(got.Links) != 150 {
		t.Errorf("want all 150 heuristic links, got total=%d len=%d", got.Total, len(got.Links))
	}
}

// partialDirectory sets up good.md, whose model call succeeds, and bad.md,
// whose call fails with a 500.
func partialDirectory(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		if strings.Contains(body.String(), "bad.md") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"good part"}}]}`))
	}))
	t.Cleanup(srv.Close)
	pointExtractRelevantAt(t, srv)
	noRetryDelay(t)

	dir := t.TempDir()
	for _, name := range []string{"good.md", "bad.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Defends the partial result in text mode: a directory run with one failed
// file printed only the ERROR line and dropped the good file's extraction,
// which --json still carried.
func TestExtractRelevantPartialDirectoryTextKeepsTheRest(t *testing.T) {
	dir := partialDirectory(t)
	code, out, _ := runCLI(t, "extract-relevant", "--path", dir, "--context", "x")
	if code == 0 {
		t.Fatalf("a directory with a failed model call exited 0:\n%s", out)
	}
	if !strings.HasPrefix(out, "ERROR: 1 of 2 files failed") {
		t.Errorf("text output does not lead with the error:\n%s", out)
	}
	if !strings.Contains(out, "good part") {
		t.Errorf("text output dropped the good file's extraction:\n%s", out)
	}
}

// Defends the same partial result when written with -o in plain text.
func TestExtractRelevantPartialDirectoryOutputFileKeepsTheRest(t *testing.T) {
	dir := partialDirectory(t)
	dst := filepath.Join(t.TempDir(), "out.txt")
	code, _, _ := runCLI(t, "extract-relevant", "--path", dir, "--context", "x", "-o", dst)
	if code == 0 {
		t.Fatal("a directory with a failed model call exited 0")
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "ERROR: 1 of 2 files failed") || !strings.Contains(string(body), "good part") {
		t.Errorf("the output file does not carry both the error and the good extraction:\n%s", body)
	}
}

// Defends the text output of a fetch failure: with no links there is nothing
// partial to show, so the error line is the whole output, with no empty
// "Total Links: 0" block after it.
func TestExtractLinksFetchErrorTextIsOnlyTheError(t *testing.T) {
	code, out, _ := runCLI(t, "extract-links", "--url", notFoundPage(t))
	if code == 0 {
		t.Fatalf("a 404 exited 0:\n%s", out)
	}
	if !strings.HasPrefix(out, "ERROR:") || strings.Contains(out, "Total Links") {
		t.Errorf("want only the error line, got:\n%s", out)
	}
}
