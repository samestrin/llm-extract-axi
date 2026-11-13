package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	goaxi "github.com/samestrin/go-axi"
)

// homeEnv points every endpoint a bare run could reach at a server that fails
// the test if called, gives the run a scratch HOME and a binary under it, and
// returns that HOME.
func homeEnv(t *testing.T) string {
	t.Helper()
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the home view made a request: %s %s", r.Method, r.URL)
	}))
	t.Cleanup(trap.Close)
	t.Setenv("HTTP_PROXY", trap.URL)
	t.Setenv("HTTPS_PROXY", trap.URL)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("FETCH_MAX_BYTES", "")

	home := t.TempDir()
	t.Setenv("HOME", home)
	oldExe := executable
	t.Cleanup(func() { executable = oldExe })
	executable = func() (string, error) { return filepath.Join(home, "bin", "llm-extract"), nil }
	return home
}

// runAXIHome runs `llm-extract --axi` and decodes stdout as one TOON document.
func runAXIHome(t *testing.T) (string, map[string]any) {
	t.Helper()
	t.Cleanup(func() { GlobalAXIOutput = false })
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--axi"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--axi exited %d; stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("--axi home view wrote to stderr: %q", stderr.String())
	}
	doc, err := goaxi.Decode(strings.NewReader(stdout.String()))
	if err != nil {
		t.Fatalf("--axi home view is not valid TOON: %v\n%s", err, stdout.String())
	}
	m, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("--axi home view is not a TOON object: %T\n%s", doc, stdout.String())
	}
	return stdout.String(), m
}

// Defends AXI principles 6 and 9 for the bare run under --axi: the home view
// must be one valid TOON document carrying where the binary is (~ for home),
// what the tool does, which commands reach outside, the live configuration and
// help[] next steps. Before this, --axi printed the same tab table as plain
// mode, which no TOON decoder reads.
func TestRunAXIHomeViewIsTOON(t *testing.T) {
	home := homeEnv(t)
	t.Setenv("OPENAI_MODEL", "test-model")
	t.Setenv("FETCH_MAX_BYTES", "1234")

	out, m := runAXIHome(t)

	if m["bin"] != "~/bin/llm-extract" || strings.Contains(out, home) {
		t.Errorf("bin = %v, want ~/bin/llm-extract with home hidden\n%s", m["bin"], out)
	}
	if m["about"] != "Fetch outside content and reduce it to what an agent needs." {
		t.Errorf("about = %v, want the one-sentence description", m["about"])
	}
	// The documented contract, written out rather than read from reachTable:
	// comparing the output with the table it was printed from would pass a
	// swapped label.
	wantReach := []map[string]string{
		{"command": "fetch", "network": "always (fetches the URL)", "model": "never"},
		{"command": "extract-links", "network": "always (fetches the URL)", "model": "only with --context (LLM link ranking)"},
		{"command": "extract-relevant", "network": "only when --path is a URL", "model": "always (--context is required)"},
		{"command": "setup", "network": "never", "model": "never"},
	}
	reachRows, _ := m["reach"].([]any)
	if len(reachRows) != len(wantReach) {
		t.Fatalf("reach has %d rows, want %d\n%s", len(reachRows), len(wantReach), out)
	}
	for i, w := range wantReach {
		row, _ := reachRows[i].(map[string]any)
		for k, v := range w {
			if row[k] != v {
				t.Errorf("reach[%d].%s = %v, want %q", i, k, row[k], v)
			}
		}
	}
	if m["model"] != "test-model" {
		t.Errorf("model = %v, want the OPENAI_MODEL in effect", m["model"])
	}
	if m["base_url"] != "https://api.openai.com/v1" {
		t.Errorf("base_url = %v, want the default in effect", m["base_url"])
	}
	if n, ok := m["fetch_max_bytes"].(float64); !ok || n != 1234 {
		t.Errorf("fetch_max_bytes = %v (%T), want 1234 from FETCH_MAX_BYTES", m["fetch_max_bytes"], m["fetch_max_bytes"])
	}
	help, _ := m["help"].([]any)
	if len(help) == 0 {
		t.Fatalf("home view has no help[] next steps\n%s", out)
	}
	for _, h := range help {
		if s, _ := h.(string); !strings.Contains(s, "llm-extract ") {
			t.Errorf("help step does not name a command to run: %q", h)
		}
	}
}

// Defends the live-state claim: api_key_set must follow the environment of
// this run, for both variables the model client reads, and the key itself
// must never be printed. A value fixed at build time, or read from the wrong
// variable, would tell an agent it can run extract-relevant when it cannot.
func TestRunAXIHomeViewAPIKeyStateIsLive(t *testing.T) {
	cases := []struct {
		name, openai, anthropic string
		want                    bool
	}{
		{"no key", "", "", false},
		{"OPENAI_API_KEY", "sk-secret-openai", "", true},
		{"ANTHROPIC_API_KEY", "", "sk-secret-anthropic", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			homeEnv(t)
			t.Setenv("OPENAI_API_KEY", tc.openai)
			t.Setenv("ANTHROPIC_API_KEY", tc.anthropic)

			out, m := runAXIHome(t)
			if m["api_key_set"] != tc.want {
				t.Errorf("api_key_set = %v, want %v\n%s", m["api_key_set"], tc.want, out)
			}
			if strings.Contains(out, "sk-secret") {
				t.Errorf("the home view printed the API key:\n%s", out)
			}
		})
	}
}

// Defends what leaves the process: OPENAI_BASE_URL may carry a password in
// its userinfo or a token in its query, and the home view is exactly the
// output an agent pastes into a transcript. Both modes must show the endpoint
// without either.
func TestHomeViewStripsBaseURLSecrets(t *testing.T) {
	for _, args := range [][]string{{"--axi"}, nil} {
		t.Run(strings.Join(append([]string{"args"}, args...), " "), func(t *testing.T) {
			homeEnv(t)
			t.Cleanup(func() { GlobalAXIOutput = false })
			t.Setenv("OPENAI_BASE_URL", "https://user:pw@host/v1?k=v")

			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("exited %d; stderr=%q", code, stderr.String())
			}
			out := stdout.String()
			if !strings.Contains(out, "https://host/v1") {
				t.Errorf("home view does not show the base URL in effect:\n%s", out)
			}
			for _, leak := range []string{"pw", "k=v", "user"} {
				if strings.Contains(out, leak) {
					t.Errorf("home view leaks %q from OPENAI_BASE_URL:\n%s", leak, out)
				}
			}
		})
	}
}

// Defends the redaction against inputs that are not a clean URL. A value
// url.Parse rejects must not be echoed raw, since the secret is still in it.
func TestRedactURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.openai.com/v1", "https://api.openai.com/v1"},
		{"https://user:pw@host/v1?k=v", "https://host/v1"},
		{"https://user@host/v1", "https://host/v1"},
		{"https://host/v1?", "https://host/v1"},
		{"https://host/v1#frag", "https://host/v1"},
		{"http://localhost:11434/v1", "http://localhost:11434/v1"},
		{"user:pw@host/v1", "(not a valid URL)"},
		{"https://host/%zz?k=v", "(not a valid URL)"},
		{"host/v1?k=v", "(not a valid URL)"},
	}
	for _, tc := range cases {
		if got := redactURL(tc.in); got != tc.want {
			t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Defends the plain home view keeping its table: --axi adds TOON, it does not
// change what a person sees. The live state appears there too, so the two
// modes do not disagree about the configuration.
func TestRunPlainHomeViewKeepsTableAndShowsState(t *testing.T) {
	homeEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-secret-openai")

	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exited %d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"command", "network", "extract-relevant  only when --path is a URL", "api key", "set", "gpt-4o-mini", "https://api.openai.com/v1", "16777216"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain home view lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sk-secret") {
		t.Errorf("plain home view printed the API key:\n%s", out)
	}
	if strings.Contains(out, "help[") || strings.Contains(out, "bin:") {
		t.Errorf("plain home view printed TOON:\n%s", out)
	}
}
