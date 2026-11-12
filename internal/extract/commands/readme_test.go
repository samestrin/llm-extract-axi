package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samestrin/llm-extract-axi/internal/support/fetch"
)

// readmeSection returns the README text between a "## heading" and the next
// "## " heading.
func readmeSection(t *testing.T, heading string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	_, rest, ok := strings.Cut(string(data), "\n## "+heading+"\n")
	if !ok {
		t.Fatalf("README.md has no %q section", heading)
	}
	body, _, _ := strings.Cut(rest, "\n## ")
	return body
}

var (
	readmeInvocation = regexp.MustCompile(`llm-extract\s+([a-z][a-z-]*)([^\x60\n]*)`)
	readmeFlag       = regexp.MustCompile(`(?:^|\s)(--[a-z][a-z-]*|-[a-zA-Z])\b`)
)

// flagListed reports whether help lists flag in its Flags section. A bare
// substring match would pass on a flag that survives only in an Examples line.
func flagListed(help, flag string) bool {
	re := regexp.MustCompile(`(?m)^\s+(-[a-zA-Z], )?` + regexp.QuoteMeta(flag) + `(?:\s|=|$)`)
	if !strings.HasPrefix(flag, "--") {
		re = regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(flag) + `, --`)
	}
	return re.MatchString(help)
}

// TestReadmeUsageFlagsAppearInHelp defends the README against drifting from
// the code: every flag the Usage section names for a command must appear in
// that command's --help, so renaming a flag in code turns this red. It drives
// the command tree in process through run, not a built binary.
func TestReadmeUsageFlagsAppearInHelp(t *testing.T) {
	usage := readmeSection(t, "Usage")
	help := map[string]string{}
	helpFor := func(cmd string) string {
		if h, ok := help[cmd]; ok {
			return h
		}
		var out, errb bytes.Buffer
		if code := run([]string{cmd, "--help"}, &out, &errb); code != 0 {
			t.Fatalf("%s --help exited %d: %s", cmd, code, errb.String())
		}
		help[cmd] = out.String()
		return help[cmd]
	}
	checked := 0
	for _, m := range readmeInvocation.FindAllStringSubmatch(usage, -1) {
		cmd, rest := m[1], m[2]
		if cmd == "" {
			continue
		}
		h := helpFor(cmd)
		for _, f := range readmeFlag.FindAllStringSubmatch(rest, -1) {
			checked++
			if f[1] == "--axi" {
				continue // global flag, checked against the root below
			}
			if !flagListed(h, f[1]) {
				t.Errorf("README names %s for %q but `%s --help` does not list it", f[1], cmd, cmd)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no flags found in the README Usage section; the parser matched nothing")
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--help"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "--axi") {
		t.Errorf("README names --axi but root --help (exit %d) does not list it", code)
	}
}

// readmeConfigEnvVars returns the variable names the README Configuration
// table documents, in table order. The table is the oracle: an independent doc
// contract, not a restatement of the code.
func readmeConfigEnvVars(t *testing.T) []string {
	t.Helper()
	cfg := readmeSection(t, "Configuration")
	var names []string
	for _, line := range strings.Split(cfg, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cell, _, _ := strings.Cut(strings.TrimPrefix(line, "|"), "|")
		for _, m := range regexp.MustCompile("`([A-Z][A-Z0-9_]+)`").FindAllStringSubmatch(cell, -1) {
			names = append(names, m[1])
		}
	}
	if len(names) == 0 {
		t.Fatal("no env vars found in the README Configuration table")
	}
	return names
}

// modelCall is what one request to the fake model endpoint carried.
type modelCall struct{ auth, model string }

// recordingLLM stands in for the model endpoint and records the authorization
// and model of the last call, plus how many calls arrived, so a probe can prove
// a variable reached the request rather than merely appearing in source.
func recordingLLM(t *testing.T) (*httptest.Server, *atomic.Value, *atomic.Int32) {
	t.Helper()
	var last atomic.Value
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last.Store(modelCall{auth: r.Header.Get("Authorization"), model: req.Model})
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"a passage"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &last, &calls
}

// extractOneFile runs extract-relevant over a one-file source through the real
// command tree, so a probe observes what a run does, not what a loader returns.
func extractOneFile(t *testing.T) (code int, stdout, stderr string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(src, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return runCLI(t, "extract-relevant", "--path", src, "--context", "c", "--json")
}

// probeAPIKey proves one key variable is read: with neither key set the run
// fails before any model call, and with this one set the call carries it. A
// name that survived only in an error message would pass the second half and
// fail the first.
func probeAPIKey(t *testing.T, name, value string) {
	t.Helper()
	noAPIKey(t)
	srv, last, calls := recordingLLM(t)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_MODEL", "probe-model")

	code, _, stderr := extractOneFile(t)
	if code == 0 || !strings.Contains(stderr, "API key") {
		t.Fatalf("with no key set the run exited %d, want a failure naming the API key: %q", code, stderr)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the run reached the model %d times with no key set", n)
	}

	t.Setenv(name, value)
	if code, out, stderr := extractOneFile(t); code != 0 {
		t.Fatalf("with %s set the run exited %d: %s %s", name, code, out, stderr)
	}
	call, _ := last.Load().(modelCall)
	if want := "Bearer " + value; call.auth != want {
		t.Errorf("%s did not reach the model call: Authorization = %q, want %q", name, call.auth, want)
	}
}

// probeBaseURL proves OPENAI_BASE_URL selects the endpoint: two fake endpoints,
// and each is reached only while the variable names it. Nothing here can fall
// back to a real endpoint, because the variable is always set to a fake.
func probeBaseURL(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-probe")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_MODEL", "probe-model")
	a, _, aCalls := recordingLLM(t)
	b, _, bCalls := recordingLLM(t)

	t.Setenv("OPENAI_BASE_URL", a.URL)
	if code, out, stderr := extractOneFile(t); code != 0 {
		t.Fatalf("the run exited %d: %s %s", code, out, stderr)
	}
	if aCalls.Load() != 1 || bCalls.Load() != 0 {
		t.Errorf("OPENAI_BASE_URL=%s did not select the endpoint: first got %d calls, second %d", a.URL, aCalls.Load(), bCalls.Load())
	}

	t.Setenv("OPENAI_BASE_URL", b.URL)
	if code, out, stderr := extractOneFile(t); code != 0 {
		t.Fatalf("the run exited %d: %s %s", code, out, stderr)
	}
	if bCalls.Load() != 1 {
		t.Errorf("OPENAI_BASE_URL=%s was not used: the second endpoint got %d calls, want 1", b.URL, bCalls.Load())
	}
}

// probeModel proves OPENAI_MODEL names the model a run asks for: two values,
// each observed in the request body, so a hard-coded model cannot pass.
func probeModel(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-probe")
	t.Setenv("ANTHROPIC_API_KEY", "")
	srv, last, _ := recordingLLM(t)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	for _, want := range []string{"probe-model-a", "probe-model-b"} {
		t.Setenv("OPENAI_MODEL", want)
		if code, out, stderr := extractOneFile(t); code != 0 {
			t.Fatalf("the run exited %d: %s %s", code, out, stderr)
		}
		call, _ := last.Load().(modelCall)
		if call.model != want {
			t.Errorf("OPENAI_MODEL=%q did not reach the model call: model = %q", want, call.model)
		}
	}
}

// probeFetchMaxBytes proves FETCH_MAX_BYTES caps what a fetch reads: the same
// page is an error under a small cap and a success under a larger one.
func probeFetchMaxBytes(t *testing.T) {
	body := strings.Repeat("x", 64)
	srv := pageServer(t, http.StatusOK, body)

	t.Setenv("FETCH_MAX_BYTES", "16")
	code, out, stderr := runCLI(t, "fetch", srv.URL)
	if code == 0 || !strings.Contains(stderr, "size cap") {
		t.Errorf("a body over FETCH_MAX_BYTES=16 exited %d, want a size-cap error: %s %s", code, out, stderr)
	}

	t.Setenv("FETCH_MAX_BYTES", "4096")
	code, out, stderr = runCLI(t, "fetch", srv.URL)
	if code != 0 || out != body {
		t.Errorf("a body under FETCH_MAX_BYTES=4096 exited %d with %q, want the whole page: %s", code, out, stderr)
	}
}

// probeFetchTimeout proves FETCH_TIMEOUT bounds one attempt: a page slower
// than one second fails at FETCH_TIMEOUT=1 and succeeds at 5. Retries are off
// so the probe costs one attempt, not three.
func probeFetchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1100 * time.Millisecond)
		_, _ = w.Write([]byte("slow page"))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FETCH_MAX_RETRIES", "0")

	t.Setenv("FETCH_TIMEOUT", "1")
	code, out, stderr := runCLI(t, "fetch", srv.URL)
	if code == 0 || stderr == "" {
		t.Errorf("a page slower than FETCH_TIMEOUT=1 exited %d, want a timeout error: %s %s", code, out, stderr)
	}

	t.Setenv("FETCH_TIMEOUT", "5")
	code, out, stderr = runCLI(t, "fetch", srv.URL)
	if code != 0 || out != "slow page" {
		t.Errorf("the same page under FETCH_TIMEOUT=5 exited %d with %q, want the whole page: %s", code, out, stderr)
	}
}

// probeFetchMaxRetries proves FETCH_MAX_RETRIES sets the attempt count on a
// retryable status: 0 means one request, 1 means two.
func probeFetchMaxRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		retries string
		want    int32
	}{{"0", 1}, {"1", 2}} {
		hits.Store(0)
		t.Setenv("FETCH_MAX_RETRIES", tc.retries)
		if code, _, _ := runCLI(t, "fetch", srv.URL); code == 0 {
			t.Errorf("a 500 page exited 0 with FETCH_MAX_RETRIES=%s", tc.retries)
		}
		if got := hits.Load(); got != tc.want {
			t.Errorf("FETCH_MAX_RETRIES=%s made %d attempts, want %d", tc.retries, got, tc.want)
		}
	}
}

// probeFetchUserAgent proves FETCH_USER_AGENT sets the request's User-Agent,
// and that unsetting it returns the built-in default rather than sticking.
func probeFetchUserAgent(t *testing.T) {
	var ua atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua.Store(r.UserAgent())
		_, _ = w.Write([]byte("page"))
	}))
	t.Cleanup(srv.Close)

	const probeUA = "readme-probe-agent"
	t.Setenv("FETCH_USER_AGENT", probeUA)
	if code, out, stderr := runCLI(t, "fetch", srv.URL); code != 0 || out != "page" {
		t.Fatalf("fetch exited %d with %q: %s", code, out, stderr)
	}
	if got, _ := ua.Load().(string); got != probeUA {
		t.Errorf("FETCH_USER_AGENT did not reach the request: User-Agent = %q, want %q", got, probeUA)
	}

	t.Setenv("FETCH_USER_AGENT", "")
	if code, out, stderr := runCLI(t, "fetch", srv.URL); code != 0 || out != "page" {
		t.Fatalf("fetch exited %d with %q: %s", code, out, stderr)
	}
	if got, _ := ua.Load().(string); got != fetch.DefaultUserAgent {
		t.Errorf("with FETCH_USER_AGENT unset the request used %q, want the default %q", got, fetch.DefaultUserAgent)
	}
}

// TestReadmeConfigurationEnvVarsAreRead defends the Configuration table: every
// environment variable it names must change what a run does. Each probe sets
// the variable, drives the real command tree through run against a fake
// endpoint, and observes the effect -- the key or model the request carried,
// the endpoint it reached, the cap, timeout, attempt count or User-Agent a
// fetch used.
//
// It replaces a grep for the quoted name in non-test source, which a name
// surviving only in a comment or an error message passed: removing the
// os.Getenv reads while keeping ErrAPIKeyRequired's text left it green while
// the documented variable stopped working (release gate F1).
func TestReadmeConfigurationEnvVarsAreRead(t *testing.T) {
	probes := map[string]func(*testing.T){
		"OPENAI_API_KEY":    func(t *testing.T) { probeAPIKey(t, "OPENAI_API_KEY", "sk-openai-probe") },
		"ANTHROPIC_API_KEY": func(t *testing.T) { probeAPIKey(t, "ANTHROPIC_API_KEY", "sk-anthropic-probe") },
		"OPENAI_BASE_URL":   probeBaseURL,
		"OPENAI_MODEL":      probeModel,
		"FETCH_MAX_BYTES":   probeFetchMaxBytes,
		"FETCH_TIMEOUT":     probeFetchTimeout,
		"FETCH_MAX_RETRIES": probeFetchMaxRetries,
		"FETCH_USER_AGENT":  probeFetchUserAgent,
	}
	names := readmeConfigEnvVars(t)
	for _, n := range names {
		probe, ok := probes[n]
		if !ok {
			t.Errorf("README documents %s but no behavioural probe covers it", n)
			continue
		}
		t.Run(n, probe)
	}
	// A probe for a name the table no longer documents is drift too: it would
	// keep passing while the README stopped telling anyone about the variable.
	for n := range probes {
		if !contains(names, n) {
			t.Errorf("a probe covers %s but the README Configuration table no longer documents it", n)
		}
	}
}
