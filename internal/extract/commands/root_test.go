package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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

// Defends --version reading commands.Version, the variable the Makefile
// stamps with -X. Stamping anything else leaves the binary reporting the
// default.
func TestRunVersionReportsVersionVariable(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "v9.9.9-test"

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--version exited %d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "v9.9.9-test") {
		t.Errorf("--version did not print the stamped version: %q", stdout.String())
	}
}

// Defends against a second in-process run inheriting the first run's
// arguments: the root is a package singleton.
func TestRunNoArgsPrintsHelp(t *testing.T) {
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
	if !strings.Contains(stdout.String(), "Usage:") || strings.Contains(stdout.String(), "v9.9.9") {
		t.Errorf("no args did not print help: %q", stdout.String())
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
