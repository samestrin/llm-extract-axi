package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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

// TestReadmeConfigurationEnvVarsAreRead defends the Configuration table: every
// environment variable it names must appear as a string literal in a non-test
// .go file, so removing or renaming the variable in code turns this red.
func TestReadmeConfigurationEnvVarsAreRead(t *testing.T) {
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
	root := filepath.Join("..", "..", "..")
	var src strings.Builder
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".planning" || d.Name() == ".paperclip") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			src.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if !strings.Contains(src.String(), `"`+n+`"`) {
			t.Errorf("README documents %s but no non-test .go file reads it", n)
		}
	}
}
