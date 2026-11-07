package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runFetchCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("FETCH_MAX_RETRIES", "0")
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"fetch"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func pageServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Defends the payload: the body reaches stdout, not just exit 0.
func TestFetchCmdWritesBody(t *testing.T) {
	srv := pageServer(t, 200, "<p>hi</p>")
	code, out, errOut := runFetchCmd(t, srv.URL)
	if code != 0 || out != "<p>hi</p>" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

// Defends the core rule: a challenge page exits non-zero, says a browser is
// needed on stderr, and writes nothing to stdout.
func TestFetchCmdChallengeIsError(t *testing.T) {
	srv := pageServer(t, 403, "<title>Just a moment...</title>")
	code, out, errOut := runFetchCmd(t, srv.URL)
	if code == 0 {
		t.Fatalf("challenge exited 0; out=%q", out)
	}
	if !strings.Contains(errOut, "needs a browser") || out != "" {
		t.Errorf("err=%q out=%q", errOut, out)
	}
}

// Defends against an empty "success": a 404 and a refused connection both
// exit non-zero, with --json too.
func TestFetchCmdFailuresAreErrors(t *testing.T) {
	srv404 := pageServer(t, 404, "gone")
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	for _, args := range [][]string{{srv404.URL}, {srv404.URL, "--json"}, {deadURL}, {deadURL, "--json"}} {
		code, out, errOut := runFetchCmd(t, args...)
		if code == 0 || out != "" || errOut == "" {
			t.Errorf("%v: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
	if _, _, e := runFetchCmd(t, srv404.URL); !strings.Contains(e, "404") {
		t.Errorf("error does not name the status: %q", e)
	}
}

// Defends --output: the file holds the body and nothing is written to a file
// when the fetch fails.
func TestFetchCmdOutputFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.html")
	srv := pageServer(t, 200, "<p>saved</p>")
	code, out, errOut := runFetchCmd(t, srv.URL, "--output", path)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "<p>saved</p>" {
		t.Errorf("file=%q err=%v", got, err)
	}
	if !strings.Contains(out, "Saved 12 bytes") || !strings.Contains(out, path) {
		t.Errorf("summary missing: %q", out)
	}

	bad := pageServer(t, 500, "x")
	failPath := filepath.Join(dir, "fail.html")
	if code, _, _ := runFetchCmd(t, bad.URL, "--output", failPath); code == 0 {
		t.Error("500 exited 0")
	}
	if _, err := os.Stat(failPath); err == nil {
		t.Error("failed fetch left an output file")
	}
}

// Defends an unwritable --output being an error, not a silent success.
func TestFetchCmdOutputUnwritable(t *testing.T) {
	srv := pageServer(t, 200, "x")
	code, _, errOut := runFetchCmd(t, srv.URL, "--output", filepath.Join(t.TempDir(), "no", "dir", "f"))
	if code == 0 || !strings.Contains(errOut, "write") {
		t.Errorf("code=%d err=%q", code, errOut)
	}
}

// Defends --json: the payload carries the body, status and tier.
func TestFetchCmdJSON(t *testing.T) {
	srv := pageServer(t, 200, "<p>j</p>")
	code, out, errOut := runFetchCmd(t, srv.URL, "--json")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var got FetchResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if got.Body != "<p>j</p>" || got.Status != 200 || got.Tier != "direct" || got.Bytes != 8 {
		t.Errorf("bad payload: %+v", got)
	}
}

// Defends input validation: non-http URLs, a bad --format and the formats not
// yet ported are errors rather than being fetched or ignored.
func TestFetchCmdRejectsBadInput(t *testing.T) {
	srv := pageServer(t, 200, "x")
	for _, args := range [][]string{
		{"ftp://example.com"},
		{"example.com"},
		{srv.URL, "--format", "markdown"},
		{srv.URL, "--format", "text"},
		{srv.URL, "--format", "bogus"},
		{},
	} {
		code, out, errOut := runFetchCmd(t, args...)
		if code == 0 || out != "" || errOut == "" {
			t.Errorf("%v: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
}

// Defends flag plumbing: --user-agent reaches the server, and --timeout and
// --retries are accepted.
func TestFetchCmdFlagsApply(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.UserAgent()
	}))
	defer srv.Close()
	code, _, errOut := runFetchCmd(t, srv.URL, "--user-agent", "zz", "--timeout", "3", "--retries", "0")
	if code != 0 || ua != "zz" {
		t.Errorf("code=%d ua=%q err=%q", code, ua, errOut)
	}
}

// Defends against a crash on a legal flag value: --retries -1 must not panic
// and must still fetch.
func TestFetchCmdNegativeRetries(t *testing.T) {
	srv := pageServer(t, 200, "ok")
	code, out, errOut := runFetchCmd(t, srv.URL, "--retries", "-1")
	if code != 0 || out != "ok" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

// Defends the --max-bytes flag end to end: a body over the cap exits non-zero,
// names the cap, prints nothing to stdout, and writes no --output file. It is
// an error, never a truncated success saved to disk.
func TestFetchCmdMaxBytesOverCapIsError(t *testing.T) {
	srv := pageServer(t, 200, strings.Repeat("x", 200))
	code, out, errOut := runFetchCmd(t, srv.URL, "--max-bytes", "100")
	if code == 0 || out != "" {
		t.Fatalf("over-cap exited %d with out=%q", code, out)
	}
	if !strings.Contains(errOut, "cap") || !strings.Contains(errOut, "100") {
		t.Errorf("error does not name the size cap: %q", errOut)
	}
	// --json must not emit a partial payload either.
	if code, out, _ := runFetchCmd(t, srv.URL, "--max-bytes", "100", "--json"); code == 0 || out != "" {
		t.Errorf("--json over-cap produced output: code=%d out=%q", code, out)
	}
	// --output must leave no file behind.
	dir := t.TempDir()
	path := filepath.Join(dir, "page.html")
	if code, _, _ := runFetchCmd(t, srv.URL, "--max-bytes", "100", "--output", path); code == 0 {
		t.Errorf("--output over-cap exited 0")
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("over-cap fetch wrote a partial output file: %s", path)
	}
}

// Defends a body under the cap still succeeds, so the flag is not a blanket
// failure: the same 200-byte page with a larger cap prints whole.
func TestFetchCmdMaxBytesUnderCapSucceeds(t *testing.T) {
	srv := pageServer(t, 200, strings.Repeat("y", 200))
	code, out, errOut := runFetchCmd(t, srv.URL, "--max-bytes", "500")
	if code != 0 || out != strings.Repeat("y", 200) {
		t.Fatalf("under-cap body wrong: code=%d out=%d bytes err=%q", code, len(out), errOut)
	}
}

// Defends the flag overriding the env: FETCH_MAX_BYTES caps the body even
// without --max-bytes, proving the command threads the configured cap.
func TestFetchCmdMaxBytesEnvOverride(t *testing.T) {
	srv := pageServer(t, 200, strings.Repeat("z", 150))
	t.Setenv("FETCH_MAX_BYTES", "100")
	code, out, errOut := runFetchCmd(t, srv.URL)
	if code == 0 || out != "" || !strings.Contains(errOut, "cap") {
		t.Errorf("env cap not applied: code=%d out=%q err=%q", code, out, errOut)
	}
	// The flag beats the env: --max-bytes 500 fetches the same page.
	code, out, errOut = runFetchCmd(t, srv.URL, "--max-bytes", "500")
	if code != 0 || len(out) != 150 {
		t.Errorf("flag did not override env: code=%d out=%d bytes err=%q", code, len(out), errOut)
	}
}

// Defends --max-bytes 0 falling back to the default rather than erroring or
// reading nothing, matching how --timeout/--retries treat a non-positive value.
func TestFetchCmdMaxBytesZeroUsesDefault(t *testing.T) {
	srv := pageServer(t, 200, "small")
	code, out, errOut := runFetchCmd(t, srv.URL, "--max-bytes", "0")
	if code != 0 || out != "small" {
		t.Fatalf("--max-bytes 0 broke a normal body: code=%d out=%q err=%q", code, out, errOut)
	}
}
