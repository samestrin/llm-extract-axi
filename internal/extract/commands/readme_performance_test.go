package commands

import (
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

var perfNumber = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)*`)

// TestReadmePerformanceMatchesGeneratedResults defends the README Performance
// section against a figure typed or edited by hand. The committed table in
// README.md must be the one benchmarks/results-performance.md generates, row
// for row, and every number in the section must appear in that generated file.
// A changed figure, date, run count or method therefore turns this red until
// `benchmarks/perf.sh` regenerates the results.
func TestReadmePerformanceMatchesGeneratedResults(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "benchmarks", "results-performance.md"))
	if err != nil {
		t.Fatalf("read benchmarks/results-performance.md: %v", err)
	}
	generated := string(raw)
	readme := readmeSection(t, "Performance")

	wantRows := perfTable(generated)
	gotRows := perfTable(readme)
	if len(wantRows) == 0 {
		t.Fatal("benchmarks/results-performance.md holds no performance table")
	}
	if len(gotRows) != len(wantRows) {
		t.Fatalf("README Performance table has %d rows, want the %d generated in benchmarks/results-performance.md", len(gotRows), len(wantRows))
	}
	for i := range wantRows {
		if gotRows[i] != wantRows[i] {
			t.Errorf("README Performance table row %d is %q, want the generated row %q", i+1, gotRows[i], wantRows[i])
		}
	}

	// The rows above are the stronger check; this one covers the header facts
	// (version, date, machine, method) that sit outside the table, so a date or
	// run count edited in the prose is caught too.
	known := map[string]bool{}
	for _, n := range perfNumber.FindAllString(generated, -1) {
		known[n] = true
	}
	for _, n := range perfNumber.FindAllString(readme, -1) {
		if !known[n] {
			t.Errorf("README Performance section names %q, which does not appear in benchmarks/results-performance.md; regenerate with benchmarks/perf.sh", n)
		}
	}
}
