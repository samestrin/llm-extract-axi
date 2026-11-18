package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeCLI writes a script that echoes its arguments (or fails), standing in
// for llm-extract so the argument mapping can be asserted.
func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "llm-extract")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func text(t *testing.T, raw any) string {
	t.Helper()
	b, _ := json.Marshal(raw)
	return string(b)
}

// Defends the argument mapping: a tool argument that silently stops reaching
// the CLI would change what is fetched without any error.
func TestBuildMapsArguments(t *testing.T) {
	cases := []struct {
		tool string
		in   string
		want []string
	}{
		{"fetch", `{"url":"http://x","timeout":5,"retries":0,"user_agent":"ua"}`,
			[]string{"fetch", "http://x", "--timeout", "5", "--retries", "0", "--user-agent", "ua"}},
		{"extract_links", `{"url":"http://x","context":"c","timeout":3}`,
			[]string{"extract-links", "--url", "http://x", "--context", "c", "--timeout", "3"}},
		{"extract_relevant", `{"path":"p","context":"c","concurrency":4}`,
			[]string{"extract-relevant", "--path", "p", "--context", "c", "--concurrency", "4"}},
	}
	for _, c := range cases {
		var a args
		if err := json.Unmarshal([]byte(c.in), &a); err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools {
			if tl.name != c.tool {
				continue
			}
			got, err := tl.build(a)
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s: got %v, %v; want %v", c.tool, got, err, c.want)
			}
		}
	}
}

// Defends against running the CLI with a missing required argument.
func TestMissingRequiredArgumentIsError(t *testing.T) {
	r := Run(context.Background(), "/nonexistent", "extract_relevant", json.RawMessage(`{"path":"p"}`))
	if !r.IsError || !strings.Contains(text(t, r.Content), "context") {
		t.Errorf("want error naming context, got %+v", r)
	}
}

// Defends the unknown-tool and bad-JSON paths from reporting success.
func TestUnknownToolAndBadJSON(t *testing.T) {
	if r := Run(context.Background(), "x", "nope", nil); !r.IsError {
		t.Error("unknown tool was not an error")
	}
	if r := Run(context.Background(), "x", "fetch", json.RawMessage(`{`)); !r.IsError {
		t.Error("bad JSON was not an error")
	}
}

// Defends the core rule: a non-zero CLI exit must surface as an error carrying
// stderr, never as an empty successful result.
func TestNonZeroExitIsError(t *testing.T) {
	bin := fakeCLI(t, "echo boom >&2; exit 1")
	r := Run(context.Background(), bin, "fetch", json.RawMessage(`{"url":"http://x"}`))
	if !r.IsError || !strings.Contains(text(t, r.Content), "boom") {
		t.Errorf("want error with stderr, got %+v", r)
	}
}

// Defends that stdout is returned verbatim on success.
func TestSuccessReturnsStdout(t *testing.T) {
	bin := fakeCLI(t, `echo "$@"`)
	r := Run(context.Background(), bin, "fetch", json.RawMessage(`{"url":"http://x"}`))
	if r.IsError || !strings.Contains(text(t, r.Content), "fetch http://x") {
		t.Errorf("unexpected result %+v", r)
	}
}

// Defends binary discovery: an override pointing nowhere must fail loudly
// rather than fall back to some other binary.
func TestFindBinary(t *testing.T) {
	t.Setenv(BinaryEnv, filepath.Join(t.TempDir(), "missing"))
	if _, err := FindBinary(); err == nil {
		t.Error("missing override was accepted")
	}
	bin := fakeCLI(t, "true")
	t.Setenv(BinaryEnv, bin)
	if got, err := FindBinary(); err != nil || got != bin {
		t.Errorf("got %q, %v", got, err)
	}
	t.Setenv(BinaryEnv, "")
	t.Setenv("PATH", t.TempDir())
	if _, err := FindBinary(); err == nil {
		t.Error("expected not-found error")
	}
}

func TestNewRegistersEveryTool(t *testing.T) {
	if New("x", "v") == nil || len(ToolNames()) != 3 {
		t.Error("server or tools missing")
	}
}

// Defends the documented discovery order: a binary beside the server wins, and
// PATH is the last resort. Removing either branch must turn this red.
func TestFindBinarySiblingAndPath(t *testing.T) {
	t.Setenv(BinaryEnv, "")
	dir := t.TempDir()
	name := "llm-extract"
	sibling := filepath.Join(dir, name)
	if err := os.WriteFile(sibling, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return filepath.Join(dir, "llm-extract-mcp"), nil }
	t.Setenv("PATH", t.TempDir())
	if got, err := FindBinary(); err != nil || got != sibling {
		t.Errorf("sibling: got %q, %v; want %q", got, err, sibling)
	}

	pathDir := t.TempDir()
	onPath := filepath.Join(pathDir, name)
	if err := os.WriteFile(onPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return filepath.Join(t.TempDir(), "llm-extract-mcp"), nil }
	t.Setenv("PATH", pathDir)
	if got, err := FindBinary(); err != nil || got != onPath {
		t.Errorf("PATH: got %q, %v; want %q", got, err, onPath)
	}
}

// Defends the required-argument guard on every tool: a missing key must be an
// error naming it, not a CLI run with an empty value.
func TestMissingRequiredArgumentsAllTools(t *testing.T) {
	cases := []struct{ tool, in, key string }{
		{"fetch", `{}`, "url"},
		{"extract_links", `{}`, "url"},
		{"extract_relevant", `{}`, "path"},
		{"extract_relevant", `{"path":"p"}`, "context"},
	}
	for _, c := range cases {
		r := Run(context.Background(), "/nonexistent", c.tool, json.RawMessage(c.in))
		if !r.IsError || !strings.Contains(text(t, r.Content), c.key) {
			t.Errorf("%s %s: want error naming %q, got %+v", c.tool, c.in, c.key, r)
		}
	}
}

// Defends the partial stdout rule: when the CLI writes content to stdout before
// exiting non-zero (a partial result), the MCP error result must carry that stdout
// so an MCP caller sees what the CLI produced, not only the error on stderr.
func TestNonZeroExitIncludesPartialStdout(t *testing.T) {
	bin := fakeCLI(t, `echo "partial content here"; echo "something went wrong" >&2; exit 1`)
	r := Run(context.Background(), bin, "extract_links", json.RawMessage(`{"url":"http://x"}`))
	got := text(t, r.Content)
	if !r.IsError {
		t.Fatal("non-zero exit must be an error")
	}
	if !strings.Contains(got, "partial content here") {
		t.Errorf("error result must include partial stdout, got: %s", got)
	}
	if !strings.Contains(got, "something went wrong") {
		t.Errorf("error result must include stderr, got: %s", got)
	}
}

// Defends the error text when the CLI dies without writing to stderr: it must
// still name the subcommand and carry a reason.
func TestNonZeroExitWithEmptyStderr(t *testing.T) {
	bin := fakeCLI(t, "exit 1")
	r := Run(context.Background(), bin, "fetch", json.RawMessage(`{"url":"http://x"}`))
	got := text(t, r.Content)
	if !r.IsError || !strings.Contains(got, "llm-extract fetch failed: exit status 1") {
		t.Errorf("got %+v %s", r, got)
	}
}

// buildCLI compiles the real llm-extract into a temp dir, so the MCP tests
// below see what the shipped CLI prints rather than what a fake script echoes.
func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "llm-extract")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/samestrin/llm-extract-axi/v2/cmd/llm-extract").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// mcpLinksPage serves a page with two links, or a 404 when status says so.
func mcpLinksPage(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`<html><body><h1><a href="/start">Start here</a></h1><p><a href="/more">More</a></p></body></html>`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Defends what an MCP caller receives now that TOON is the CLI's default: the
// server injects no --plain and adds no logic, so a tool result is the CLI's
// TOON, help[] included, and a failure carries the TOON error document after
// the exit status. A server that starts rewriting the output, or a CLI whose
// default drifts, changes these strings.
func TestRunReturnsTheCLIsTOON(t *testing.T) {
	bin := buildCLI(t)
	t.Setenv("FETCH_MAX_RETRIES", "0")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	ok := mcpLinksPage(t, http.StatusOK)
	r := Run(context.Background(), bin, "extract_links", json.RawMessage(`{"url":"`+ok+`"}`))
	if r.IsError {
		t.Fatalf("success came back as an error: %s", text(t, r.Content))
	}
	wantOK := strings.ReplaceAll(`help[1]: "Read the top link: llm-extract extract-relevant --path <links[0].href> --context '<what you need>'"
links[2]{href,score,section,text}:
  "{{U}}/start",100,Start here,Start here
  "{{U}}/more",40,Start here,More
total: 2
url: "{{U}}"
`, "{{U}}", ok)
	if got := r.Content[0].(*mcp.TextContent).Text; got != wantOK {
		t.Errorf("success text:\n got %q\nwant %q", got, wantOK)
	}

	missing := mcpLinksPage(t, http.StatusNotFound)
	r = Run(context.Background(), bin, "extract_links", json.RawMessage(`{"url":"`+missing+`"}`))
	if !r.IsError {
		t.Fatalf("a 404 came back as success: %s", text(t, r.Content))
	}
	wantFail := strings.ReplaceAll(`llm-extract extract-links failed: exit status 1
error: "HTTP error: 404 Not Found"
help[2]: "Check the URL is reachable, or raise --timeout, then run the command again",Run 'llm-extract extract-links --help' for usage
url: "{{U}}"`, "{{U}}", missing)
	if got := r.Content[0].(*mcp.TextContent).Text; got != wantFail {
		t.Errorf("failure text:\n got %q\nwant %q", got, wantFail)
	}
}
