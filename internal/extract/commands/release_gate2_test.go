package commands

import (
	"encoding/json"
	"strings"
	"testing"

	goaxi "github.com/samestrin/go-axi"
)

// These tests defend the four findings of the second v1.1.0 release gate.

// cmdInjectHref is a link a page controls that is harmless in a POSIX shell
// once single-quoted, but runs whoami when pasted into cmd.exe, where single
// quotes do not quote and & separates commands.
const cmdInjectHref = "https://example.com/?a=1&whoami&rem"

// helpFor sets the platform the help[] commands are written for, for one test.
func helpFor(t *testing.T, goos string) {
	t.Helper()
	orig := helpGOOS
	t.Cleanup(func() { helpGOOS = orig })
	helpGOOS = goos
}

// allHelpLines returns every help[] line that suggests a command on target,
// across extract-links (list and empty result) and extract-relevant (path and
// URL, empty and non-empty), with the prefix each line starts with.
func allHelpLines(target string) map[string]string {
	return map[string]string{
		extractLinksHelp(ExtractLinksResult{URL: "https://example.com", Links: []LinkInfo{{Href: target}}})[0]: "Read the top link: ",
		extractLinksHelp(ExtractLinksResult{URL: target})[0]:                                                   "Read the page text instead: ",
		extractRelevantHelp(ExtractRelevantResult{URL: target})[0]:                                             "Broaden the context: ",
		extractRelevantHelp(ExtractRelevantResult{URL: target, ExtractedParts: []string{"x"}})[0]:              "Narrow and save it: ",
		extractRelevantHelp(ExtractRelevantResult{Path: target})[0]:                                            "Broaden the context: ",
		extractRelevantHelp(ExtractRelevantResult{Path: target, ExtractedParts: []string{"x"}})[0]:             "Narrow and save it: ",
	}
}

// Defends shell-quote-windows-injection on POSIX: the link that injects in
// cmd.exe is read by sh as one argument, so the POSIX quoting holds for it.
func TestHelpCommandPOSIXQuotesCmdInjectLink(t *testing.T) {
	helpFor(t, "linux")
	for line, prefix := range allHelpLines(cmdInjectHref) {
		got := runHelpCommand(t, strings.TrimSuffix(line, " -o <file>"), prefix)
		if len(got) != 5 || got[2] != cmdInjectHref {
			t.Errorf("%q: sh read %q", line, got)
		}
	}
}

// Defends shell-quote-windows-injection on Windows: no quoting is safe in
// both cmd.exe and PowerShell, so a help line built on Windows must carry no
// part of the page-controlled value at all -- pasting the whole line must not
// run whoami -- and must name the result field that holds it instead.
func TestHelpCommandWindowsLeavesUntrustedValueOut(t *testing.T) {
	helpFor(t, "windows")
	for line := range allHelpLines(cmdInjectHref) {
		for _, bad := range []string{"whoami", "example.com", "&", "%", "|", "^", `"`} {
			if strings.Contains(line, bad) {
				t.Errorf("windows help line carries %q: %s", bad, line)
			}
		}
		if !strings.Contains(line, "--path <") {
			t.Errorf("windows help line does not name the field to use: %s", line)
		}
	}
	links := extractLinksHelp(ExtractLinksResult{URL: "https://example.com", Links: []LinkInfo{{Href: cmdInjectHref}}})[0]
	if want := "Read the top link: llm-extract extract-relevant --path <links[0].href> --context '<what you need>'"; links != want {
		t.Errorf("windows top-link line:\n got %s\nwant %s", links, want)
	}
	page := extractLinksHelp(ExtractLinksResult{URL: cmdInjectHref})[0]
	if !strings.Contains(page, "--path <url>") {
		t.Errorf("windows empty-page line does not name url: %s", page)
	}
	if p := extractRelevantHelp(ExtractRelevantResult{Path: `C:\docs\a&b.md`})[0]; !strings.Contains(p, "--path <path>") {
		t.Errorf("windows path line does not name path: %s", p)
	}
	if u := extractRelevantHelp(ExtractRelevantResult{URL: cmdInjectHref})[0]; !strings.Contains(u, "--path <url>") {
		t.Errorf("windows url line does not name url: %s", u)
	}
}

// Defends axi-last-occurrence-bypass: on a usage error the last --axi wins,
// even when pflag had already read an earlier one before it stopped.
func TestUsageErrorHonoursLastAXIOccurrence(t *testing.T) {
	axiOffAfter(t)
	code, stdout, stderr := runCLI(t, "extract-links", "--axi", "--bogus", "--axi=false")
	if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "Error: ") {
		t.Errorf("--axi --bogus --axi=false: want the plain error, got exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI(t, "extract-links", "--axi=false", "--bogus", "--axi")
	if code != 2 || stderr != "" {
		t.Errorf("--axi=false --bogus --axi: exit %d stderr %q", code, stderr)
	}
	axiError(t, stdout)
	// A run error after a clean parse still follows the parsed flag.
	code, stdout, stderr = runCLI(t, "extract-links", "--url", "not-a-url", "--axi")
	if code == 0 || stderr != "" {
		t.Errorf("bad URL under --axi: exit %d stderr %q", code, stderr)
	}
	axiError(t, stdout)
}

// Defends truncation-note-lost-on-ranking-error: a ranking that failed after
// its input was cut still says the input was cut, in every mode, and says the
// links kept their page-position score. A failure before any prompt was sent
// claims no cut.
func TestExtractLinksRankingErrorKeepsTruncationNote(t *testing.T) {
	axiOffAfter(t)
	for name, setup := range map[string]func(){
		"http 500":  func() { failingLLM(t, 500) },
		"malformed": func() { pointExtractRelevantAt(t, fakeLLM(t, func(string) string { return "not json" })) },
	} {
		setup()
		code, out, _ := runCLI(t, "extract-links", "--url", manyLinksPage(t, 120), "--context", "c", "--json")
		var r ExtractLinksResult
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("%s: %v\n%.400s", name, err, out)
		}
		if code != 1 || r.Error == "" || len(r.Links) != 120 {
			t.Fatalf("%s: want a partial result with all 120 links, got exit %d error %q links %d", name, code, r.Error, len(r.Links))
		}
		for _, s := range []string{"100 of 120", "25000 bytes", "page-position score"} {
			if !strings.Contains(r.Truncated, s) {
				t.Errorf("%s: --json truncation note %q lacks %q", name, r.Truncated, s)
			}
		}
		if strings.Contains(r.Truncated, "not in the result") {
			t.Errorf("%s: note claims links were dropped, but all are kept: %q", name, r.Truncated)
		}
		_, out, _ = runCLI(t, "extract-links", "--url", manyLinksPage(t, 120), "--context", "c", "--json", "--min")
		if !strings.Contains(out, `"tr":`) {
			t.Errorf("%s: --min lacks the truncation note", name)
		}
		_, out, _ = runCLI(t, "extract-links", "--url", manyLinksPage(t, 120), "--context", "c", "--axi")
		doc, err := goaxi.Decode(strings.NewReader(out))
		if err != nil {
			t.Fatalf("%s: --axi not TOON: %v", name, err)
		}
		if s, _ := doc.(map[string]any)["truncated"].(string); !strings.Contains(s, "25000 bytes") {
			t.Errorf("%s: --axi lacks the truncation note", name)
		}
		_, out, _ = runCLI(t, "extract-links", "--url", manyLinksPage(t, 120), "--context", "c")
		if !strings.Contains(out, "Truncated: ") {
			t.Errorf("%s: text lacks the truncation note", name)
		}
	}
	// No API key: no prompt was built, so nothing was cut.
	noAPIKey(t)
	_, out, _ := runCLI(t, "extract-links", "--url", manyLinksPage(t, 120), "--context", "c", "--json")
	if strings.Contains(out, "truncated") {
		t.Errorf("a ranking that sent nothing claims truncation")
	}
}
