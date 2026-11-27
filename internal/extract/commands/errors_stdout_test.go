package commands

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	goaxi "github.com/samestrin/go-axi"
)

// stdoutFormat is one output format and how to read an error printed in it.
type stdoutFormat struct {
	name string
	flag string // "" is the default, TOON
	// check fails the test unless stdout is one error in this format.
	check func(t *testing.T, stdout string)
}

var stdoutFormats = []stdoutFormat{
	{"toon", "", func(t *testing.T, stdout string) { axiError(t, stdout) }},
	{"toon --axi", "--axi", func(t *testing.T, stdout string) { axiError(t, stdout) }},
	{"json", "--json", func(t *testing.T, stdout string) {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
		}
		if doc["error"] != true {
			t.Errorf("error is %#v, want true:\n%s", doc["error"], stdout)
		}
		if msg, _ := doc["message"].(string); msg == "" {
			t.Errorf("no message:\n%s", stdout)
		}
	}},
	{"plain", "--plain", func(t *testing.T, stdout string) {
		t.Helper()
		if !strings.HasPrefix(stdout, "Error: ") || strings.Count(stdout, "\n") != 1 {
			t.Errorf("stdout is not one plain error line: %q", stdout)
		}
	}},
}

// Defends AXI principle 6: an error is on stdout in the format a success would
// have used, in every format and for every kind of failure, fetch included, so
// a caller reading stdout alone never sees an empty stream after a failure and
// never has to read stderr to learn why. The exit code is not changed by the
// move: 2 for a malformed invocation, 1 for a command that ran and failed.
func TestErrorsGoToStdoutInEveryFormat(t *testing.T) {
	failing, _ := countingServer(t, http.StatusInternalServerError)
	for _, f := range stdoutFormats {
		// around puts the format flag between before and after.
		around := func(before []string, after ...string) []string {
			args := append([]string{}, before...)
			if f.flag != "" {
				args = append(args, f.flag)
			}
			return append(args, after...)
		}
		withFlag := func(args ...string) []string { return around(args) }
		cases := []struct {
			name string
			args []string
			code int
		}{
			// Before the format flag, pflag stops at --bogus and never reads
			// the format flag, so run() has to find it in the arguments.
			{"unknown flag before", withFlag("extract-links", "--bogus"), int(goaxi.ExitUsage)},
			{"unknown flag after", around([]string{"extract-links"}, "--bogus"), int(goaxi.ExitUsage)},
			{"unknown command", withFlag("nosuch"), int(goaxi.ExitUsage)},
			// A bad URL is a rejected value, exit 2 (EST-6950).
			{"bad URL", withFlag("extract-links", "--url", "not-a-url"), int(goaxi.ExitUsage)},
			{"failed fetch", withFlag("fetch", failing, "--retries", "0"), int(goaxi.ExitError)},
			// cobra checks the argument count before the pre-run publishes
			// the format, so the format must come from the flags as given.
			// A missing URL is a malformed invocation, exit 2 (EST-6877).
			{"missing argument", withFlag("fetch"), int(goaxi.ExitUsage)},
		}
		for _, c := range cases {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				code, stdout, stderr := runCLI(t, c.args...)
				if code != c.code {
					t.Errorf("%v exited %d, want %d", c.args, code, c.code)
				}
				if stderr != "" {
					t.Errorf("%v wrote to stderr: %q", c.args, stderr)
				}
				f.check(t, stdout)
			})
		}
	}
	minErrorsInEveryFormat(t)
}

// minFormats are the formats an error under --min is printed in: JSON takes
// the abbreviated keys of output.KeyAbbreviations, as Formatter.PrintError
// does; TOON and plain text are what they are without --min.
var minFormats = []stdoutFormat{
	stdoutFormats[0],
	stdoutFormats[1],
	{"json", "--json", func(t *testing.T, stdout string) {
		t.Helper()
		if strings.Count(stdout, "\n") != 1 {
			t.Errorf("minimal JSON is not one line: %q", stdout)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
		}
		if doc["err"] != true {
			t.Errorf("err is %#v, want true:\n%s", doc["err"], stdout)
		}
		if msg, _ := doc["msg"].(string); msg == "" {
			t.Errorf("no msg:\n%s", stdout)
		}
		for _, k := range []string{"error", "message", "n"} {
			if _, ok := doc[k]; ok {
				t.Errorf("minimal JSON has the key %q:\n%s", k, stdout)
			}
		}
	}},
	stdoutFormats[3],
}

// Defends the minimal error shape: under --json --min a command's own result
// came out abbreviated, but an error printed by run() kept the full
// {error, message} keys, so a --min caller had to read two key sets. The rows
// cover both ways run() learns the format: scanning the arguments on a usage
// error (exit 2) and reading the parsed flags on a failure (exit 1).
func minErrorsInEveryFormat(t *testing.T) {
	noAPIKey(t)
	for _, f := range minFormats {
		flags := []string{"--min"}
		if f.flag != "" {
			flags = append(flags, f.flag)
		}
		with := func(args ...string) []string { return append(args, flags...) }
		// pflag stops at --bogus, so --min after it is found only by the scan.
		cases := []struct {
			name string
			args []string
			code int
		}{
			{"extract-links unknown flag before", with("extract-links", "--bogus"), int(goaxi.ExitUsage)},
			{"extract-links unknown flag after", append(append([]string{"extract-links"}, flags...), "--bogus"), int(goaxi.ExitUsage)},
			{"extract-links bad URL", with("extract-links", "--url", "not-a-url"), int(goaxi.ExitUsage)},
			{"extract-relevant unknown flag before", with("extract-relevant", "--bogus"), int(goaxi.ExitUsage)},
			{"extract-relevant no API key", with("extract-relevant", "--path", ".", "--context", "x"), int(goaxi.ExitUsage)},
		}
		for _, c := range cases {
			t.Run("min "+f.name+"/"+c.name, func(t *testing.T) {
				code, stdout, stderr := runCLI(t, c.args...)
				if code != c.code {
					t.Errorf("%v exited %d, want %d", c.args, code, c.code)
				}
				if stderr != "" {
					t.Errorf("%v wrote to stderr: %q", c.args, stderr)
				}
				f.check(t, stdout)
			})
		}
		t.Run("min "+f.name+"/extract-relevant missing path", func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "dummy")
			args := with("extract-relevant", "--path", filepath.Join(t.TempDir(), "absent"), "--context", "x")
			code, stdout, stderr := runCLI(t, args...)
			if code != int(goaxi.ExitError) {
				t.Errorf("%v exited %d, want %d", args, code, goaxi.ExitError)
			}
			if stderr != "" {
				t.Errorf("%v wrote to stderr: %q", args, stderr)
			}
			f.check(t, stdout)
		})
	}
}
