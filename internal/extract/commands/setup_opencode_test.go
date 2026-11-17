package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The OpenCode target writes a plugin for the V2 plugin API, documented at
// https://opencode.ai/docs/build/plugins/server (local plugins load from
// .opencode/plugins/: https://opencode.ai/docs/build/plugins#load). The
// byte-for-byte contract with the committed plugin is defended by
// TestSetupOpenCodeWritesCommittedPlugin below.

func openCodePluginPath(dir string) string {
	return filepath.Join(dir, ".opencode", "plugins", "llm-extract.js")
}

// committedOpenCodePlugin is the expected plugin for a binary on PATH.
func committedOpenCodePlugin(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "integrations", "opencode", "llm-extract.js"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pluginFor is the committed plugin with its command literal pointed at cmd.
func pluginFor(t *testing.T, cmd string) []byte {
	t.Helper()
	want := committedOpenCodePlugin(t)
	from := []byte(`const command = "llm-extract"`)
	if bytes.Count(want, from) != 1 {
		t.Fatalf("committed plugin does not hold exactly one %s", from)
	}
	return bytes.Replace(want, from, []byte("const command = "+strconv.Quote(cmd)), 1)
}

// Defends: the default run must register OpenCode too, writing the plugin
// once and naming it, byte for byte the plugin committed under integrations/,
// so a reviewer sees exactly what lands in a user's project.
func TestSetupOpenCodeWritesCommittedPlugin(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	out, code := setupRun(t, home, "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	path := openCodePluginPath(dir)
	if n := strings.Count(out, "wrote "+path); n != 1 {
		t.Errorf("output names %s %d times:\n%s", path, n, out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := committedOpenCodePlugin(t); !bytes.Equal(got, want) {
		t.Errorf("plugin differs from integrations/opencode/llm-extract.js:\n%s", got)
	}
}

// Defends: a second run must leave the plugin byte-for-byte alone.
func TestSetupOpenCodeUnchangedOnRerun(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	setupRun(t, home, "--dir", dir)
	path := openCodePluginPath(dir)
	st1, _ := os.Stat(path)

	out, code := setupRun(t, home, "--dir", dir)
	if code != 0 || !strings.Contains(out, "unchanged "+path) || strings.Contains(out, "wrote ") {
		t.Fatalf("second run exit %d: %s", code, out)
	}
	st2, _ := os.Stat(path)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("second run rewrote the plugin")
	}
}

// Defends: off PATH the plugin must run the binary by absolute path, after a
// move a rerun must point it at the new path, and once the binary is on PATH
// the bare name comes back. The path goes into a JS string, not a shell, so
// a space or quote in it must not be shell-quoted.
func TestSetupOpenCodeRepairedAfterMove(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	path := openCodePluginPath(dir)
	oldExe := filepath.Join(t.TempDir(), "old tools", "llm-extract")
	newExe := filepath.Join(t.TempDir(), `it's "new"`, "llm-extract")

	if out, code := setupRunAt(t, home, oldExe, "--dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, pluginFor(t, oldExe)) {
		t.Fatalf("first run plugin does not run %s:\n%s", oldExe, got)
	}

	out, code := setupRunAt(t, home, newExe, "--dir", dir)
	if code != 0 || !strings.Contains(out, "wrote "+path) {
		t.Fatalf("after move exit %d: %s", code, out)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, pluginFor(t, newExe)) {
		t.Errorf("plugin after move does not run %s:\n%s", newExe, got)
	}
	if out, _ := setupRunAt(t, home, newExe, "--dir", dir); !strings.Contains(out, "unchanged "+path) {
		t.Errorf("rerun at the new path changed the plugin: %s", out)
	}

	setupRun(t, home, "--dir", dir)
	if got, _ := os.ReadFile(path); !bytes.Equal(got, committedOpenCodePlugin(t)) {
		t.Errorf("plugin after install on PATH does not run llm-extract by name:\n%s", got)
	}
}

// Defends: --dry-run covers the OpenCode target and writes nothing.
func TestSetupOpenCodeDryRun(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	out, code := setupRun(t, home, "--dir", dir, "--target", "opencode", "--dry-run")
	if code != 0 || !strings.Contains(out, "would write "+openCodePluginPath(dir)) {
		t.Fatalf("exit %d: %s", code, out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("dry run created %v", entries)
	}
}

// Defends: --target opencode writes only the plugin, and the other targets
// do not write it.
func TestSetupTargetOpenCodeOnly(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	if out, code := setupRun(t, home, "--dir", dir, "--target", "opencode"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 || entries[0].Name() != ".opencode" {
		t.Errorf("--target opencode wrote %v", entries)
	}
	if _, err := os.Stat(openCodePluginPath(dir)); err != nil {
		t.Errorf("plugin not written: %v", err)
	}

	dir2 := t.TempDir()
	setupRun(t, home, "--dir", dir2, "--target", "claude,codex")
	if _, err := os.Stat(filepath.Join(dir2, ".opencode")); err == nil {
		t.Error("--target claude,codex wrote .opencode")
	}
}

// Defends: a hand-edited plugin is ours to manage, so a rerun restores it
// rather than reporting it set up.
func TestSetupOpenCodeRestoresEditedPlugin(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	setupRun(t, home, "--dir", dir)
	path := openCodePluginPath(dir)
	_ = os.WriteFile(path, []byte("edited"), 0o644)
	if out, _ := setupRun(t, home, "--dir", dir); !strings.Contains(out, "wrote "+path) {
		t.Errorf("edited plugin not restored: %s", out)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, committedOpenCodePlugin(t)) {
		t.Error("restored plugin differs from the committed one")
	}
}

// Defends: an unreadable plugin path is an error, and no other target is
// written either.
func TestSetupOpenCodeReportsUnreadablePlugin(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(openCodePluginPath(dir), 0o755)
	if _, code := setupRun(t, home, "--dir", dir); code == 0 {
		t.Error("exit 0 when the plugin path is a directory")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude")); err == nil {
		t.Error(".claude written despite failure")
	}
}
