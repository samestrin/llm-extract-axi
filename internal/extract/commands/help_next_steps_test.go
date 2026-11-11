package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	goaxi "github.com/samestrin/go-axi"
	"github.com/samestrin/llm-extract-axi/pkg/output"
)

func linksOut(t *testing.T, args ...string) string {
	t.Helper()
	t.Cleanup(func() { extractLinksJSON = false; extractLinksMinimal = false; extractLinksContext = "" })
	cmd := newExtractLinksCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs(args)
	_ = cmd.Execute()
	return buf.String()
}

func helpOf(t *testing.T, out string) []any {
	t.Helper()
	doc, err := goaxi.Decode(strings.NewReader(out))
	if err != nil {
		t.Fatalf("not TOON: %v\n%s", err, out)
	}
	h, _ := doc.(map[string]any)["help"].([]any)
	return h
}

func helpPageServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

// Defends Q2/T4: an empty extract-links run under --axi names a concrete next
// command, and --json stays free of help[].
func TestExtractLinksHelpOnEmptyAndJSONUnchanged(t *testing.T) {
	srv := helpPageServer(t, "<html><body><p>none</p></body></html>")
	withAXI(t)
	h := helpOf(t, linksOut(t, "--url", srv.URL))
	if len(h) == 0 || !strings.Contains(h[0].(string), "llm-extract extract-relevant --path") {
		t.Errorf("empty result lacks a concrete next command: %v", h)
	}
	GlobalAXIOutput = false
	outputSetOff()
	if out := linksOut(t, "--url", srv.URL, "--json"); strings.Contains(out, "help") {
		t.Errorf("--json carries help[]:\n%s", out)
	}
}

// Defends T4: a failed fetch under --axi carries help[]; a list carries one step.
func TestExtractLinksHelpOnErrorAndList(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	withAXI(t)
	if h := helpOf(t, linksOut(t, "--url", bad.URL)); len(h) == 0 {
		t.Error("error output lacks help[]")
	}
	ok := helpPageServer(t, `<html><body><h1><a href="/top">Top</a></h1></body></html>`)
	h := helpOf(t, linksOut(t, "--url", ok.URL))
	if len(h) != 1 || !strings.Contains(h[0].(string), "/top") {
		t.Errorf("list should carry one next step naming the top link: %v", h)
	}
}

// Defends T4: extract-relevant carries help[] on empty and error, not in --json.
func TestExtractRelevantHelp(t *testing.T) {
	srv := fakeLLM(t, func(string) string { return "NONE" })
	pointExtractRelevantAt(t, srv)
	src := filepath.Join(t.TempDir(), "d.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withAXI(t)
	out, _ := runExtractRelevantCmd(t, "--path", src, "--context", "x")
	if h := helpOf(t, out); len(h) == 0 {
		t.Errorf("empty result lacks help[]:\n%s", out)
	}
	out, _ = runExtractRelevantCmd(t, "--path", src+"-missing", "--context", "x")
	_ = out
	// An error after work starts: unreachable model.
	srv.Close()
	out, _ = runExtractRelevantCmd(t, "--path", src, "--context", "x")
	if h := helpOf(t, out); len(h) == 0 {
		t.Errorf("error output lacks help[]:\n%s", out)
	}
	GlobalAXIOutput = false
	outputSetOff()
	out, _ = runExtractRelevantCmd(t, "--path", src, "--context", "x", "--json")
	if strings.Contains(out, "help") {
		t.Errorf("--json carries help[]:\n%s", out)
	}
}

func outputSetOff() { output.SetDefaultAXI(false) }
