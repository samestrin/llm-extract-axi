// Package e2e drives the built binaries from outside the module's packages.
//
// The tests need `make build` first. They FAIL, not skip, when a binary is
// missing: a skip is how an end-to-end suite stops running without anyone
// noticing. CI runs `make build` before `go test` for this reason.
package e2e

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// binary returns the path of a built binary under ./build, failing the test
// with the fix when it is absent.
func binary(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "build", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s is not built (%v); run `make build` before `go test`", path, err)
	}
	return path
}

// Defends the Makefile's -X target and the bare-version contract: every
// version flag on both binaries prints the same version with no program name,
// no leading "v", exit 0, and never "dev" (an unstamped build).
func TestBinariesPrintBareStampedVersion(t *testing.T) {
	var first string
	for _, bin := range []string{"llm-extract", "llm-extract-mcp"} {
		for _, flag := range []string{"-v", "-V", "--version"} {
			out, err := exec.Command(binary(t, bin), flag).Output()
			if err != nil {
				t.Fatalf("%s %s failed: %v\n%s", bin, flag, err, out)
			}
			got := strings.TrimSpace(string(out))
			if got == "" || got == "dev" || strings.ContainsAny(got, " \t") || strings.HasPrefix(got, "v") {
				t.Errorf("%s %s printed %q, want a bare stamped version", bin, flag, got)
			}
			if first == "" {
				first = got
			} else if got != first {
				t.Errorf("%s %s printed %q, want %q", bin, flag, got, first)
			}
		}
	}
}

// Defends the exit code across the process boundary: an unknown command must
// exit non-zero from the real binary, not only from run().
func TestCLIUnknownCommandExitsNonZero(t *testing.T) {
	cmd := exec.Command(binary(t, "llm-extract"), "no-such-command")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unknown command exited 0:\n%s", out)
	}
	if !strings.Contains(string(out), "no-such-command") {
		t.Errorf("error does not name the bad command:\n%s", out)
	}
}

// Defends the exit code of `fetch` across the process boundary: a challenge
// page must exit non-zero from the real binary and print nothing on stdout.
func TestCLIFetchChallengeExitsNonZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<title>Just a moment...</title>"))
	}))
	defer srv.Close()
	cmd := exec.Command(binary(t, "llm-extract"), "fetch", srv.URL)
	cmd.Env = append(os.Environ(), "FETCH_MAX_RETRIES=0")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("challenge page exited 0; stdout=%q", stdout.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "needs a browser") {
		t.Errorf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// llmEnv returns the process environment with every model setting replaced,
// so an exported key on the machine running the tests cannot change the path.
func llmEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch strings.SplitN(kv, "=", 2)[0] {
		case "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL":
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// Defends the exit code of `extract-links` across the process boundary: a 404
// page must exit non-zero from the real binary, with the reason on stderr.
func TestCLIExtractLinksFetchErrorExitsNonZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cmd := exec.Command(binary(t, "llm-extract"), "extract-links", "--url", srv.URL, "--json")
	cmd.Env = llmEnv()
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("a 404 exited 0; stdout=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "404") || !strings.Contains(stderr.String(), "404") {
		t.Errorf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// Defends the partial result across the process boundary: when --context
// ranking fails, the heuristic links are still printed and the exit is
// non-zero.
func TestCLIExtractLinksRankingFailureExitsNonZeroAndKeepsLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h1><a href="/start">Start</a></h1></body></html>`))
	}))
	defer srv.Close()
	cmd := exec.Command(binary(t, "llm-extract"), "extract-links", "--url", srv.URL, "--context", "start", "--json")
	cmd.Env = llmEnv()
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("ranking with no API key exited 0; stdout=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "/start") || !strings.Contains(stderr.String(), "LLM ranking failed") {
		t.Errorf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// Defends the exit code of `extract-relevant` across the process boundary: a
// failed model call must exit non-zero from the real binary. The fake answers
// 401, which the client does not retry, to keep the test fast.
func TestCLIExtractRelevantModelErrorExitsNonZero(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer llm.Close()
	src := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary(t, "llm-extract"), "extract-relevant", "--path", src, "--context", "x")
	cmd.Env = llmEnv("OPENAI_API_KEY=sk-test", "OPENAI_BASE_URL="+llm.URL, "OPENAI_MODEL=fake")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("a model 401 exited 0; stdout=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "ERROR:") || !strings.Contains(stderr.String(), "401") {
		t.Errorf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// Defends the AXI exit-code split across the process boundary: a malformed
// invocation exits 2 and a command that ran and failed exits 1. A flat 1 for
// both leaves a caller unable to tell a typo in a flag from an outage.
func TestCLIExitCodeSeparatesUsageFromFailure(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"unknown flag", []string{"--bogus"}, 2},
		{"unknown command", []string{"no-such-command"}, 2},
		{"failed fetch", []string{"fetch", down.URL}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(binary(t, "llm-extract"), tc.args...)
			cmd.Env = append(os.Environ(), "FETCH_MAX_RETRIES=0")
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("want exit %d, got err=%v\n%s", tc.want, err, out)
			}
			if got := exitErr.ExitCode(); got != tc.want {
				t.Errorf("exit %d, want %d\n%s", got, tc.want, out)
			}
		})
	}
}
