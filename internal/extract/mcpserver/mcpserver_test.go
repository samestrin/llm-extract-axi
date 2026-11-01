package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
