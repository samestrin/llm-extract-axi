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

	_, out, _ = runCLI(t, "extract-links", "--url", page, "--plain")
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

// Defends against text mode dropping the model and byte count that --json
// carries.
func TestExtractLinksTextShowsModelAndBytesSent(t *testing.T) {
	pointExtractRelevantAt(t, fakeLLM(t, func(string) string {
		return `{"rankings":[{"index":0,"score":90}]}`
	}))
	t.Setenv("OPENAI_MODEL", "test-model-x")
	_, out, _ := runCLI(t, "extract-links", "--url", linksPage(t), "--context", "start", "--plain")
	if !strings.Contains(out, "Model: test-model-x | Bytes sent: ") {
		t.Errorf("text output lost model and bytes sent:\n%s", out)
	}
}

// Defends against a URL run whose model answers NONE (or nothing) reporting
// the sentinel as an extracted part and a total of 1.
func TestExtractRelevantURLZeroResultIsExplicit(t *testing.T) {
	for _, reply := range []string{"NONE", "", "  NONE\n"} {
		pointExtractRelevantAt(t, fakeLLM(t, func(string) string { return reply }))
		code, out, _ := runCLI(t, "extract-relevant", "--path", emptyPage(t), "--context", "c", "--json")
		var got ExtractRelevantResult
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if code != 0 || len(got.ExtractedParts) != 0 || got.Total == nil || *got.Total != 0 || got.Message != noRelevantMessage {
			t.Errorf("reply %q: zero result not explicit (exit %d): %s", reply, code, out)
		}
	}
}

// Defends against a run that never reached a model claiming one with
// bytes_sent 0.
func TestExtractRelevantNoModelCallReportsNoModel(t *testing.T) {
	pointExtractRelevantAt(t, fakeLLM(t, func(string) string { return "x" }))
	code, out, _ := runCLI(t, "extract-relevant", "--path", notFoundPage(t), "--context", "c", "--json")
	if code == 0 || strings.Contains(out, `"model"`) || strings.Contains(out, "bytes_sent") {
		t.Errorf("no model call was made, yet (exit %d): %s", code, out)
	}
}

// Defends epic 15 T1: docs/output.md used to say `--min` was "independent of
// the encoding", but --min only abbreviates the structured output (TOON and
// --json); under --plain it has no effect. It also defends the failure path:
// --plain --min printed a failed extract-links with no ERROR line, so a caller
// saw a URL and an exit 1 with no reason. The probes run both ways first, so
// the doc cannot pass while the code disagrees with it.
func TestDocsMatchMinUnderPlain(t *testing.T) {
	withAXI(t)
	page := linksPage(t)

	// --plain is the same text with and without --min.
	_, plain, _ := runCLI(t, "extract-links", "--url", page, "--plain")
	_, plainMin, _ := runCLI(t, "extract-links", "--url", page, "--plain", "--min")
	if plain != plainMin {
		t.Fatalf("--plain --min is not the same text as --plain; the probe, not the docs, is wrong:\n--plain:\n%s\n--plain --min:\n%s", plain, plainMin)
	}

	// A failure carries its reason under --plain, with or without --min.
	bad := notFoundPage(t)
	code, plainErr, _ := runCLI(t, "extract-links", "--url", bad, "--plain")
	codeMin, plainErrMin, _ := runCLI(t, "extract-links", "--url", bad, "--plain", "--min")
	if code != 1 || codeMin != 1 {
		t.Fatalf("a failed fetch exited %d and %d under --plain, want 1 twice", code, codeMin)
	}
	if !strings.Contains(plainErr, "ERROR: ") || !strings.Contains(plainErrMin, "ERROR: ") {
		t.Fatalf("--plain --min dropped the failure reason; the probe, not the docs, is wrong:\n--plain:\n%s\n--plain --min:\n%s", plainErr, plainErrMin)
	}
	if plainErr != plainErrMin {
		t.Fatalf("--plain --min differs from --plain on a failure; the probe, not the docs, is wrong: plain=%q min=%q", plainErr, plainErrMin)
	}

	// Where --min does act: --json goes one line with abbreviated keys, and
	// TOON abbreviates its keys.
	_, jsonFull, _ := runCLI(t, "extract-links", "--url", page, "--json")
	_, jsonMin, _ := runCLI(t, "extract-links", "--url", page, "--json", "--min")
	if jsonFull == jsonMin || strings.Contains(strings.TrimSpace(jsonMin), "\n") || !strings.Contains(jsonMin, `"l":[`) {
		t.Fatalf("--json --min is not one line of abbreviated JSON; the probe, not the docs, is wrong:\n%s", jsonMin)
	}
	_, toonFull, _ := runCLI(t, "extract-links", "--url", page)
	_, toonMin, _ := runCLI(t, "extract-links", "--url", page, "--min")
	if toonFull == toonMin || !strings.Contains(toonMin, "l[") || !strings.Contains(toonFull, "links[") {
		t.Fatalf("--min did not abbreviate the TOON keys; the probe, not the docs, is wrong:\n%s", toonMin)
	}

	var bullet string
	for _, line := range strings.Split(readRepoFile(t, "docs/output.md"), "\n") {
		if strings.HasPrefix(line, "- `--min`:") {
			bullet = line
		}
	}
	if bullet == "" {
		t.Fatal("docs/output.md has no `--min` bullet")
	}
	for _, want := range []string{"TOON", "--json", "no effect on `--plain`"} {
		if !strings.Contains(bullet, want) {
			t.Errorf("the docs/output.md --min bullet does not name %q: %s", want, bullet)
		}
	}
	if strings.Contains(readRepoFile(t, "docs/output.md"), "independent of the encoding") {
		t.Error("docs/output.md still claims --min is independent of the encoding, but it has no effect on --plain")
	}
}
