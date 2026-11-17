package commands

import (
	"encoding/json"
	"net/http"
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
			{"runtime error", withFlag("extract-links", "--url", "not-a-url"), int(goaxi.ExitError)},
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
}
