package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests defend the finding of the third v1.1.0 release gate.

// pasteHref is a link a page controls that, if any of it reached a pasted
// command, would create the file pwned in the working directory.
const pasteHref = "/safe?q=';touch pwned;'"

// renderedHelpCommand returns the command of the help[] line that starts with
// prefix, cut from the raw --axi bytes as a user copies it off the terminal:
// TOON escaping and all, not the decoded string.
func renderedHelpCommand(t *testing.T, raw, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "help[") {
			continue
		}
		_, rest, ok := strings.Cut(line, prefix)
		if !ok {
			t.Fatalf("help line lacks %q: %s", prefix, line)
		}
		return strings.TrimSuffix(rest, `"`)
	}
	t.Fatalf("no help[] line in:\n%s", raw)
	return ""
}

// Defends help-paste-injection: the decoded help[] string was quoted, but the
// TOON encoder doubles the backslash in '\” on the terminal, so a user who
// pasted the suggested command into a shell ran the page's injected command.
// The rendered line must carry no page-controlled text, and pasting it must
// run nothing.
func TestExtractLinksAXIHelpPastedFromTerminalRunsNothing(t *testing.T) {
	page := helpPageServer(t, `<html><body><h1><a href="`+pasteHref+`">x</a></h1></body></html>`)
	withAXI(t)
	raw := linksOut(t, "--url", page.URL)
	cmdline := renderedHelpCommand(t, raw, "Read the top link: ")
	for _, bad := range []string{"safe", "touch", "pwned", page.URL} {
		if strings.Contains(cmdline, bad) {
			t.Errorf("rendered help command carries page text %q: %s", bad, cmdline)
		}
	}
	dir := t.TempDir()
	cmdline = strings.Replace(cmdline, "llm-extract ", "llm_extract ", 1)
	sh := exec.Command("sh", "-c", `llm_extract() { :; }; `+cmdline)
	sh.Dir = dir
	_ = sh.Run() // the placeholder is not a real argument; only side effects matter
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Errorf("pasting the rendered help command ran the page's command: %s", cmdline)
	}
}
