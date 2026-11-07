package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samestrin/llm-extract-axi/pkg/llmapi"
	"github.com/samestrin/llm-extract-axi/pkg/output"
)

// TestOutputExtractResultReportsFormatterErrorForFile defends EST-5240 row 1:
// with `-o` and `--json` the formatter error was discarded (`_ = formatter.Print`)
// and the output file was still written, so a caller saw a success-shaped run
// that produced an empty file. A value the JSON encoder refuses (NaN seconds)
// must now return the error and leave no file on disk.
func TestOutputExtractResultReportsFormatterErrorForFile(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.json")

	t.Cleanup(func() {
		extractRelevantOutput = ""
		extractRelevantJSON = false
		extractRelevantMinimal = false
		output.SetDefaultAXI(false)
	})
	output.SetDefaultAXI(false)

	// Build the command first: newExtractRelevantCmd re-registers the flags with
	// StringVar/BoolVar, which resets the package globals to their defaults, so
	// the globals must be set only after it returns (root.go notes this).
	cmd := newExtractRelevantCmd()
	cmd.SetOut(io.Discard)
	extractRelevantOutput = dst
	extractRelevantJSON = true
	extractRelevantMinimal = false

	err := outputExtractResult(cmd, ExtractRelevantResult{
		Path:            filepath.Join(dir, "src.md"),
		Context:         "anything",
		ProcessingTimeS: math.NaN(),
	})
	if err == nil {
		t.Fatal("outputExtractResult returned success even though the formatter failed")
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Errorf("the output file was written despite the format failure: %s", dst)
	}
}

// TestExtractRelevantContentHonoursContextCancellation defends EST-5240 row 2:
// processDirectory built a whole-run context but extractRelevantContent ignored
// it and minted its own from context.Background() via CompleteWithSystem, so a
// cancelled or expired run never reached the model call. It must accept the
// caller's context and honour its cancellation.
//
// The proof: with a pre-cancelled run context, the call must return a
// context-cancellation error instead of reaching the model. The old code built
// a fresh background context, so the same server would answer "ignored" and the
// call would report success -- a whole-run cancel would be silently ignored.
func TestExtractRelevantContentHonoursContextCancellation(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		_, _ = io.WriteString(w, "ignored")
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_MODEL", "fake-model")
	client := llmapi.NewLLMClientFromConfig(llmapi.GetAPIConfig())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel the run before the call: nothing should reach the model

	// A hung call would mean the cancellation was ignored; bound the wait so a
	// regression fails fast instead of parking the whole suite.
	done := make(chan error, 1)
	go func() {
		_, err := extractRelevantContent(ctx, client, "body content", "anything", 30)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("extractRelevantContent succeeded despite a cancelled run context")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("expected a cancellation error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("extractRelevantContent ignored the cancelled context and hung past its budget")
	}
}

// TestExtractRelevantWritesJSONToOutputFile defends the happy path the row-1
// fix must not break: with `-o` and `--json` on a successful run, the JSON
// output must still reach the file. The existing `-o` test only covers text
// output, so the JSON write branch would otherwise go untested.
func TestExtractRelevantWritesJSONToOutputFile(t *testing.T) {
	srv := fakeLLM(t, func(string) string { return "the extracted text" })
	pointExtractRelevantAt(t, srv)

	dir := t.TempDir()
	src := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.json")

	if _, err := runExtractRelevantCmd(t, "--path", src, "--context", "anything", "--json", "-o", dst); err != nil {
		t.Fatalf("extract-relevant: %v", err)
	}

	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("the JSON output file was not written: %v", err)
	}
	var got ExtractRelevantResult
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("the output file is not valid JSON: %v\n%s", err, body)
	}
	if len(got.ExtractedParts) != 1 || !strings.Contains(got.ExtractedParts[0], "the extracted text") {
		t.Errorf("the output file lost the extraction: %s", body)
	}
}

// TestExtractRelevantDirectoryAbortsOnCommandCancel proves the whole-run context
// reaches the model calls: cancelling the command's context before a directory
// run must fail it with a cancellation error, not quietly complete every file.
// Before the fix extractRelevantContent built its own background context, so a
// top-level cancel was ignored (EST-5240 row 2).
func TestExtractRelevantDirectoryAbortsOnCommandCancel(t *testing.T) {
	srv := fakeLLM(t, func(string) string { return "should not be reached" })
	pointExtractRelevantAt(t, srv)

	dir := t.TempDir()
	for _, name := range []string{"one.md", "two.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("body of "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() {
		extractRelevantContext = ""
		extractRelevantConcurrency = 2
		extractRelevantOutput = ""
		extractRelevantPath = "."
		extractRelevantJSON = false
		extractRelevantMinimal = false
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the run: the whole directory run is dead on arrival

	cmd := newExtractRelevantCmd()
	cmd.SetContext(ctx)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"--path", dir, "--context", "anything", "--json"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a directory run completed despite a cancelled command context")
	}
	if !strings.Contains(buf.String(), "failed") {
		t.Errorf("the JSON output does not report the failed files:\n%s", buf.String())
	}
}
