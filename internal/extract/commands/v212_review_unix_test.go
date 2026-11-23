//go:build unix

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Defends EST-8337 item 2: a --path naming a FIFO, or a link to /dev/zero,
// was read whole by os.ReadFile, which blocks forever on the FIFO and grows
// without bound on /dev/zero. It is refused before any model call.
func TestExtractRelevantSingleNonRegularPathRefused(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.txt")
	mkfifo(t, fifo)
	zero := filepath.Join(dir, "zero.txt")
	symlink(t, "/dev/zero", zero)

	for _, p := range []string{fifo, zero} {
		calls, _ := countingLLM(t, "NONE")
		code, out := runCLIWithin(t, 20*time.Second, "extract-relevant", "--path", p, "--context", "x", "--json")
		if code == 0 || !strings.Contains(out, "not a regular file") {
			t.Errorf("%s: exit %d, want a refusal naming a non-regular file:\n%s", p, code, out)
		}
		if n := calls.Load(); n != 0 {
			t.Errorf("%s: %d model calls, want none", p, n)
		}
	}
}

// Defends EST-8337 item 3: checkWritable opened an existing -o for writing to
// test it, and opening a FIFO for writing blocks until a reader appears, so
// the run hung before doing anything. A -o that is not a regular file or a
// new path is a usage error, and fetch --output shares the check.
func TestOutputFIFORefused(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	writeFiles(t, dir, map[string]string{"a.txt": "TEXT"})
	fifo := filepath.Join(dir, "out.fifo")
	mkfifo(t, fifo)

	calls, _ := countingLLM(t, "NONE")
	code, out := runCLIWithin(t, 20*time.Second, "extract-relevant", "--path", src, "--context", "x", "-o", fifo)
	if code != 2 || !strings.Contains(out, "not a regular file") {
		t.Errorf("extract-relevant -o FIFO: exit %d, want 2 naming a non-regular file:\n%s", code, out)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("extract-relevant -o FIFO made %d model calls", n)
	}

	code, out = runCLIWithin(t, 20*time.Second, "fetch", "http://127.0.0.1:1/", "--output", fifo)
	if code != 2 || !strings.Contains(out, "not a regular file") {
		t.Errorf("fetch --output FIFO: exit %d, want 2 naming a non-regular file:\n%s", code, out)
	}
}

// Defends EST-8337 item 2's cap: a named --path was read whole, so a huge
// regular file was loaded into memory although the model reads only the first
// maxRelevantPromptBytes. readCapped returns at most limit+1 bytes, enough for
// the prompt cut to be seen and reported.
func TestReadCappedStopsPastLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(p, []byte(strings.Repeat("a", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := readCapped(p, 10)
	if err != nil || len(b) != 11 {
		t.Errorf("readCapped(1000 bytes, 10) = %d bytes, %v; want 11", len(b), err)
	}
	b, err = readCapped(p, 5000)
	if err != nil || len(b) != 1000 {
		t.Errorf("readCapped(1000 bytes, 5000) = %d bytes, %v; want 1000", len(b), err)
	}
	if _, err := readCapped(filepath.Join(t.TempDir(), "missing"), 10); err == nil {
		t.Error("readCapped of a missing file succeeded")
	}
}

// Defends EST-8337 item 1 at the CLI end: the MCP server passes the url after
// "--", so a url that looks like a flag must be taken as the url, never as
// --output, and must write nothing.
func TestFetchDoubleDashURLIsNotAFlag(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "pwned")
	code, out, _ := runCLI(t, "fetch", "--", "--output="+dst)
	if code == 0 {
		t.Errorf("fetch -- --output=... exited 0:\n%s", out)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("a flag-looking url after -- was parsed as --output: %v", err)
	}
}
