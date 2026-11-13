// Command perf times llm-extract in three offline cases and writes every raw
// timing to a JSON file, then renders a Markdown table from that JSON:
//
//   - Cold start: `llm-extract --version`.
//   - Typical command: `extract-links --axi` on the committed fixture page.
//   - Large input: `extract-links --axi` on an HTML page of at least 5 MB,
//     built from the fixture unless one is given with -large.
//
// It makes no network call. Both pages are served from an in-process server on
// the loopback interface, and the child's proxies point at a closed port, as in
// benchmarks/tokens. A remote host is never timed: that would measure the host.
//
// A case that records zero timings fails the run, and so does any process that
// fails or finds no links. Nothing is written until every case has passed, so a
// failed run leaves the committed results as they were.
//
// Usage, from the repository root (benchmarks/perf.sh builds the release
// binary and passes -bin):
//
//	go run ./benchmarks/perf -bin path/to/llm-extract [-warmup 3] [-runs 20] [-large page.html]
//	go run ./benchmarks/perf -render
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// largeMinBytes is the smallest page the large-input case accepts.
const largeMinBytes = 5_000_000

// deadProxy is where the child sends any request Go would proxy. Go never
// proxies a loopback request, so the pages still load, while a request to any
// other host would go to a closed port and fail rather than leave the machine.
const deadProxy = "http://127.0.0.1:9"

// clock names what one timing is.
const clock = "wall-clock per process"

// placeholderOrigin replaces the loopback origin in the recorded commands; its
// port changes from run to run.
const placeholderOrigin = "http://127.0.0.1"

// linksHeader is the TOON header of a non-empty links array.
var linksHeader = regexp.MustCompile(`(?m)^links\[[1-9][0-9]*\]`)

// Report is the content of results-performance.json.
type Report struct {
	Version string  `json:"version"`
	Date    string  `json:"date"`
	Machine Machine `json:"machine"`
	Method  Method  `json:"method"`
	Cases   []Case  `json:"cases"`
}

// Machine describes where the timings were taken.
type Machine struct {
	CPU      string `json:"cpu"`
	Threads  int    `json:"threads"`
	RAMBytes uint64 `json:"ram_bytes"`
	OS       string `json:"os"`
	Kernel   string `json:"kernel"`
	Arch     string `json:"arch"`
}

// Method says how the timings were taken.
type Method struct {
	Warmup int    `json:"warmup"`
	Runs   int    `json:"runs"`
	Clock  string `json:"clock"`
}

// Case is one timed command and every timing it recorded, in milliseconds.
type Case struct {
	Name       string    `json:"name"`
	Command    string    `json:"command"`
	InputBytes int       `json:"input_bytes,omitempty"`
	TimingsMS  []float64 `json:"timings_ms"`
}

// options are the command-line flags.
type options struct {
	bin, fixture, large, jsonPath, mdPath string
	warmup, runs                          int
	render                                bool
}

func main() {
	var o options
	flag.StringVar(&o.bin, "bin", filepath.Join("build", "llm-extract"), "llm-extract binary to time")
	flag.StringVar(&o.fixture, "fixture", filepath.Join("benchmarks", "fixtures", "docs-page.html"), "HTML page for the typical command, and the source of the large page")
	flag.StringVar(&o.large, "large", "", "HTML page of at least 5 MB for the large-input case (default: built from -fixture)")
	flag.StringVar(&o.jsonPath, "json", filepath.Join("benchmarks", "results-performance.json"), "raw timings, written by a run and read by -render")
	flag.StringVar(&o.mdPath, "md", filepath.Join("benchmarks", "results-performance.md"), "the table generated from -json")
	flag.IntVar(&o.warmup, "warmup", 3, "untimed runs per case before the timed ones")
	flag.IntVar(&o.runs, "runs", 20, "timed runs per case")
	flag.BoolVar(&o.render, "render", false, "only regenerate -md from -json; time nothing")
	flag.Parse()
	if err := run(o, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "performance benchmark failed:", err)
		os.Exit(1)
	}
}

// run times the cases and writes -json, or only renders it with -render, then
// writes -md from -json. Every failure is returned before a file is written.
func run(o options, w io.Writer) error {
	if !o.render {
		rep, err := measure(o)
		if err != nil {
			return err
		}
		if err := validate(rep); err != nil {
			return err
		}
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFile(o.jsonPath, append(data, '\n')); err != nil {
			return err
		}
	}
	md, err := renderFile(o.jsonPath)
	if err != nil {
		return err
	}
	if err := writeFile(o.mdPath, []byte(md)); err != nil {
		return err
	}
	_, _ = io.WriteString(w, md)
	return nil
}

// renderFile reads a report from path and renders its table.
func renderFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if err := validate(rep); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return render(rep), nil
}

// validate rejects a report that would render a row nobody measured.
func validate(rep Report) error {
	if strings.TrimSpace(rep.Version) == "" {
		return errors.New("no version recorded")
	}
	if rep.Date == "" {
		return errors.New("no date recorded")
	}
	if len(rep.Cases) == 0 {
		return errors.New("no cases recorded")
	}
	for _, c := range rep.Cases {
		if len(c.TimingsMS) == 0 {
			return fmt.Errorf("case %q recorded zero timings", c.Name)
		}
		if len(c.TimingsMS) != rep.Method.Runs {
			return fmt.Errorf("case %q recorded %d timings, the method says %d runs", c.Name, len(c.TimingsMS), rep.Method.Runs)
		}
		for _, t := range c.TimingsMS {
			if t <= 0 {
				return fmt.Errorf("case %q recorded a timing of %v ms", c.Name, t)
			}
		}
	}
	return nil
}

// measure serves the pages on loopback and times every case.
func measure(o options) (Report, error) {
	if o.runs <= 0 {
		return Report{}, fmt.Errorf("-runs %d would record zero timings", o.runs)
	}
	if o.warmup < 0 {
		return Report{}, fmt.Errorf("-warmup %d is negative", o.warmup)
	}
	small, err := os.ReadFile(o.fixture)
	if err != nil {
		return Report{}, err
	}
	var large []byte
	if o.large != "" {
		if large, err = os.ReadFile(o.large); err != nil {
			return Report{}, err
		}
	} else if large, err = enlarge(small, largeMinBytes); err != nil {
		return Report{}, err
	}
	if len(large) < largeMinBytes {
		return Report{}, fmt.Errorf("the large page is %d bytes, under %d", len(large), largeMinBytes)
	}

	mux := http.NewServeMux()
	serve := func(path string, page []byte) {
		mux.HandleFunc(path, func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = rw.Write(page)
		})
	}
	serve("/docs/getting-started/", small)
	serve("/docs/large/", large)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := runOnce(o.bin, []string{"--version"})
	if err != nil {
		return Report{}, fmt.Errorf("--version: %w", err)
	}
	version := strings.TrimSpace(out)
	if version == "" {
		return Report{}, errors.New("--version printed nothing")
	}

	hasOutput := func(out string) error {
		if strings.TrimSpace(out) == "" {
			return errors.New("printed nothing")
		}
		return nil
	}
	hasLinks := func(out string) error {
		if !linksHeader.MatchString(out) {
			return errors.New("found no links on the page:\n" + out)
		}
		return nil
	}
	links := func(path string) []string {
		return []string{"extract-links", "--url", srv.URL + path, "--axi"}
	}
	specs := []struct {
		name  string
		args  []string
		input int
		check func(string) error
	}{
		{"Cold start", []string{"--version"}, 0, hasOutput},
		{"Typical command", links("/docs/getting-started/"), len(small), hasLinks},
		{"Large input", links("/docs/large/"), len(large), hasLinks},
	}

	rep := Report{
		Version: version,
		Date:    time.Now().UTC().Format("2006-01-02"),
		Machine: machine(),
		Method:  Method{Warmup: o.warmup, Runs: o.runs, Clock: clock},
	}
	for _, s := range specs {
		c := Case{
			Name:       s.name,
			Command:    "llm-extract " + strings.ReplaceAll(strings.Join(s.args, " "), srv.URL, placeholderOrigin),
			InputBytes: s.input,
			TimingsMS:  []float64{},
		}
		for i := 0; i < o.warmup+o.runs; i++ {
			start := time.Now()
			out, err := runOnce(o.bin, s.args)
			elapsed := time.Since(start)
			if err == nil {
				err = s.check(out)
			}
			if err != nil {
				return Report{}, fmt.Errorf("case %q, run %d of %d: %w (it records zero timings)", s.name, i+1, o.warmup+o.runs, err)
			}
			if i >= o.warmup {
				c.TimingsMS = append(c.TimingsMS, float64(elapsed.Nanoseconds())/1e6)
			}
		}
		rep.Cases = append(rep.Cases, c)
	}
	return rep, nil
}

// runOnce runs bin once with the offline environment and returns its stdout.
func runOnce(bin string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = childEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()+stdout.String()))
	}
	return stdout.String(), nil
}

// childEnv is the environment minus model credentials, proxy settings and the
// fetch tuning variables, with every proxy pointed at deadProxy. extract-links
// without --context calls no model; dropping the keys makes sure of it.
func childEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name := strings.ToUpper(kv[:strings.IndexByte(kv+"=", '=')])
		if strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "ANTHROPIC_") ||
			strings.HasPrefix(name, "FETCH_") || strings.HasSuffix(name, "_PROXY") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HTTP_PROXY="+deadProxy, "HTTPS_PROXY="+deadProxy)
}

// enlarge repeats the content of page's <body> until the page is at least
// minBytes. The links repeat too, and extract-links drops duplicates, so the
// case times reading and parsing a large page, not printing a long list.
func enlarge(page []byte, minBytes int) ([]byte, error) {
	open := bytes.Index(page, []byte("<body"))
	end := bytes.LastIndex(page, []byte("</body>"))
	if open < 0 || end < 0 {
		return nil, errors.New("the fixture has no <body> to repeat")
	}
	start := open + bytes.IndexByte(page[open:], '>') + 1
	body := page[start:end]
	if start > end || len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("the fixture's <body> is empty")
	}
	var b bytes.Buffer
	b.Write(page[:start])
	for b.Len()+len(page)-end < minBytes {
		b.Write(body)
	}
	b.Write(page[end:])
	return b.Bytes(), nil
}

// machine reads what the host says about itself. A value it cannot read is
// recorded as "unknown", never guessed.
func machine() Machine {
	m := Machine{CPU: "unknown", Threads: runtime.NumCPU(), OS: runtime.GOOS, Kernel: "unknown", Arch: runtime.GOARCH}
	switch runtime.GOOS {
	case "linux":
		if v := procField("/proc/cpuinfo", "model name"); v != "" {
			m.CPU = v
		}
		// MemTotal reads "16318028 kB".
		kbField, _, _ := strings.Cut(procField("/proc/meminfo", "MemTotal"), " ")
		if kb, err := strconv.ParseUint(kbField, 10, 64); err == nil {
			m.RAMBytes = kb * 1024
		}
		if v := osRelease("/etc/os-release"); v != "" {
			m.OS = v
		}
		if v, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			m.Kernel = strings.TrimSpace(string(v))
		}
	case "darwin":
		if v := command("sysctl", "-n", "machdep.cpu.brand_string"); v != "" {
			m.CPU = v
		}
		if n, err := strconv.ParseUint(command("sysctl", "-n", "hw.memsize"), 10, 64); err == nil {
			m.RAMBytes = n
		}
		if v := command("sw_vers", "-productVersion"); v != "" {
			m.OS = "macOS " + v
		}
		if v := command("uname", "-r"); v != "" {
			m.Kernel = "Darwin " + v
		}
	}
	return m
}

// procField returns the value of the first "key : value" line in path.
func procField(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// osRelease returns PRETTY_NAME from an os-release file.
func osRelease(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// command runs a local program and returns its trimmed output, or "".
func command(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// render is the Markdown table for rep. Every figure in it is computed here
// from rep's raw timings.
func render(rep Report) string {
	var b strings.Builder
	m := rep.Machine
	ram := "unknown"
	if m.RAMBytes > 0 {
		ram = fmt.Sprintf("%.1f GiB", float64(m.RAMBytes)/(1<<30))
	}
	fmt.Fprintf(&b, "# Performance\n\n")
	fmt.Fprintf(&b, "Generated by `benchmarks/perf.sh` from `results-performance.json`. Do not edit it by hand: `./benchmarks/perf.sh -render` regenerates it from the JSON, and `./benchmarks/perf.sh` takes a new measurement.\n\n")
	fmt.Fprintf(&b, "- **Version:** llm-extract %s\n", rep.Version)
	fmt.Fprintf(&b, "- **Date:** %s\n", rep.Date)
	fmt.Fprintf(&b, "- **Machine:** %s, %d threads, %s RAM, %s (%s), kernel %s\n", m.CPU, m.Threads, ram, m.OS, m.Arch, m.Kernel)
	fmt.Fprintf(&b, "- **Method:** %d warmup and %d timed runs per case, one after another; each timing is the %s, from start to exit, with the pages served on 127.0.0.1\n\n",
		rep.Method.Warmup, rep.Method.Runs, rep.Method.Clock)
	fmt.Fprintf(&b, "| Case | Command | Input | Runs | Median | Mean | Min | Max |\n")
	fmt.Fprintf(&b, "|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, c := range rep.Cases {
		t := append([]float64(nil), c.TimingsMS...)
		sort.Float64s(t)
		sum := 0.0
		for _, v := range t {
			sum += v
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s | %d | %s | %s | %s | %s |\n",
			c.Name, c.Command, size(c.InputBytes), len(t),
			ms(median(t)), ms(sum/float64(len(t))), ms(t[0]), ms(t[len(t)-1]))
	}
	return b.String()
}

// median of sorted, non-empty t.
func median(t []float64) float64 {
	if n := len(t); n%2 == 1 {
		return t[n/2]
	}
	return (t[len(t)/2-1] + t[len(t)/2]) / 2
}

func ms(v float64) string { return fmt.Sprintf("%.1f ms", v) }

// size is a byte count for the Input column; a case with no input shows "-".
func size(n int) string {
	switch {
	case n == 0:
		return "-"
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
}

// writeFile replaces path in one rename, so a reader never sees half a file.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".perf-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
