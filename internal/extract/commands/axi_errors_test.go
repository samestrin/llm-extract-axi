package commands

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	goaxi "github.com/samestrin/go-axi"
)

// These tests defend the AXI rule that under --axi an error is on stdout, in
// the same TOON encoding as a success, with a next step in help[]. A caller
// reading stdout alone must never see an empty stream and conclude nothing
// went wrong, and must never have to switch to stderr to learn why.

// axiError decodes stdout as one TOON document and returns it. It fails the
// test unless the document is an error with a message and a non-empty help[].
func axiError(t *testing.T, stdout string) map[string]any {
	t.Helper()
	v, err := goaxi.Decode(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("stdout is not one TOON document: %v\n%s", err, stdout)
	}
	doc, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("stdout is not a TOON object: %#v", v)
	}
	if doc["error"] != true {
		t.Errorf("error is %#v, want true:\n%s", doc["error"], stdout)
	}
	if msg, _ := doc["message"].(string); msg == "" {
		t.Errorf("no message:\n%s", stdout)
	}
	if help, _ := doc["help"].([]any); len(help) == 0 {
		t.Errorf("no help[]:\n%s", stdout)
	}
	return doc
}

// strs returns doc[key] as a []string, failing the test if it is not a list.
func strs(t *testing.T, doc map[string]any, key string) []string {
	t.Helper()
	raw, ok := doc[key].([]any)
	if !ok {
		t.Fatalf("%s is %#v, want a list", key, doc[key])
	}
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i], _ = v.(string)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// countingServer counts the requests it gets, so a test can prove a rejected
// invocation reached no dependency.
func countingServer(t *testing.T, status int) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

// Defends every failure that stops a command before its work: under --axi it
// is a TOON error on stdout, stderr stays empty, and the exit code still
// separates a failure (1) from a malformed invocation (2).
func TestAXIErrorsGoToStdoutAsTOON(t *testing.T) {
	noAPIKey(t)
	t.Setenv("FETCH_MAX_RETRIES", "0")
	down, _ := countingServer(t, http.StatusInternalServerError)
	cases := []struct {
		name    string
		args    []string
		code    int
		message string
		help    string
	}{
		{"no API key", []string{"extract-relevant", "--context", "x", "--axi"}, 1, "API key", "OPENAI_API_KEY"},
		{"fetch bad URL", []string{"fetch", "not-a-url", "--axi"}, 1, "invalid URL", "http://"},
		{"extract-links bad URL", []string{"extract-links", "--url", "ftp://x", "--axi"}, 1, "invalid URL", "http://"},
		{"failed fetch", []string{"fetch", down, "--axi"}, 1, "500", "fetch --help"},
		{"failed fetch --json", []string{"fetch", "--json", down, "--axi"}, 1, "500", "fetch --help"},
		{"unknown command", []string{"no-such-command", "--axi"}, 2, `"no-such-command"`, "llm-extract --help"},
		{"required flag", []string{"extract-links", "--axi"}, 2, `"url"`, "extract-links --help"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, tc.args...)
			if code != tc.code {
				t.Errorf("exit %d, want %d", code, tc.code)
			}
			if stderr != "" {
				t.Errorf("stderr is not empty under --axi: %q", stderr)
			}
			doc := axiError(t, stdout)
			if msg, _ := doc["message"].(string); !strings.Contains(msg, tc.message) {
				t.Errorf("message %q does not contain %q", msg, tc.message)
			}
			if help := strings.Join(strs(t, doc, "help"), "\n"); !strings.Contains(help, tc.help) {
				t.Errorf("help %q does not mention %q", help, tc.help)
			}
			// The README promises an unknown command lists the commands.
			if tc.name == "unknown command" {
				commands := strs(t, doc, "commands")
				for _, c := range []string{"fetch", "extract-links", "extract-relevant", "setup"} {
					if !contains(commands, c) {
						t.Errorf("commands %v does not list %s", commands, c)
					}
				}
			}
		})
	}
}

// Defends the -o branch of extract-relevant: the error went to the file, not
// stdout, so it is not "already reported" and run() must still print the TOON
// error. Marking it reported would leave stdout holding only the plain summary
// of a failed run, with nothing saying it failed.
func TestAXIExtractRelevantOutputFileFailureStillPrintsError(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "dummy")
	t.Setenv("ANTHROPIC_API_KEY", "")
	out := filepath.Join(t.TempDir(), "out.md")
	code, stdout, stderr := runCLI(t, "extract-relevant", "--path", notFoundPage(t),
		"--context", "c", "-o", out, "--axi")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if stderr != "" {
		t.Errorf("stderr is not empty under --axi: %q", stderr)
	}
	written, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(written), "404") {
		t.Errorf("the output file does not carry the error: %v %q", err, written)
	}
	// The plain -o summary comes first (filed as debt); the TOON error is
	// what follows it.
	i := strings.Index(stdout, "error: true")
	if i < 0 {
		t.Fatalf("no TOON error on stdout:\n%s", stdout)
	}
	doc := axiError(t, stdout[i:])
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "404") {
		t.Errorf("message %q does not carry the 404", msg)
	}
}

// Defends an unknown flag under --axi: it is named, the command's valid flags
// are listed so the caller can correct itself, it exits 2, and it is rejected
// before any dependency is called -- wherever --axi sits relative to the bad
// flag, since pflag stops parsing at the first unknown one.
func TestAXIUnknownFlagListsValidFlagsAndCallsNothing(t *testing.T) {
	url, hits := countingServer(t, http.StatusOK)
	cases := []struct {
		name  string
		args  []string
		bad   string
		flags []string
	}{
		{"fetch, --axi after", []string{"fetch", "--bogus", "--axi", url}, "--bogus", []string{"--output", "--max-bytes", "--json", "--axi"}},
		{"fetch, --axi before", []string{"--axi", "fetch", url, "--bogus"}, "--bogus", []string{"--output", "--timeout"}},
		{"extract-links", []string{"extract-links", "--url", url, "--nope", "--axi"}, "--nope", []string{"--url", "--context", "--axi"}},
		{"unknown shorthand", []string{"extract-relevant", "-Z", "--axi", "--context", "x"}, "-Z", []string{"--path", "--output", "--context"}},
		{"root", []string{"--bogus", "--axi"}, "--bogus", []string{"--axi", "--version"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, tc.args...)
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if stderr != "" {
				t.Errorf("stderr is not empty under --axi: %q", stderr)
			}
			doc := axiError(t, stdout)
			if msg, _ := doc["message"].(string); !strings.Contains(msg, tc.bad) {
				t.Errorf("message %q does not name %s", msg, tc.bad)
			}
			flags := strs(t, doc, "flags")
			for _, f := range tc.flags {
				if !contains(flags, f) {
					t.Errorf("flags %v does not list %s", flags, f)
				}
			}
			if contains(flags, tc.bad) {
				t.Errorf("flags %v lists the unknown flag %s as valid", flags, tc.bad)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("a rejected invocation reached the server %d times", n)
	}
}

// Defends against an error the command already printed in its TOON result
// being printed a second time: two documents on stdout do not decode as one,
// and anything on stderr breaks "stderr is empty under --axi".
func TestAXIReportedResultErrorIsPrintedOnce(t *testing.T) {
	code, stdout, stderr := runCLI(t, "extract-links", "--url", notFoundPage(t), "--axi")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if stderr != "" {
		t.Errorf("stderr is not empty under --axi: %q", stderr)
	}
	v, err := goaxi.Decode(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("stdout is not one TOON document: %v\n%s", err, stdout)
	}
	doc, _ := v.(map[string]any)
	if msg, _ := doc["error"].(string); !strings.Contains(msg, "404") {
		t.Errorf("the result does not carry the 404:\n%s", stdout)
	}
	if strings.Count(stdout, "404") != 1 {
		t.Errorf("the error is printed more than once:\n%s", stdout)
	}
}

// Defends Q1: without --axi an error stays on stderr, as "Error: " and the
// message, with nothing on stdout -- including --axi=false, which is plain.
func TestPlainErrorsStayOnStderr(t *testing.T) {
	url, _ := countingServer(t, http.StatusOK)
	for _, args := range [][]string{
		{"fetch", "--bogus", url},
		{"--axi=false", "fetch", "--bogus", url},
		{"fetch", "not-a-url"},
	} {
		code, stdout, stderr := runCLI(t, args...)
		if code == 0 {
			t.Errorf("%v exited 0", args)
		}
		if stdout != "" {
			t.Errorf("%v wrote to stdout: %q", args, stdout)
		}
		if !strings.HasPrefix(stderr, "Error: ") || strings.Count(stderr, "\n") != 1 {
			t.Errorf("%v: stderr is not one Error line: %q", args, stderr)
		}
	}
}

// Defends the scan for --axi on a failed parse following pflag's own rules: the
// last occurrence wins, a value after = is parsed as a bool, and nothing after
// "--" is a flag.
func TestArgsWantAXI(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--axi"}, true},
		{[]string{"--axi=true"}, true},
		{[]string{"--axi=false"}, false},
		{[]string{"--axi", "--axi=0"}, false},
		{[]string{"--axi=false", "--axi"}, true},
		{[]string{"--axi=nonsense"}, false},
		{[]string{"--", "--axi"}, false},
		{[]string{"-axi"}, false},
	}
	for _, tc := range cases {
		if got := argsWantAXI(tc.args); got != tc.want {
			t.Errorf("argsWantAXI(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
