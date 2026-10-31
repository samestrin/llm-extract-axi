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
