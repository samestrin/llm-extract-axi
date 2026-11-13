package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fixture = "../fixtures/docs-page.html"

// builtBinary returns ../../build/llm-extract, failing (not skipping) when it is
// absent: a skipped benchmark test is one nobody notices has stopped running.
func builtBinary(t *testing.T) string {
	t.Helper()
	name := "llm-extract"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "build", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s is not built (%v); run `make build` before `go test`", path, err)
	}
	return path
}

// TestMain lets the test binary stand in for llm-extract: --version prints a
// stamp, and extract-links behaves as BENCH_FAKE_PERF says, so the failure
// paths are tested without a shell script per platform.
func TestMain(m *testing.M) {
	if mode := os.Getenv("BENCH_FAKE_PERF"); mode != "" {
		if len(os.Args) > 1 && os.Args[1] == "--version" {
			fmt.Println("9.9.9-fake")
			os.Exit(0)
		}
		switch mode {
		case "nolinks":
			fmt.Println("count: 0")
			os.Exit(0)
		default:
			fmt.Fprintln(os.Stderr, "fake extract-links failure")
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func paths(t *testing.T) (jsonPath, mdPath string) {
	dir := t.TempDir()
	return filepath.Join(dir, "r.json"), filepath.Join(dir, "r.md")
}

// Defends the deliverable: a run against the real binary times all three cases
// (the fixture and the 5 MB page both yield links), records every timing in the
// JSON, and renders a table stamped with the binary's own version. Checks the
// payload, not just that it ran.
func TestRunTimesTheRealBinary(t *testing.T) {
	bin := builtBinary(t)
	jsonPath, mdPath := paths(t)
	var out strings.Builder
	o := options{bin: bin, fixture: fixture, jsonPath: jsonPath, mdPath: mdPath, warmup: 0, runs: 2}
	if err := run(o, &out); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(command(bin, "--version"))
	if version == "" || rep.Version != version {
		t.Errorf("version = %q, want the binary's own %q", rep.Version, version)
	}
	if len(rep.Cases) != 3 {
		t.Fatalf("got %d cases, want 3", len(rep.Cases))
	}
	for _, c := range rep.Cases {
		if len(c.TimingsMS) != 2 {
			t.Errorf("case %q has %d timings, want 2", c.Name, len(c.TimingsMS))
		}
		if strings.Contains(c.Command, "127.0.0.1:") {
			t.Errorf("case %q records the loopback port, which changes every run: %s", c.Name, c.Command)
		}
	}
	if got := rep.Cases[2].InputBytes; got < largeMinBytes {
		t.Errorf("large input is %d bytes, want at least %d", got, largeMinBytes)
	}

	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(md) != out.String() {
		t.Error("the table printed differs from the table written")
	}
	for _, want := range []string{"llm-extract " + version, "**Date:** " + rep.Date, "**Machine:**", "**Method:** 0 warmup and 2 timed runs", "| Large input |"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("table has no %q:\n%s", want, md)
		}
	}
}

// Defends "a failed run is never a result": a case whose process fails, or that
// exits 0 having found no links, records zero timings, fails the run and
// writes neither file.
func TestFailingCaseWritesNothing(t *testing.T) {
	for _, mode := range []string{"fail", "nolinks"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("BENCH_FAKE_PERF", mode)
			jsonPath, mdPath := paths(t)
			o := options{bin: os.Args[0], fixture: fixture, jsonPath: jsonPath, mdPath: mdPath, warmup: 1, runs: 2}
			err := run(o, &strings.Builder{})
			if err == nil || !strings.Contains(err.Error(), `"Typical command"`) || !strings.Contains(err.Error(), "zero timings") {
				t.Fatalf("err = %v, want the typical command to fail with zero timings", err)
			}
			for _, p := range []string{jsonPath, mdPath} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Errorf("%s was written by a failed run", filepath.Base(p))
				}
			}
		})
	}
}

// Defends the options that would time nothing, or the wrong thing: no runs,
// negative warmups, a "large" page under 5 MB, a binary that does not run, and
// a fixture with no body to repeat.
func TestMeasureRejectsBadOptions(t *testing.T) {
	small := filepath.Join(t.TempDir(), "small.html")
	if err := os.WriteFile(small, []byte("<html><body><a href=/a>a</a></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	nobody := filepath.Join(t.TempDir(), "nobody.html")
	if err := os.WriteFile(nobody, []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok := options{bin: os.Args[0], fixture: fixture, runs: 1}
	cases := []struct {
		name string
		edit func(*options)
		want string
	}{
		{"zero runs", func(o *options) { o.runs = 0 }, "zero timings"},
		{"negative warmup", func(o *options) { o.warmup = -1 }, "negative"},
		{"small large page", func(o *options) { o.large = small }, "under 5000000"},
		{"missing large page", func(o *options) { o.large = small + ".missing" }, "no such file"},
		{"missing fixture", func(o *options) { o.fixture = small + ".missing" }, "no such file"},
		{"fixture without body", func(o *options) { o.fixture = nobody }, "no <body>"},
		{"binary missing", func(o *options) { o.bin = small + ".missing" }, "--version"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := ok
			c.edit(&o)
			if _, err := measure(o); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func goodReport() Report {
	return Report{
		Version: "9.9.9",
		Date:    "2026-11-13",
		Machine: Machine{CPU: "Test CPU", Threads: 8, RAMBytes: 16 << 30, OS: "Test OS", Kernel: "6.0", Arch: "amd64"},
		Method:  Method{Warmup: 1, Runs: 3, Clock: clock},
		Cases: []Case{
			{Name: "Cold start", Command: "llm-extract --version", TimingsMS: []float64{3, 1, 2}},
			{Name: "Large input", Command: "llm-extract extract-links", InputBytes: 5 << 20, TimingsMS: []float64{10, 40, 20}},
		},
	}
}

// Defends the zero-timing rule at render time: a JSON with a case that holds
// no timings, fewer timings than its method says, or a non-positive timing is
// refused, so no table row can stand for something that was not measured.
func TestValidateRejectsUnmeasuredRows(t *testing.T) {
	if err := validate(goodReport()); err != nil {
		t.Fatalf("a good report was rejected: %v", err)
	}
	cases := []struct {
		name string
		edit func(*Report)
		want string
	}{
		{"zero timings", func(r *Report) { r.Cases[1].TimingsMS = nil }, `"Large input" recorded zero timings`},
		{"short", func(r *Report) { r.Cases[0].TimingsMS = []float64{1} }, "recorded 1 timings"},
		{"non-positive", func(r *Report) { r.Cases[0].TimingsMS[1] = 0 }, "timing of 0"},
		{"no version", func(r *Report) { r.Version = " " }, "no version"},
		{"no date", func(r *Report) { r.Date = "" }, "no date"},
		{"no cases", func(r *Report) { r.Cases = nil }, "no cases"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := goodReport()
			c.edit(&r)
			if err := validate(r); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
		})
	}
}

// Defends "no figure is typed by hand": every figure in the table is computed
// from the raw timings, and the stamp lines name version, date, machine and
// method.
func TestRenderComputesEveryFigure(t *testing.T) {
	md := render(goodReport())
	for _, want := range []string{
		"- **Version:** llm-extract 9.9.9\n",
		"- **Date:** 2026-11-13\n",
		"- **Machine:** Test CPU, 8 threads, 16.0 GiB RAM, Test OS (amd64), kernel 6.0\n",
		"- **Method:** 1 warmup and 3 timed runs per case",
		"wall-clock per process",
		"| Cold start | `llm-extract --version` | - | 3 | 2.0 ms | 2.0 ms | 1.0 ms | 3.0 ms |",
		"| Large input | `llm-extract extract-links` | 5.0 MiB | 3 | 20.0 ms | 23.3 ms | 10.0 ms | 40.0 ms |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("table has no %q:\n%s", want, md)
		}
	}

	r := goodReport()
	r.Machine.RAMBytes = 0
	if md := render(r); !strings.Contains(md, "unknown RAM") {
		t.Errorf("unread RAM is not shown as unknown:\n%s", md)
	}
	if got := median([]float64{1, 2, 3, 10}); got != 2.5 {
		t.Errorf("median of an even count = %v, want 2.5", got)
	}
	if got := size(15411); got != "15.0 KiB" {
		t.Errorf("size(15411) = %q", got)
	}
}

// Defends -render: it regenerates the table from the JSON alone, overwriting a
// hand edit, and refuses a JSON it cannot read.
func TestRenderOnlyRewritesFromJSON(t *testing.T) {
	jsonPath, mdPath := paths(t)
	data, _ := json.Marshal(goodReport())
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte("| Hand-typed | 0.1 ms |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(options{render: true, jsonPath: jsonPath, mdPath: mdPath}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	md, _ := os.ReadFile(mdPath)
	if string(md) != render(goodReport()) {
		t.Errorf("-render did not rewrite the table from the JSON:\n%s", md)
	}

	if err := os.WriteFile(jsonPath, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(options{render: true, jsonPath: jsonPath, mdPath: mdPath}, &strings.Builder{}); err == nil {
		t.Error("a JSON that does not parse was rendered")
	}
	if err := run(options{render: true, jsonPath: jsonPath + ".missing", mdPath: mdPath}, &strings.Builder{}); err == nil {
		t.Error("a missing JSON was rendered")
	}
}

// Defends the large page: it reaches the size asked for, keeps the fixture's
// head and closing tags, so it is still one well-formed page.
func TestEnlargeRepeatsTheBody(t *testing.T) {
	page := []byte("<html><head><title>t</title></head><body class=x><a href=/a>a</a></body></html>")
	got, err := enlarge(page, 1000)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if len(got) < 1000 || !strings.HasPrefix(s, "<html><head><title>t</title></head><body class=x><a") ||
		!strings.HasSuffix(s, "</a></body></html>") || strings.Count(s, "<body") != 1 {
		t.Errorf("enlarge gave %d bytes: %.80s...%s", len(got), s, s[len(s)-30:])
	}
	if _, err := enlarge([]byte("<html><body>  </body></html>"), 1000); err == nil {
		t.Error("an empty body was repeated forever, or accepted")
	}
}

// Defends the machine stamp: values are read from the files the host keeps,
// and an unreadable one stays empty rather than guessed.
func TestMachineReaders(t *testing.T) {
	dir := t.TempDir()
	cpuinfo := filepath.Join(dir, "cpuinfo")
	_ = os.WriteFile(cpuinfo, []byte("processor\t: 0\nmodel name\t: Test CPU @ 3.0GHz\n"), 0o644)
	if got := procField(cpuinfo, "model name"); got != "Test CPU @ 3.0GHz" {
		t.Errorf("procField = %q", got)
	}
	if got := procField(cpuinfo, "MemTotal"); got != "" {
		t.Errorf("absent field = %q", got)
	}
	if got := procField(cpuinfo+".missing", "model name"); got != "" {
		t.Errorf("missing file = %q", got)
	}
	release := filepath.Join(dir, "os-release")
	_ = os.WriteFile(release, []byte("NAME=Test\nPRETTY_NAME=\"Test Linux 1\"\n"), 0o644)
	if got := osRelease(release); got != "Test Linux 1" {
		t.Errorf("osRelease = %q", got)
	}
	if got := osRelease(release + ".missing"); got != "" {
		t.Errorf("missing os-release = %q", got)
	}
	if got := command(filepath.Join(dir, "no-such-program")); got != "" {
		t.Errorf("a missing program gave %q", got)
	}
	m := machine()
	if m.Threads < 1 || m.Arch != runtime.GOARCH || m.OS == "" {
		t.Errorf("machine() = %+v", m)
	}
}
