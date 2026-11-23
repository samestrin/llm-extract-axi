package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// outputModes is every encoding selection a caller can make. The order is the
// order of the columns in TestOutputModeMatrix.
var outputModes = []struct {
	name string
	args []string
}{
	{"default", nil},
	{"--plain", []string{"--plain"}},
	{"--json", []string{"--json"}},
	{"--axi", []string{"--axi"}},
	{"--axi=false", []string{"--axi=false"}},
	{"--min", []string{"--min"}},
	{"--plain --json", []string{"--plain", "--json"}},
}

// modeCheck asserts one command's output under one mode.
type modeCheck func(t *testing.T, code int, stdout, stderr string)

// isTOONWithHelp: a TOON document on stdout that carries a help[] block, and
// not JSON, which would mean --json leaked into the default.
func isTOONWithHelp(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if json.Valid([]byte(stdout)) {
		t.Errorf("stdout is JSON, want TOON:\n%s", stdout)
	}
	if !strings.HasPrefix(stdout, "help[") && !strings.Contains(stdout, "\nhelp[") {
		t.Errorf("TOON carries no help[] block:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

// isJSONWithoutHelp: --json as before, one JSON document and no help[].
func isJSONWithoutHelp(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if _, ok := doc["help"]; ok {
		t.Errorf("--json carries help[]: %s", stdout)
	}
}

// isText: today's prose, recognised by a line it always prints, with no TOON
// help[] block in it.
func isText(marker string) modeCheck {
	return func(t *testing.T, code int, stdout, stderr string) {
		t.Helper()
		if code != 0 {
			t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, marker) {
			t.Errorf("text output lacks %q:\n%s", marker, stdout)
		}
		if strings.Contains(stdout, "help[") || json.Valid([]byte(stdout)) {
			t.Errorf("--plain printed a structured document:\n%s", stdout)
		}
	}
}

// isExactly: the bytes and nothing else: fetch and setup under --plain, and
// completion, which no encoding flag changes.
func isExactly(want *string) modeCheck {
	return func(t *testing.T, code int, stdout, stderr string) {
		t.Helper()
		if code != 0 {
			t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
		}
		if stdout != *want {
			t.Errorf("stdout differs from the reference output:\n got %q\nwant %q", stdout, *want)
		}
	}
}

// isUsage: a malformed invocation, exit 2.
func isUsage(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	if code != 2 {
		t.Errorf("exit %d, want 2; stdout=%q stderr=%q", code, stdout, stderr)
	}
}

// isConflict: --plain with --json is a usage error that names both flags.
func isConflict(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	isUsage(t, code, stdout, stderr)
	msg := stdout + stderr
	if !strings.Contains(msg, "--plain") || !strings.Contains(msg, "--json") {
		t.Errorf("the conflict error does not name --plain and --json: stdout=%q stderr=%q", stdout, stderr)
	}
}

// Defends the epic 09 default: TOON with help[] when no flag is given, today's
// text behind --plain (and --axi=false), --json and --min as before, --axi an
// accepted no-op, and --plain with --json refused. setup prints TOON, JSON or
// its --plain lines like the others; fetch prints its envelope in TOON or JSON and the
// page bytes under --plain (epic 10 reversed epic 09's byte-identical pin). Flipping the default back, or letting
// --json lose to it, turns a row of this red. completion's row is
// TestCLICompletionIgnoresTheOutputMode in tests/e2e: cobra binds that
// command to the first run's stdout, so only one in-process run sees it.
func TestOutputModeMatrix(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	homeEnv(t)

	const page = "<html><body>raw <b>page</b> body\n</body></html>\n"
	fetchURL := pageServer(t, 200, page).URL
	linksURL := linksPage(t)

	srv := fakeLLM(t, func(string) string { return "the relevant passage" })
	pointExtractRelevantAt(t, srv)
	doc := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(doc, []byte("a document with a relevant passage"), 0o644); err != nil {
		t.Fatal(err)
	}
	setupDir := t.TempDir()

	pageBody := page
	code, setupRef, stderr := runCLI(t, "setup", "--dry-run", "--dir", setupDir, "--plain")
	if code != 0 || setupRef == "" {
		t.Fatalf("setup --plain: exit %d, stdout %q, stderr %q", code, setupRef, stderr)
	}

	type row struct {
		name string
		args []string
		// want holds one check per entry of outputModes, in order.
		want [7]modeCheck
	}
	rows := []row{
		{"home", nil, [7]modeCheck{
			isTOONWithHelp, isText("Run 'llm-extract --help' for flags"), isUsage, isTOONWithHelp,
			isText("Run 'llm-extract --help' for flags"), isUsage, isUsage,
		}},
		{"fetch", []string{"fetch", fetchURL}, [7]modeCheck{
			isFetchTOON(page), isExactly(&pageBody), isFetchJSON(page), isFetchTOON(page),
			isExactly(&pageBody), isUsage, isConflict,
		}},
		{"extract-links", []string{"extract-links", "--url", linksURL}, [7]modeCheck{
			isTOONWithHelp, isText("Total Links: 2"), isJSONWithoutHelp, isTOONWithHelp,
			isText("Total Links: 2"), isTOONWithHelp, isConflict,
		}},
		{"extract-relevant", []string{"extract-relevant", "--path", doc, "--context", "c"}, [7]modeCheck{
			isTOONWithHelp, isText("the relevant passage"), isJSONWithoutHelp, isTOONWithHelp,
			isText("the relevant passage"), isTOONWithHelp, isConflict,
		}},
		// setup prints a TOON result with help[] since EST-8259 item 9; its
		// one-line-per-file text moved behind --plain.
		{"setup", []string{"setup", "--dry-run", "--dir", setupDir}, [7]modeCheck{
			isTOONWithHelp, isExactly(&setupRef), isJSONWithoutHelp, isTOONWithHelp,
			isExactly(&setupRef), isUsage, isConflict,
		}},
	}
	for _, r := range rows {
		for i, m := range outputModes {
			t.Run(r.name+"/"+m.name, func(t *testing.T) {
				args := append(append([]string{}, r.args...), m.args...)
				code, stdout, stderr := runCLI(t, args...)
				r.want[i](t, code, stdout, stderr)
			})
		}
	}
}

// isFetchJSON: fetch --json, the JSON envelope with the page as body.
func isFetchJSON(page string) modeCheck {
	return func(t *testing.T, code int, stdout, stderr string) {
		t.Helper()
		if code != 0 {
			t.Fatalf("exit %d; stderr=%q", code, stderr)
		}
		var got FetchResult
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("fetch --json is not JSON: %v\n%s", err, stdout)
		}
		if got.Body == nil || *got.Body != page || got.Truncated {
			t.Errorf("body = %v truncated = %v, want %q whole", got.Body, got.Truncated, page)
		}
	}
}

// isFetchTOON: fetch's default, a TOON envelope with a short page whole in
// body, not the raw page.
func isFetchTOON(page string) modeCheck {
	return func(t *testing.T, code int, stdout, stderr string) {
		t.Helper()
		if code != 0 {
			t.Fatalf("exit %d; stderr=%q", code, stderr)
		}
		doc := decodeFetchDoc(t, stdout, false)
		if doc["body"] != page || doc["truncated"] != false {
			t.Errorf("envelope = %v, want body %q whole", doc, page)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
	}
}

// Defends the flag rules around the matrix: --axi=false is --plain, so with
// --json it is the same usage error; --axi with --json is plain --json, since
// --axi selects nothing the default does not; and --json=false leaves --plain
// alone rather than tripping the conflict on a flag that is off.
func TestOutputFlagCombinations(t *testing.T) {
	url := linksPage(t)
	cases := []struct {
		args  []string
		check modeCheck
	}{
		{[]string{"--axi=false", "--json"}, isConflict},
		{[]string{"--json", "--axi=false"}, isConflict},
		{[]string{"--axi", "--json"}, isJSONWithoutHelp},
		{[]string{"--plain", "--json=false"}, isText("Total Links: 2")},
		{[]string{"--axi", "--plain"}, isText("Total Links: 2")},
		{[]string{"--plain", "--min"}, isText("Total Links: 2")},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			code, stdout, stderr := runCLI(t, append([]string{"extract-links", "--url", url}, c.args...)...)
			c.check(t, code, stdout, stderr)
		})
	}
}
