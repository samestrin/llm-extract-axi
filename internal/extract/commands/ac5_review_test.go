package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	goaxi "github.com/samestrin/go-axi"
)

// These tests defend the EST-6865 AC5 review findings (EST-6877): each one is
// a place where the binary contradicted the exit-code or help[] contract the
// README, docs/output.md and docs/axi-audit.md state.

// runDoc runs args and decodes stdout as TOON.
func runDoc(t *testing.T, args ...string) (int, map[string]any, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	if errb.Len() != 0 {
		t.Errorf("%v wrote to stderr: %q", args, errb.String())
	}
	doc, err := goaxi.Decode(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("%v: stdout is not TOON: %v\n%s", args, err, out.String())
	}
	m, _ := doc.(map[string]any)
	return code, m, out.String()
}

// Defends item 1: `fetch` with no URL or two URLs is a malformed invocation,
// exit 2, but cobra's ExactArgs error carries no usage marker and exited 1,
// telling an agent a retry might help.
func TestFetchWrongArgCountIsUsageError(t *testing.T) {
	for _, args := range [][]string{{"fetch"}, {"fetch", "https://a.invalid", "https://b.invalid"}} {
		code, doc, out := runDoc(t, args...)
		if code != 2 {
			t.Errorf("%v exited %d, want 2:\n%s", args, code, out)
		}
		if doc["error"] != true || !strings.Contains(out, "arg") {
			t.Errorf("%v: no error document naming the argument count:\n%s", args, out)
		}
	}
}

// Defends item 2: an unsupported `fetch --format` is a rejected flag value,
// exit 2 like every other one, and is rejected before any request is sent.
func TestFetchUnsupportedFormatIsUsageError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	code, doc, out := runDoc(t, "fetch", "--format", "text", srv.URL)
	if code != 2 {
		t.Errorf("fetch --format text exited %d, want 2:\n%s", code, out)
	}
	if doc["error"] != true || !strings.Contains(out, "--format") {
		t.Errorf("no error document naming --format:\n%s", out)
	}
	if hits.Load() != 0 {
		t.Errorf("fetch sent %d request(s) before rejecting --format", hits.Load())
	}
}

// Defends item 3: a bare `completion` or an unknown shell printed help and
// exited 0, so a mistyped shell name looked like success. It is a usage
// error that lists the shells, and a real shell still prints its script.
func TestCompletionWithoutAKnownShellIsUsageError(t *testing.T) {
	for _, args := range [][]string{{"completion"}, {"completion", "bogus"}} {
		code, doc, out := runDoc(t, args...)
		if code != 2 {
			t.Errorf("%v exited %d, want 2:\n%s", args, code, out)
		}
		cmds, _ := doc["commands"].([]any)
		var names []string
		for _, c := range cmds {
			names = append(names, c.(string))
		}
		if doc["error"] != true || !strings.Contains(strings.Join(names, ","), "bash") {
			t.Errorf("%v: no error document listing the shells:\n%s", args, out)
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"completion", "bash"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "# bash completion") {
		t.Errorf("completion bash exited %d and printed %.40q, want 0 and the script", code, out.String())
	}
	out.Reset()
	if code := run([]string{"completion", "--help"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "bash") {
		t.Errorf("completion --help exited %d:\n%s", code, out.String())
	}
}

// Defends item 5: a ranking failure that is not a key problem, such as a
// model reply that is not JSON or a server error, used to get the "Check the
// key configuration" advice, sending an agent to fix a key that works. The
// key advice is kept for an unusable key configuration and a rejected key.
func TestExtractLinksRankingHelpNamesTheKeyOnlyForAKeyProblem(t *testing.T) {
	page := helpPageServer(t, `<html><body><h1><a href="/a">A</a></h1></body></html>`)
	keyHint := func(h []any) bool { return len(h) > 0 && strings.Contains(h[0].(string), "API_KEY") }

	t.Run("reply is not JSON", func(t *testing.T) {
		pointExtractRelevantAt(t, fakeLLM(t, func(string) string { return "I cannot rank these." }))
		withAXI(t)
		h := helpOf(t, linksOut(t, "--url", page.URL, "--context", "auth"))
		if len(h) == 0 || keyHint(h) {
			t.Errorf("a non-JSON reply gets key advice: %v", h)
		}
	})
	t.Run("server error", func(t *testing.T) {
		failingLLM(t, 500)
		withAXI(t)
		h := helpOf(t, linksOut(t, "--url", page.URL, "--context", "auth"))
		if len(h) == 0 || keyHint(h) {
			t.Errorf("a 500 gets key advice: %v", h)
		}
	})
	t.Run("key rejected", func(t *testing.T) {
		failingLLM(t, 401)
		withAXI(t)
		if h := helpOf(t, linksOut(t, "--url", page.URL, "--context", "auth")); !keyHint(h) {
			t.Errorf("a 401 lacks key advice: %v", h)
		}
	})
	t.Run("no key", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "")
		t.Setenv("ANTHROPIC_API_KEY", "")
		withAXI(t)
		if h := helpOf(t, linksOut(t, "--url", page.URL, "--context", "auth")); !keyHint(h) {
			t.Errorf("a missing key lacks key advice: %v", h)
		}
	})
}

// Defends item 6, declared rather than changed: `extract-relevant --path` on a
// directory with no text file to read searched nothing, so it is a failure
// (exit 1, error, no total), never `total: 0`, which would claim a search
// that did not happen. docs/output.md and the audit say the same.
func TestExtractRelevantEmptyDirectoryIsAFailure(t *testing.T) {
	var calls atomic.Int32
	pointExtractRelevantAt(t, fakeLLM(t, func(string) string { calls.Add(1); return "x" }))
	code, doc, out := runDoc(t, "extract-relevant", "--path", t.TempDir(), "--context", "auth")
	if code != 1 {
		t.Errorf("empty directory exited %d, want 1:\n%s", code, out)
	}
	if _, ok := doc["error"].(string); !ok {
		t.Errorf("empty directory carries no error:\n%s", out)
	}
	if _, ok := doc["total"]; ok {
		t.Errorf("empty directory carries a total, which reads as an empty success:\n%s", out)
	}
	if calls.Load() != 0 {
		t.Errorf("empty directory called the model %d time(s)", calls.Load())
	}
}
