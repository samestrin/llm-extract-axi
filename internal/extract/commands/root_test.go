package commands

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samestrin/llm-extract-axi/pkg/output"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Defends the exit-code contract: an unknown command must exit non-zero and
// say why on stderr, never exit 0 with nothing printed.
func TestRunUnknownCommandFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"no-such-command"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("unknown command exited 0; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "no-such-command") {
		t.Errorf("stderr does not name the bad command: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("an error wrote to stdout: %q", stdout.String())
	}
}

// Defends the AXI split between a malformed invocation (exit 2: retrying it
// unchanged never helps) and a command that ran and failed (exit 1: retrying
// may). A flat 1 for both leaves a caller unable to tell a typo from an outage.
func TestRunExitCodeSeparatesUsageFromFailure(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()
	t.Setenv("FETCH_MAX_RETRIES", "0")
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"unknown root flag", []string{"--bogus"}, 2},
		{"unknown command", []string{"no-such-command"}, 2},
		{"unknown subcommand flag", []string{"fetch", "--bogus", down.URL}, 2},
		{"failed fetch", []string{"fetch", down.URL}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, &stdout, &stderr); code != tc.want {
				t.Fatalf("exit %d, want %d; stderr=%q", code, tc.want, stderr.String())
			}
			if !strings.HasPrefix(stderr.String(), "Error: ") {
				t.Errorf("stderr does not carry the error: %q", stderr.String())
			}
		})
	}
}

// Defends --version reading commands.Version, the variable the Makefile
// stamps with -X. Stamping anything else leaves the binary reporting the
// default.
func TestRunVersionReportsVersionVariable(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "v9.9.9-test"

	// The v of a git describe stamp is stripped; --axi alongside takes the
	// cobra path rather than the lone-flag shortcut.
	for _, args := range [][]string{{"-v"}, {"-V"}, {"--version"}, {"--axi", "--version"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v exited %d; stderr=%q", args, code, stderr.String())
		}
		if stdout.String() != "9.9.9-test\n" {
			t.Errorf("%v printed %q, want the bare version", args, stdout.String())
		}
	}
}

// Defends against a second in-process run inheriting the first run's
// arguments: the root is a package singleton. A bare run after --version must
// print the home view, not the version again.
func TestRunNoArgsDoesNotInheritPreviousArgs(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "v9.9.9-test"

	var stdout, stderr bytes.Buffer
	run([]string{"--version"}, &stdout, &stderr)
	stdout.Reset()
	stderr.Reset()
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("no args exited %d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "extract-relevant") || strings.Contains(stdout.String(), "v9.9.9") {
		t.Errorf("no args did not print the home view: %q", stdout.String())
	}
}

// Defends AXI content first: a bare run must show where the binary lives
// (home shortened to ~), what the tool does in one sentence, and which
// commands reach the network or a model, instead of a wall of help. It must
// also reach neither: the model and fetch endpoints point at a server that
// fails the test if it is called.
func TestRunNoArgsPrintsHomeView(t *testing.T) {
	hit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a bare run made a request: %s %s", r.Method, r.URL)
	}))
	defer hit.Close()
	t.Setenv("OPENAI_BASE_URL", hit.URL)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("HTTP_PROXY", hit.URL)
	t.Setenv("HTTPS_PROXY", hit.URL)

	home := t.TempDir()
	t.Setenv("HOME", home)
	oldExe := executable
	t.Cleanup(func() { executable = oldExe })
	executable = func() (string, error) { return filepath.Join(home, "bin", "llm-extract"), nil }

	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("no args exited %d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "~/bin/llm-extract") || strings.Contains(out, home) {
		t.Errorf("home view does not show the executable path with ~ for home: %q", out)
	}
	if !strings.Contains(out, "Fetch outside content and reduce it to what an agent needs.") {
		t.Errorf("home view lacks the one-sentence description: %q", out)
	}
	for _, want := range []string{"network", "model", "fetch", "extract-links", "extract-relevant", "only with --context", "only when --path is a URL"} {
		if !strings.Contains(out, want) {
			t.Errorf("home view's network/model table lacks %q: %q", want, out)
		}
	}
	if strings.Contains(out, "Usage:") {
		t.Errorf("a bare run printed help instead of the home view: %q", out)
	}
	if stderr.Len() != 0 {
		t.Errorf("a bare run wrote to stderr: %q", stderr.String())
	}
}

// Defends the path shortening: only a path under home gets ~, and a sibling
// directory that merely shares home's prefix (/home/sam2 under /home/sam)
// must not be rewritten.
func TestTildeHome(t *testing.T) {
	cases := []struct{ path, home, want string }{
		{"/home/sam/bin/x", "/home/sam", "~/bin/x"},
		{"/home/sam", "/home/sam", "~"},
		{"/home/sam2/bin/x", "/home/sam", "/home/sam2/bin/x"},
		{"/usr/local/bin/x", "/home/sam", "/usr/local/bin/x"},
		{"/usr/local/bin/x", "", "/usr/local/bin/x"},
	}
	for _, tc := range cases {
		if got := tildeHome(tc.path, tc.home); got != tc.want {
			t.Errorf("tildeHome(%q, %q) = %q, want %q", tc.path, tc.home, got, tc.want)
		}
	}
}

// Defends the home view when the executable cannot be located: it says so
// rather than printing an empty path, and the rest of the view still prints.
func TestRunNoArgsUnknownExecutable(t *testing.T) {
	oldExe := executable
	t.Cleanup(func() { executable = oldExe })
	executable = func() (string, error) { return "", errors.New("no /proc") }

	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("no args exited %d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "unknown (no /proc)") || !strings.Contains(stdout.String(), "extract-relevant") {
		t.Errorf("home view does not report the unknown path: %q", stdout.String())
	}
}

// Defends --help against the home view: a bare run changed, an explicit
// request for help did not.
func TestRunHelpStillPrintsHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v exited %d; stderr=%q", args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Usage:") || !strings.Contains(stdout.String(), "Available Commands:") {
			t.Errorf("%v did not print help: %q", args, stdout.String())
		}
	}
}

// Defends resetFlags against the pflag slice trap: Set on a slice that has
// already been set appends, so a naive reset would leave the first run's
// values in place and add the default to them.
func TestResetFlagsRestoresDefaults(t *testing.T) {
	c := &cobra.Command{Use: "x"}
	sub := &cobra.Command{Use: "sub"}
	c.AddCommand(sub)
	s := c.Flags().String("name", "def", "")
	sl := sub.Flags().StringSlice("tags", []string{"a", "b"}, "")
	empty := c.PersistentFlags().StringSlice("none", nil, "")

	if err := c.Flags().Set("name", "changed"); err != nil {
		t.Fatal(err)
	}
	if err := sub.Flags().Set("tags", "x,y"); err != nil {
		t.Fatal(err)
	}
	if err := c.PersistentFlags().Set("none", "z"); err != nil {
		t.Fatal(err)
	}

	resetFlags(c)

	if *s != "def" {
		t.Errorf("string flag = %q, want def", *s)
	}
	if got := strings.Join(*sl, ","); got != "a,b" {
		t.Errorf("slice flag = %q, want a,b", got)
	}
	if len(*empty) != 0 {
		t.Errorf("empty slice flag = %v, want empty", *empty)
	}
	for _, f := range []*pflag.Flag{c.Flags().Lookup("name"), sub.Flags().Lookup("tags"), c.PersistentFlags().Lookup("none")} {
		if f.Changed {
			t.Errorf("flag %s still marked Changed", f.Name)
		}
	}
}

// Defends --axi on the root reaching the lifted commands, and not leaking into
// the next in-process run. In llm-data the flag was published only when true,
// so a run with --axi followed by one with --json emitted TOON for both.
func TestRunAXIFlagReachesCommandsAndResets(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><a href="/docs">Documentation</a></body></html>`))
	}))
	defer page.Close()
	t.Cleanup(func() { output.SetDefaultAXI(false) })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"extract-links", "--url", page.URL, "--json", "--axi"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--axi run exited %d; stderr=%q", code, stderr.String())
	}
	axi := strings.TrimSpace(stdout.String())
	if strings.HasPrefix(axi, "{") || !strings.Contains(axi, "/docs") {
		t.Errorf("--axi did not produce TOON carrying the link:\n%s", axi)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"extract-links", "--url", page.URL, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--json run exited %d; stderr=%q", code, stderr.String())
	}
	if js := strings.TrimSpace(stdout.String()); !strings.HasPrefix(js, "{") {
		t.Errorf("the previous run's --axi leaked into this one; not JSON:\n%s", js)
	}
}
