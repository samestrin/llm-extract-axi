package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// docs/axi-audit.md is admission evidence for the axi.md catalog. Its
// file:line pointers went stale once already: the v1.1.0 release-gate fixes
// moved lines in extract_links.go and extract_relevant.go, and the audit kept
// pointing at the old ones. These tests make a moved line fail here instead of
// at the next release gate.

const (
	auditPath        = "docs/axi-audit.md"
	catalogEntryPath = "docs/axi-catalog-entry.yaml"
)

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// auditSourcePath resolves a pointer's file: a bare name is in this package,
// anything with a slash is relative to the repository root.
func auditSourcePath(name string) string {
	if strings.Contains(name, "/") {
		return name
	}
	return filepath.Join("internal", "extract", "commands", name)
}

var (
	// `symbol` (`file.go:12`) or `symbol` (`file.go:12-14`).
	auditPointerRe = regexp.MustCompile("`([^`]+)` \\(`([\\w./]+\\.go):(\\d+)(?:-(\\d+))?`\\)")
	// Any file.go:N in backticks, paired with a symbol or not.
	auditAnyLineRe = regexp.MustCompile("`[\\w./]+\\.go:\\d+(?:-\\d+)?`")
)

// Defends the audit's evidence against moved code: every file:line names the
// symbol it points at, and that symbol is on one of the cited lines.
func TestAXIAuditPointersLandOnTheirSymbols(t *testing.T) {
	audit := readRepoFile(t, auditPath)
	pointers := auditPointerRe.FindAllStringSubmatch(audit, -1)
	if all := auditAnyLineRe.FindAllString(audit, -1); len(all) != len(pointers) || len(pointers) == 0 {
		t.Fatalf("%s has %d file:line pointers but %d of them name their symbol as `symbol` (`file:line`)", auditPath, len(all), len(pointers))
	}
	for _, p := range pointers {
		symbol, file := p[1], p[2]
		first, _ := strconv.Atoi(p[3])
		last := first
		if p[4] != "" {
			last, _ = strconv.Atoi(p[4])
		}
		lines := strings.Split(readRepoFile(t, auditSourcePath(file)), "\n")
		if first < 1 || last < first || last > len(lines) {
			t.Errorf("%s: %s:%d-%d is outside the file (%d lines)", p[0], file, first, last, len(lines))
			continue
		}
		if !strings.Contains(strings.Join(lines[first-1:last], "\n"), symbol) {
			t.Errorf("%s: %q is not on %s:%d-%d", auditPath, symbol, file, first, last)
		}
	}
}

// Defends the audit's named tests: a renamed or deleted test leaves the claim
// it backed with no evidence.
func TestAXIAuditNamedTestsExist(t *testing.T) {
	audit := readRepoFile(t, auditPath)
	var src strings.Builder
	for _, dir := range []string{"internal", "pkg", "cmd", "tests"} {
		err := filepath.WalkDir(filepath.Join("..", "..", "..", dir), func(path string, d os.DirEntry, err error) error {
			if err == nil && strings.HasSuffix(path, "_test.go") {
				b, readErr := os.ReadFile(path)
				src.Write(b)
				return readErr
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	names := regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(audit, -1)
	if len(names) == 0 {
		t.Fatalf("%s names no tests", auditPath)
	}
	for _, n := range names {
		if !strings.Contains(src.String(), "func "+n[1]+"(") {
			t.Errorf("%s names %s, which no test file defines", auditPath, n[1])
		}
	}
}

// Defends the catalog entry against reviewing one revision and citing another:
// reviewed_revision must be the commit the audit says it was made against.
func TestAXICatalogEntryReviewsTheAuditedRevision(t *testing.T) {
	audit := readRepoFile(t, auditPath)
	m := regexp.MustCompile("on `main` at `([0-9a-f]{7,40})`").FindStringSubmatch(audit)
	if m == nil {
		t.Fatalf("%s does not say which commit it audited (on `main` at `<sha>`)", auditPath)
	}
	entry := readRepoFile(t, catalogEntryPath)
	r := regexp.MustCompile(`reviewed_revision: ([0-9a-f]{40})`).FindStringSubmatch(entry)
	if r == nil {
		t.Fatalf("%s has no full reviewed_revision", catalogEntryPath)
	}
	if !strings.HasPrefix(r[1], m[1]) {
		t.Errorf("%s reviews %s but %s was made against %s", catalogEntryPath, r[1], auditPath, m[1])
	}
	if !strings.Contains(entry, "("+m[1]+",") {
		t.Errorf("%s's note does not name the audited commit %s", catalogEntryPath, m[1])
	}
}
