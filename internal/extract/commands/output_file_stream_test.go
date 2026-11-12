package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	goaxi "github.com/samestrin/go-axi"
)

// These tests defend the stdout stream contract of `extract-relevant -o FILE`
// in each output mode. Under --axi and --json stdout is one structured
// document and nothing else; the plain-text summary belongs to plain mode
// only. Release gate F2: the three prose summary lines used to print under
// --axi too, and run() then appended the TOON error, so a caller decoding the
// single stdout stream got prose followed by an object and could read neither.

// outputFileSummaryLines are the plain-mode -o summary. They are prose, so
// none of them may reach stdout in a structured mode.
var outputFileSummaryLines = []string{
	"Extracted content written to:",
	"Files processed:",
	"Processing time:",
}

// extractingFile writes a one-file source and returns its path plus the path
// of the -o target, with a fake model that returns passage.
func extractingFile(t *testing.T, passage string) (src, dst string) {
	t.Helper()
	srv := fakeLLM(t, func(string) string { return passage })
	pointExtractRelevantAt(t, srv)
	dir := t.TempDir()
	src = filepath.Join(dir, "doc.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src, filepath.Join(dir, "out.md")
}

// noProseSummary fails the test if any plain-mode summary line reached stdout.
func noProseSummary(t *testing.T, stdout string) {
	t.Helper()
	for _, line := range outputFileSummaryLines {
		if strings.Contains(stdout, line) {
			t.Errorf("structured stdout carries the plain summary line %q:\n%s", line, stdout)
		}
	}
}

// Defends the other failure at the same -o boundary: the file cannot be
// written. Under --axi stdout is still one TOON error document with no prose in
// front of it, and the exit code says the run failed.
func TestAXIExtractRelevantOutputFileWriteFailureIsOneTOONDocument(t *testing.T) {
	src, _ := extractingFile(t, "the extracted passage")
	dst := filepath.Join(t.TempDir(), "no-such-dir", "out.md")
	code, stdout, stderr := runCLI(t, "extract-relevant", "--path", src, "--context", "c", "-o", dst, "--axi")
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr is not empty under --axi: %q", stderr)
	}
	noProseSummary(t, stdout)
	doc := axiError(t, stdout)
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "failed to write output") {
		t.Errorf("message %q does not name the write failure", msg)
	}
	if n := strings.Count(stdout, "error: true"); n != 1 {
		t.Errorf("stdout holds %d error documents, want 1:\n%s", n, stdout)
	}
}

// Defends the successful -o run under --axi: stdout is one TOON document that
// still reports the total and a next step, while the passages stay in the file
// (-o exists to keep content out of the caller's context). Dropping the prose
// without printing a document would leave a silent success, which reports no
// total and no empty state.
func TestAXIExtractRelevantOutputFileSuccessIsOneTOONDocument(t *testing.T) {
	src, dst := extractingFile(t, "the extracted passage")
	code, stdout, stderr := runCLI(t, "extract-relevant", "--path", src, "--context", "c", "-o", dst, "--axi")
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr is not empty under --axi: %q", stderr)
	}
	noProseSummary(t, stdout)
	v, err := goaxi.Decode(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("stdout is not one TOON document: %v\n%s", err, stdout)
	}
	doc, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("stdout is not a TOON object: %#v", v)
	}
	if got := fmt.Sprint(doc["total"]); got != "1" {
		t.Errorf("total is %v, want 1 (a successful run always reports a total):\n%s", doc["total"], stdout)
	}
	if help, _ := doc["help"].([]any); len(help) == 0 {
		t.Errorf("no help[] on a successful -o run:\n%s", stdout)
	}
	if strings.Contains(stdout, "the extracted passage") {
		t.Errorf("-o put the extraction back on stdout, which is what -o avoids:\n%s", stdout)
	}
	written, err := os.ReadFile(dst)
	if err != nil || !strings.Contains(string(written), "the extracted passage") {
		t.Errorf("the output file lost the extraction: %v %q", err, written)
	}
}

// Defends the empty -o success under --axi: it is still one TOON document, and
// it says outright that nothing was found rather than going silent.
func TestAXIExtractRelevantOutputFileEmptyResultIsExplicit(t *testing.T) {
	src, dst := extractingFile(t, "NONE")
	code, stdout, stderr := runCLI(t, "extract-relevant", "--path", src, "--context", "c", "-o", dst, "--axi")
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr is not empty under --axi: %q", stderr)
	}
	noProseSummary(t, stdout)
	v, err := goaxi.Decode(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("stdout is not one TOON document: %v\n%s", err, stdout)
	}
	doc, _ := v.(map[string]any)
	if got := fmt.Sprint(doc["total"]); got != "0" {
		t.Errorf("total is %v, want 0:\n%s", doc["total"], stdout)
	}
	if msg, _ := doc["message"].(string); msg != noRelevantMessage {
		t.Errorf("message is %q, want %q:\n%s", msg, noRelevantMessage, stdout)
	}
}

// Defends the -o run under --json: stdout is one JSON document, not the prose
// summary, and the extraction is in the file. A caller parsing stdout as JSON
// got a parse error before the fix.
func TestJSONExtractRelevantOutputFileStdoutIsOneJSONDocument(t *testing.T) {
	src, dst := extractingFile(t, "the extracted passage")
	code, stdout, stderr := runCLI(t, "extract-relevant", "--path", src, "--context", "c", "-o", dst, "--json")
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr is not empty on a successful --json run: %q", stderr)
	}
	noProseSummary(t, stdout)
	var doc ExtractRelevantResult
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if doc.Total == nil || *doc.Total != 1 {
		t.Errorf("total is %v, want 1:\n%s", doc.Total, stdout)
	}
	if len(doc.ExtractedParts) != 0 {
		t.Errorf("-o put the extraction back on stdout: %+v", doc.ExtractedParts)
	}
	if len(doc.Help) != 0 {
		t.Errorf("--json carries help[], which is --axi only: %v", doc.Help)
	}
	written, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("the output file was not written: %v", err)
	}
	var inFile ExtractRelevantResult
	if err := json.Unmarshal(written, &inFile); err != nil {
		t.Fatalf("the output file is not JSON: %v\n%s", err, written)
	}
	if len(inFile.ExtractedParts) != 1 || !strings.Contains(inFile.ExtractedParts[0], "the extracted passage") {
		t.Errorf("the output file lost the extraction: %s", written)
	}
}

// Defends the other half of the contract: a successful plain run with -o still
// prints the summary, naming the file, the file counts and the elapsed time.
// Suppressing it everywhere would take away the only report a plain run gives.
func TestPlainExtractRelevantOutputFileStillPrintsSummary(t *testing.T) {
	src, dst := extractingFile(t, "the extracted passage")
	code, stdout, stderr := runCLI(t, "extract-relevant", "--path", src, "--context", "c", "-o", dst)
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr is not empty on a successful plain run: %q", stderr)
	}
	for _, want := range []string{
		"Extracted content written to: " + dst,
		"Files processed: 1/1",
		"Processing time: ",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the plain summary lost %q:\n%s", want, stdout)
		}
	}
	written, err := os.ReadFile(dst)
	if err != nil || !strings.Contains(string(written), "the extracted passage") {
		t.Errorf("the output file lost the extraction: %v %q", err, written)
	}
}
