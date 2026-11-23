//go:build unix

package commands

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Defends EST-8274 item 1: a FIFO with a text name inside --path was opened
// for reading, which blocks until something writes to it, so the run never
// finished. A file that is not regular is skipped and counted.
func TestExtractRelevantDirectorySkipsFIFO(t *testing.T) {
	_, prompts := countingLLM(t, "NONE")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.txt": "TEXT"})
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.txt"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink(t, "pipe.txt", filepath.Join(dir, "pipe-link.md"))

	code, out := runCLIWithin(t, 20*time.Second, "extract-relevant", "--path", dir, "--context", "x", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(prompts(), "TEXT") || !strings.Contains(out, `"not_regular": 2`) {
		t.Errorf("FIFO not skipped and counted:\n%s", out)
	}
}
