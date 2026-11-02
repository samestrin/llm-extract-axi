package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// emptyPage serves HTML with no links at all.
func emptyPage(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>nothing here</p></body></html>`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func writeDoc(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

// Defends against a model run reporting neither which model ran nor how many
// bytes were sent to it.
func TestExtractRelevantJSONCarriesModelAndBytesSent(t *testing.T) {
	pointExtractRelevantAt(t, fakeLLM(t, func(string) string { return "kept" }))
	t.Setenv("OPENAI_MODEL", "test-model-x")
	code, out, _ := runCLI(t, "extract-relevant", "--path", writeDoc(t), "--context", "c", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var got ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "test-model-x" || got.BytesSent == nil || *got.BytesSent < int64(len("body\n")) {
		t.Errorf("want model and a byte size, got %q %v", got.Model, got.BytesSent)
	}
	if got.Total == nil || *got.Total != 1 || got.Message != "" {
		t.Errorf("want total 1 and no message, got %v %q", got.Total, got.Message)
	}
}

// Defends against the minimal and AXI forms dropping the model and byte size.
func TestExtractRelevantMinAndAXICarryModelAndBytesSent(t *testing.T) {
	pointExtractRelevantAt(t, fakeLLM(t, func(string) string { return "kept" }))
	t.Setenv("OPENAI_MODEL", "test-model-x")
	_, out, _ := runCLI(t, "extract-relevant", "--path", writeDoc(t), "--context", "c", "--json", "--min")
	var got ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.M != "test-model-x" || got.BS == nil || *got.BS == 0 || got.N == nil {
		t.Errorf("minimal output lost model/bytes/total: %s", out)
	}

	withAXI(t)
	_, out, _ = runCLI(t, "extract-relevant", "--path", writeDoc(t), "--context", "c", "--json")
	if !strings.Contains(out, "bytes_sent") || !strings.Contains(out, "test-model-x") {
		t.Errorf("AXI output lost model/bytes_sent:\n%s", out)
	}
}

// Defends against a successful run that found nothing looking like a failed
// one: it must exit 0, carry total 0 and say so, in every mode.
func TestExtractRelevantZeroResultIsExplicit(t *testing.T) {
	pointExtractRelevantAt(t, fakeLLM(t, func(string) string { return "NONE" }))
	src := writeDoc(t)

	code, out, _ := runCLI(t, "extract-relevant", "--path", src, "--context", "c", "--json")
	var got ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if code != 0 || got.Error != "" || got.Total == nil || *got.Total != 0 || got.Message != noRelevantMessage || len(got.ExtractedParts) != 0 {
		t.Errorf("zero result not explicit (exit %d): %s", code, out)
	}

	_, out, _ = runCLI(t, "extract-relevant", "--path", src, "--context", "c", "--json", "--min")
	if !strings.Contains(out, `"n":0`) || !strings.Contains(out, noRelevantMessage) {
		t.Errorf("minimal zero result not explicit: %s", out)
	}

	_, out, _ = runCLI(t, "extract-relevant", "--path", src, "--context", "c")
	if !strings.Contains(out, noRelevantMessage) {
		t.Errorf("text zero result not explicit: %s", out)
	}

	withAXI(t)
	_, out, _ = runCLI(t, "extract-relevant", "--path", src, "--context", "c", "--json")
	if !strings.Contains(out, noRelevantMessage) || !strings.Contains(out, "total") {
		t.Errorf("AXI zero result not explicit: %s", out)
	}
}

// Defends against a failed run being given a total, which would make it look
// like an empty success.
func TestExtractRelevantFailureHasNoTotalOrMessage(t *testing.T) {
	failingLLM(t, http.StatusInternalServerError)
	code, out, _ := runCLI(t, "extract-relevant", "--path", writeDoc(t), "--context", "c", "--json")
	var got ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if code == 0 || got.Error == "" || got.Total != nil || got.Message != "" {
		t.Errorf("failure should carry error and no total (exit %d): %s", code, out)
	}
}

// Defends against LLM-ranked links omitting the model and byte size, and
// against heuristic runs claiming a model they never called.
func TestExtractLinksModelAndBytesSentOnlyWhenModelRan(t *testing.T) {
	pointExtractRelevantAt(t, fakeLLM(t, func(string) string {
		return `{"rankings":[{"index":0,"score":90},{"index":1,"score":10}]}`
	}))
	t.Setenv("OPENAI_MODEL", "test-model-x")
	page := linksPage(t)

	_, out, _ := runCLI(t, "extract-links", "--url", page, "--context", "start", "--json")
	var got ExtractLinksResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "test-model-x" || got.BytesSent == nil || *got.BytesSent == 0 {
		t.Errorf("ranked run lacks model/bytes_sent: %s", out)
	}

	_, out, _ = runCLI(t, "extract-links", "--url", page, "--json")
	if strings.Contains(out, "model") || strings.Contains(out, "bytes_sent") {
		t.Errorf("heuristic run claims a model: %s", out)
	}

	_, out, _ = runCLI(t, "extract-links", "--url", page, "--context", "start", "--json", "--min")
	if !strings.Contains(out, `"m":"test-model-x"`) || !strings.Contains(out, `"bs":`) {
		t.Errorf("minimal ranked run lacks m/bs: %s", out)
	}

	withAXI(t)
	_, out, _ = runCLI(t, "extract-links", "--url", page, "--context", "start", "--json")
	if !strings.Contains(out, "bytes_sent") || !strings.Contains(out, "test-model-x") {
		t.Errorf("AXI ranked run lacks model/bytes_sent: %s", out)
	}
}

// Defends against a page with no links printing the same as a failed fetch:
// total 0 and a message, exit 0, in every mode.
func TestExtractLinksZeroResultIsExplicit(t *testing.T) {
	page := emptyPage(t)

	code, out, _ := runCLI(t, "extract-links", "--url", page, "--json")
	var got ExtractLinksResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if code != 0 || got.Error != "" || got.Total == nil || *got.Total != 0 || got.Message != noLinksMessage {
		t.Errorf("zero links not explicit (exit %d): %s", code, out)
	}

	_, out, _ = runCLI(t, "extract-links", "--url", page, "--json", "--min")
	if !strings.Contains(out, `"n":0`) || !strings.Contains(out, noLinksMessage) {
		t.Errorf("minimal zero result not explicit: %s", out)
	}

	_, out, _ = runCLI(t, "extract-links", "--url", page)
	if !strings.Contains(out, "Total Links: 0") || !strings.Contains(out, noLinksMessage) {
		t.Errorf("text zero result not explicit: %s", out)
	}

	withAXI(t)
	_, out, _ = runCLI(t, "extract-links", "--url", page, "--json")
	if !strings.Contains(out, noLinksMessage) || !strings.Contains(out, "total") {
		t.Errorf("AXI zero result not explicit: %s", out)
	}
}

// Defends against a failed fetch carrying a total.
func TestExtractLinksFailureHasNoTotal(t *testing.T) {
	code, out, _ := runCLI(t, "extract-links", "--url", notFoundPage(t), "--json")
	var got ExtractLinksResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if code == 0 || got.Error == "" || got.Total != nil || got.Message != "" {
		t.Errorf("failed fetch should have error and no total (exit %d): %s", code, out)
	}
}
