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
		// A missing key is a configuration error the caller must fix before a
		// retry can succeed, so it is a usage error (exit 2), same as an
		// Anthropic key with no OPENAI_BASE_URL (EST-6628 F3).
		{"no API key", []string{"extract-relevant", "--path", ".", "--context", "x", "--axi"}, 2, "API key", "OPENAI_API_KEY"},
		{"fetch bad URL", []string{"fetch", "not-a-url", "--axi"}, 2, "invalid URL", "http://"},
		{"extract-links bad URL", []string{"extract-links", "--url", "ftp://x", "--axi"}, 2, "invalid URL", "http://"},
		{"failed fetch", []string{"fetch", down, "--axi"}, 1, "500", "fetch --help"},
		{"failed fetch, default", []string{"fetch", down}, 1, "500", "fetch --help"},
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
// error. Marking it reported would leave stdout holding nothing that says the
// run failed. It also defends the whole stream: the plain -o summary used to
// print before the error document, so stdout was prose followed by TOON and
// decoded as neither (release gate F2).
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
	noProseSummary(t, stdout)
	// The whole stream, not a slice of it: one decodable TOON document.
	doc := axiError(t, stdout)
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "404") {
		t.Errorf("message %q does not carry the 404", msg)
	}
	// Two concatenated TOON documents decode without error (the last wins), so
	// decoding alone does not prove there is only one.
	if n := strings.Count(stdout, "error: true"); n != 1 {
		t.Errorf("stdout holds %d error documents, want 1:\n%s", n, stdout)
	}
	if n := strings.Count(stdout, "404"); n != 1 {
		t.Errorf("the error is printed %d times, want 1:\n%s", n, stdout)
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

// Defends AXI principle 6 outside TOON: under --plain (and --axi=false, which
// is --plain) an error is one "Error: " line on stdout, and under --json it is
// one {error, message} JSON document on stdout; stderr stays empty either way.
func TestPlainAndJSONErrorsGoToStdout(t *testing.T) {
	url, _ := countingServer(t, http.StatusOK)
	for _, tc := range []struct {
		args []string
		json bool
	}{
		{[]string{"--plain", "fetch", "--bogus", url}, false},
		{[]string{"--axi=false", "fetch", "--bogus", url}, false},
		{[]string{"fetch", "--plain", "not-a-url"}, false},
		{[]string{"fetch", "--json", "--bogus", url}, true},
		{[]string{"fetch", "--json", "not-a-url"}, true},
		{[]string{"fetch", "--axi", "--json", "not-a-url"}, true},
	} {
		code, stdout, stderr := runCLI(t, tc.args...)
		if code == 0 {
			t.Errorf("%v exited 0", tc.args)
		}
		if stderr != "" {
			t.Errorf("%v wrote to stderr: %q", tc.args, stderr)
		}
		if !fetchError(stdout, stderr) || strings.HasPrefix(stdout, "{") != tc.json {
			t.Errorf("%v: stdout is not one error in the asked format: %q", tc.args, stdout)
		}
	}
}

// Defends the scan for the format flags on a failed parse following pflag's
// own rules and the pre-run's: TOON unless --plain, --axi=false or --json; the
// last occurrence of each flag wins, a value after = is parsed as a bool (an
// unparsable one changes nothing), and nothing after "--" is a flag. --plain
// beats --json, so asking for both gets its usage error as text.
func TestArgsFormat(t *testing.T) {
	cases := []struct {
		args []string
		want outFormat
	}{
		{nil, formatTOON},
		{[]string{"--axi"}, formatTOON},
		{[]string{"--axi=true"}, formatTOON},
		{[]string{"--axi=false"}, formatPlain},
		{[]string{"--axi", "--axi=0"}, formatPlain},
		{[]string{"--axi=false", "--axi"}, formatTOON},
		{[]string{"--axi=nonsense"}, formatTOON},
		{[]string{"--", "--axi=false"}, formatTOON},
		{[]string{"-axi"}, formatTOON},
		{[]string{"--plain"}, formatPlain},
		{[]string{"--plain=true"}, formatPlain},
		{[]string{"--plain", "--plain=false"}, formatTOON},
		{[]string{"--plain=nonsense"}, formatTOON},
		{[]string{"--axi", "--plain"}, formatPlain},
		{[]string{"--plain", "--axi"}, formatPlain},
		{[]string{"--json"}, formatJSON},
		{[]string{"--json=true"}, formatJSON},
		{[]string{"--json", "--json=false"}, formatTOON},
		{[]string{"--axi", "--json"}, formatJSON},
		{[]string{"--json", "--plain"}, formatPlain},
		{[]string{"--plain", "--json"}, formatPlain},
		{[]string{"--axi=false", "--json"}, formatPlain},
		{[]string{"--", "--json"}, formatTOON},
		{[]string{"--", "--plain"}, formatTOON},
	}
	for _, tc := range cases {
		if got := argsFormat(RootCmd, tc.args); got != tc.want {
			t.Errorf("argsFormat(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
