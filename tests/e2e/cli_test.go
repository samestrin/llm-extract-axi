// Package e2e drives the built binaries from outside the module's packages.
//
// The tests need `make build` first. They FAIL, not skip, when a binary is
// missing: a skip is how an end-to-end suite stops running without anyone
// noticing. CI runs `make build` before `go test` for this reason.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// binary returns the path of a built binary under ./build, failing the test
// with the fix when it is absent.
func binary(t *testing.T, name string) string {
	t.Helper()
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

// Defends the Makefile's -X target: if the ldflags name the wrong variable,
// the build still succeeds and the binary reports "dev".
func TestCLIVersionIsStamped(t *testing.T) {
	out, err := exec.Command(binary(t, "llm-extract"), "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("--version failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if !strings.HasPrefix(got, "llm-extract version ") {
		t.Fatalf("unexpected --version output: %q", got)
	}
	if strings.HasSuffix(got, " dev") {
		t.Errorf("version was not stamped at build time: %q", got)
	}
}

// Defends the exit code across the process boundary: an unknown command must
// exit non-zero from the real binary, not only from run().
func TestCLIUnknownCommandExitsNonZero(t *testing.T) {
	cmd := exec.Command(binary(t, "llm-extract"), "no-such-command")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unknown command exited 0:\n%s", out)
	}
	if !strings.Contains(string(out), "no-such-command") {
		t.Errorf("error does not name the bad command:\n%s", out)
	}
}
