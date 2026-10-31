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
	"sync"
	"testing"
)

// fakeLLM stands in for the OpenAI-compatible endpoint. reply receives the last
// user message and returns the assistant's answer, so a test can make the model
// respond differently per file.
func fakeLLM(t *testing.T, reply func(prompt string) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var last string
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				last = req.Messages[i].Content
				break
			}
		}
		out, err := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": reply(last)}},
			},
		})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pointExtractRelevantAt aims the command at a fake endpoint. The command reads
// its configuration from the environment only, which is the whole of
// pkg/llmapi/config.go — there is no config file, despite what the help text
// used to claim.
func pointExtractRelevantAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_MODEL", "fake-model")
}

func runExtractRelevantCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Cleanup(func() {
		extractRelevantContext = ""
		extractRelevantConcurrency = 2
		extractRelevantOutput = ""
		extractRelevantPath = "."
		extractRelevantJSON = false
		extractRelevantMinimal = false
	})

	cmd := newExtractRelevantCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func decodeExtractResult(t *testing.T, out string) ExtractRelevantResult {
	t.Helper()
	var got ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return got
}

func TestExtractRelevantRequiresAContext(t *testing.T) {
	srv := fakeLLM(t, func(string) string { return "x" })
	pointExtractRelevantAt(t, srv)

	if _, err := runExtractRelevantCmd(t, "--path", t.TempDir()); err == nil {
		t.Error("extract-relevant ran with no --context")
	}
}

// The API key is the one setting with no default, and the failure must arrive
// before any file is read rather than as a confusing HTTP error later.
func TestExtractRelevantRefusesWithNoAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	_, err := runExtractRelevantCmd(t, "--path", t.TempDir(), "--context", "anything")
	if err == nil {
		t.Fatal("extract-relevant ran with no API key")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("error does not mention the API key: %v", err)
	}
}

func TestExtractRelevantOnASingleFile(t *testing.T) {
	srv := fakeLLM(t, func(prompt string) string {
		if !strings.Contains(prompt, "the needle") {
			return "NONE"
		}
		return "found: the needle"
	})
	pointExtractRelevantAt(t, srv)

	file := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(file, []byte("hay hay the needle hay\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runExtractRelevantCmd(t, "--path", file, "--context", "needles", "--json")
	if err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}

	got := decodeExtractResult(t, out)
	if got.Error != "" {
		t.Fatalf("result carries an error: %s", got.Error)
	}
	if got.TotalFiles != 1 || got.ProcessedFiles != 1 {
		t.Errorf("TotalFiles=%d ProcessedFiles=%d, want 1 and 1", got.TotalFiles, got.ProcessedFiles)
	}
	if len(got.ExtractedParts) != 1 || !strings.Contains(got.ExtractedParts[0], "the needle") {
		t.Errorf("ExtractedParts = %v", got.ExtractedParts)
	}
}

func TestExtractRelevantReportsAMissingPath(t *testing.T) {
	srv := fakeLLM(t, func(string) string { return "x" })
	pointExtractRelevantAt(t, srv)

	missing := filepath.Join(t.TempDir(), "nope.md")
	_, err := runExtractRelevantCmd(t, "--path", missing, "--context", "anything")
	if err == nil {
		t.Fatal("extract-relevant succeeded on a path that does not exist")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error does not say the path is missing: %v", err)
	}
}

func TestExtractRelevantOnADirectoryVisitsEveryTextFile(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}

	srv := fakeLLM(t, func(prompt string) string {
		mu.Lock()
		defer mu.Unlock()
		for _, name := range []string{"one.md", "two.md"} {
			if strings.Contains(prompt, name) {
				seen[name] = true
			}
		}
		return "extracted"
	})
	pointExtractRelevantAt(t, srv)

	dir := t.TempDir()
	for _, name := range []string{"one.md", "two.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("body of "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A binary extension and an excluded directory must both be skipped.
	if err := os.WriteFile(filepath.Join(dir, "image.png"), []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "dep.md"), []byte("vendored\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runExtractRelevantCmd(t, "--path", dir, "--context", "anything", "--json")
	if err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}

	got := decodeExtractResult(t, out)
	if got.TotalFiles != 2 {
		t.Errorf("TotalFiles = %d, want 2 (png and node_modules must be skipped)", got.TotalFiles)
	}
	if got.ProcessedFiles != 2 {
		t.Errorf("ProcessedFiles = %d, want 2", got.ProcessedFiles)
	}
	if len(got.ExtractedParts) != 2 {
		t.Errorf("ExtractedParts has %d entries, want 2: %v", len(got.ExtractedParts), got.ExtractedParts)
	}

	mu.Lock()
	defer mu.Unlock()
	if !seen["one.md"] || !seen["two.md"] {
		t.Errorf("the per-file prompt did not name both files: %v", seen)
	}
}

// "NONE" is the model's way of saying a file had nothing relevant. It must be
// counted as processed but must not become an extracted part, or every
// irrelevant file would pad the output.
func TestExtractRelevantDropsFilesTheModelAnswersNONEFor(t *testing.T) {
	srv := fakeLLM(t, func(prompt string) string {
		if strings.Contains(prompt, "keep.md") {
			return "kept content"
		}
		return "NONE"
	})
	pointExtractRelevantAt(t, srv)

	dir := t.TempDir()
	for _, name := range []string{"keep.md", "drop.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runExtractRelevantCmd(t, "--path", dir, "--context", "anything", "--json")
	if err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}

	got := decodeExtractResult(t, out)
	if got.TotalFiles != 2 || got.ProcessedFiles != 2 {
		t.Errorf("TotalFiles=%d ProcessedFiles=%d, want 2 and 2", got.TotalFiles, got.ProcessedFiles)
	}
	if len(got.ExtractedParts) != 1 {
		t.Fatalf("ExtractedParts = %v, want just the kept file", got.ExtractedParts)
	}
	if !strings.Contains(got.ExtractedParts[0], "kept content") {
		t.Errorf("the wrong file survived: %v", got.ExtractedParts)
	}
}

func TestExtractRelevantReportsAnEmptyDirectory(t *testing.T) {
	srv := fakeLLM(t, func(string) string { return "x" })
	pointExtractRelevantAt(t, srv)

	out, err := runExtractRelevantCmd(t, "--path", t.TempDir(), "--context", "anything", "--json")
	if err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}
	got := decodeExtractResult(t, out)
	if got.Error == "" {
		t.Error("an empty directory reported no error")
	}
}

func TestExtractRelevantFetchesAURL(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The junk is inside <p> tags on purpose. htmlToText only selects
		// h1-h6/p/li/pre/code/blockquote/td/th, so bare text in a <nav> would
		// never be picked up and the assertion below would pass even with the
		// chrome-stripping Remove() deleted. Measured: with the junk as bare
		// text, sabotaging that line left this test green.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Docs</title></head><body>`+
			`<nav><p>skip me</p></nav>`+
			`<p>install with brew</p>`+
			`<footer><p>copyright junk</p></footer>`+
			`</body></html>`)
	}))
	defer page.Close()

	var gotPrompt string
	var mu sync.Mutex
	srv := fakeLLM(t, func(prompt string) string {
		mu.Lock()
		gotPrompt = prompt
		mu.Unlock()
		return "brew install"
	})
	pointExtractRelevantAt(t, srv)

	out, err := runExtractRelevantCmd(t, "--path", page.URL, "--context", "installation", "--json")
	if err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}

	got := decodeExtractResult(t, out)
	if got.URL != page.URL {
		t.Errorf("URL = %q, want %q", got.URL, page.URL)
	}
	if len(got.ExtractedParts) != 1 || !strings.Contains(got.ExtractedParts[0], "brew install") {
		t.Errorf("ExtractedParts = %v", got.ExtractedParts)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(gotPrompt, "install with brew") {
		t.Errorf("the page body did not reach the model:\n%s", gotPrompt)
	}
	// nav and footer are stripped before the content is sent, which is the
	// point of converting HTML to text rather than posting the raw markup.
	if strings.Contains(gotPrompt, "skip me") || strings.Contains(gotPrompt, "copyright junk") {
		t.Errorf("HTML chrome reached the model:\n%s", gotPrompt)
	}
}

func TestExtractRelevantReadsANonHTMLURLVerbatim(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"openapi":"3.0.0"}`)
	}))
	defer page.Close()

	var gotPrompt string
	var mu sync.Mutex
	srv := fakeLLM(t, func(prompt string) string {
		mu.Lock()
		gotPrompt = prompt
		mu.Unlock()
		return "3.0.0"
	})
	pointExtractRelevantAt(t, srv)

	if _, err := runExtractRelevantCmd(t, "--path", page.URL, "--context", "version", "--json"); err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(gotPrompt, `"openapi":"3.0.0"`) {
		t.Errorf("non-HTML content was not passed through verbatim:\n%s", gotPrompt)
	}
}

func TestExtractRelevantSurfacesAnHTTPError(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer page.Close()

	srv := fakeLLM(t, func(string) string { return "x" })
	pointExtractRelevantAt(t, srv)

	out, err := runExtractRelevantCmd(t, "--path", page.URL, "--context", "anything", "--json")
	if err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}
	got := decodeExtractResult(t, out)
	if got.Error == "" {
		t.Error("a 404 was reported as a success with no error field")
	}
}

func TestExtractRelevantWritesToAnOutputFile(t *testing.T) {
	srv := fakeLLM(t, func(string) string { return "the extracted text" })
	pointExtractRelevantAt(t, srv)

	dir := t.TempDir()
	src := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.md")

	if _, err := runExtractRelevantCmd(t, "--path", src, "--context", "anything", "-o", dst); err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}

	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("the output file was not written: %v", err)
	}
	if !strings.Contains(string(body), "the extracted text") {
		t.Errorf("the output file does not hold the extraction:\n%s", body)
	}
}

func TestExtractRelevantTextOutputCarriesTheExtraction(t *testing.T) {
	srv := fakeLLM(t, func(string) string { return "the extracted text" })
	pointExtractRelevantAt(t, srv)

	src := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runExtractRelevantCmd(t, "--path", src, "--context", "anything")
	if err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}
	if !strings.Contains(out, "the extracted text") {
		t.Errorf("text output lost the extraction:\n%s", out)
	}
}

func TestExtractRelevantHonoursAXI(t *testing.T) {
	srv := fakeLLM(t, func(string) string { return "the extracted text" })
	pointExtractRelevantAt(t, srv)
	withAXI(t)

	src := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runExtractRelevantCmd(t, "--path", src, "--context", "anything", "--json")
	if err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("--axi was accepted and ignored; JSON was emitted:\n%s", out)
	}
	if !strings.Contains(out, "the extracted text") {
		t.Errorf("AXI output lost the extraction:\n%s", out)
	}
}
