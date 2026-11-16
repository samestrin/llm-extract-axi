package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// perfTable returns the contiguous markdown table lines in text, in order.
func perfTable(text string) []string {
	var rows []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "|") {
			rows = append(rows, line)
		}
	}
	return rows
}

var perfBullet = regexp.MustCompile(`(?m)^- \*\*([^*]+):\*\* (.*)$`)

// perfBullets maps each "- **Name:** value" line in text to its value.
func perfBullets(text string) map[string]string {
	m := map[string]string{}
	for _, b := range perfBullet.FindAllStringSubmatch(text, -1) {
		m[b[1]] = b[2]
	}
	return m
}

// perfMismatches lists every way readme's Performance section differs from
// the generated results: a table row, or a fact outside the table.
func perfMismatches(generated, readme string) []string {
	var bad []string
	wantRows := perfTable(generated)
	gotRows := perfTable(readme)
	if len(gotRows) != len(wantRows) {
		return []string{fmt.Sprintf("README Performance table has %d rows, want the %d generated in benchmarks/results-performance.md", len(gotRows), len(wantRows))}
	}
	for i := range wantRows {
		if gotRows[i] != wantRows[i] {
			bad = append(bad, fmt.Sprintf("README Performance table row %d is %q, want the generated row %q", i+1, gotRows[i], wantRows[i]))
		}
	}
	// The header facts outside the table are compared as whole bullets: a
	// number matched against the generated file's numbers passed "4 warmup"
	// on the 4 in a commit hash.
	want, got := perfBullets(generated), perfBullets(readme)
	if len(want) == 0 {
		bad = append(bad, "benchmarks/results-performance.md holds no - **Name:** bullets")
	}
	for name, w := range want {
		if g, ok := got[name]; !ok {
			bad = append(bad, fmt.Sprintf("README Performance section has no %s bullet", name))
		} else if g != w {
			bad = append(bad, fmt.Sprintf("README Performance %s is %q, want the generated %q", name, g, w))
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			bad = append(bad, fmt.Sprintf("README Performance %s bullet is not in benchmarks/results-performance.md", name))
		}
	}
	return bad
}

// TestReadmePerformanceMatchesGeneratedResults defends the README Performance
// section against a figure typed or edited by hand. The committed table in
// README.md must be the one benchmarks/results-performance.md generates, row
// for row, and so must the Version, Date, Machine and Method bullets above it.
// A changed figure, date, run count or method therefore turns this red until
// `benchmarks/perf.sh` regenerates the results.
func TestReadmePerformanceMatchesGeneratedResults(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "benchmarks", "results-performance.md"))
	if err != nil {
		t.Fatalf("read benchmarks/results-performance.md: %v", err)
	}
	generated := string(raw)
	if len(perfTable(generated)) == 0 {
		t.Fatal("benchmarks/results-performance.md holds no performance table")
	}
	for _, m := range perfMismatches(generated, readmeSection(t, "Performance")) {
		t.Error(m)
	}
}

// Defends the header-fact check against a substring match: it compared each
// number in the README with the set of numbers in the generated file, so
// "3 warmup" edited to "4 warmup" passed on the 4 in a commit hash. Each
// bullet edit below must be caught; the unedited section must pass.
func TestPerfMismatchesCatchesAnEditedBullet(t *testing.T) {
	generated := "- **Version:** llm-extract 1.0.1-23-g4d4972f\n" +
		"- **Date:** 2026-11-13\n" +
		"- **Machine:** Apple M5, 10 threads\n" +
		"- **Method:** 3 warmup and 20 timed runs per case\n\n" +
		"| Case | Runs |\n|---|---:|\n| Cold start | 20 |\n"
	if bad := perfMismatches(generated, generated); len(bad) != 0 {
		t.Fatalf("the unedited section is flagged: %v", bad)
	}
	for _, edit := range [][2]string{
		{"3 warmup", "4 warmup"},
		{"20 timed", "2 timed"},
		{"10 threads", "1 threads"},
		{"2026-11-13", "2026-11-1"},
		{"Apple M5", "Apple M4"},
	} {
		readme := strings.Replace(generated, edit[0], edit[1], 1)
		if bad := perfMismatches(generated, readme); len(bad) == 0 {
			t.Errorf("%q edited to %q was not caught", edit[0], edit[1])
		}
	}
}
