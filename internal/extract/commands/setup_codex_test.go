package commands

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexHooksPath(dir string) string { return filepath.Join(dir, ".codex", "hooks.json") }

// sessionStartCommands returns every SessionStart command in a hooks file.
func sessionStartCommands(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	hooks, _ := readJSON(t, path)["hooks"].(map[string]any)
	groups, _ := hooks["SessionStart"].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			c, _ := hm["command"].(string)
			out = append(out, c)
		}
	}
	return out
}

// Defends: the default run must register Codex too, with one SessionStart
// command hook running llm-extract, and name the file it wrote.
func TestSetupCodexWritesHookOnce(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	out, code := setupRun(t, home, "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	path := codexHooksPath(dir)
	if !strings.Contains(out, "wrote "+path) {
		t.Errorf("output does not name %s:\n%s", path, out)
	}
	if got := sessionStartCommands(t, path); len(got) != 1 || got[0] != "llm-extract" {
		t.Errorf("SessionStart commands = %q, want [llm-extract]", got)
	}
	if !strings.Contains(mustJSON(readJSON(t, path)), `"type":"command"`) {
		t.Errorf("hook is not a command hook: %v", readJSON(t, path))
	}
}

// Defends: a second run must leave the Codex file byte-for-byte alone and
// add no second entry.
func TestSetupCodexUnchangedOnRerun(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	setupRun(t, home, "--dir", dir)
	path := codexHooksPath(dir)
	b1, _ := os.ReadFile(path)
	st1, _ := os.Stat(path)

	out, code := setupRun(t, home, "--dir", dir)
	if code != 0 || !strings.Contains(out, "unchanged "+path) || strings.Contains(out, "wrote ") {
		t.Fatalf("second run exit %d: %s", code, out)
	}
	b2, _ := os.ReadFile(path)
	st2, _ := os.Stat(path)
	if !bytes.Equal(b1, b2) || !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("second run rewrote hooks.json")
	}
}

// Defends: when llm-extract is not on PATH the hook must name the binary by
// absolute path, and after the binary moves a rerun must point the existing
// entry at the new path instead of keeping a dead one or adding a second.
// The Claude hook is repaired the same way.
func TestSetupCodexRepairedAfterMove(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	oldExe := filepath.Join(t.TempDir(), "llm-extract")
	newExe := filepath.Join(t.TempDir(), "llm-extract")

	if out, code := setupRunAt(t, home, oldExe, "--dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if got := sessionStartCommands(t, codexHooksPath(dir)); len(got) != 1 || got[0] != oldExe {
		t.Fatalf("first run commands = %q, want [%s]", got, oldExe)
	}

	out, code := setupRunAt(t, home, newExe, "--dir", dir)
	if code != 0 || !strings.Contains(out, "wrote "+codexHooksPath(dir)) {
		t.Fatalf("after move exit %d: %s", code, out)
	}
	for _, p := range []string{codexHooksPath(dir), filepath.Join(dir, ".claude", "settings.json")} {
		if got := sessionStartCommands(t, p); len(got) != 1 || got[0] != newExe {
			t.Errorf("%s after move = %q, want [%s]", p, got, newExe)
		}
	}

	// And once it is on PATH, the bare name replaces the absolute path.
	setupRun(t, home, "--dir", dir)
	if got := sessionStartCommands(t, codexHooksPath(dir)); len(got) != 1 || got[0] != "llm-extract" {
		t.Errorf("after install on PATH = %q, want [llm-extract]", got)
	}
}

// Defends: an absolute path with a space must be quoted so the shell runs
// it as one word, and the quoted form must still be recognised as ours so a
// rerun changes nothing.
func TestSetupCodexQuotesPathWithSpace(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	exe := filepath.Join(t.TempDir(), "my tools", "llm-extract")
	setupRunAt(t, home, exe, "--dir", dir)
	want := "'" + exe + "'"
	if got := sessionStartCommands(t, codexHooksPath(dir)); len(got) != 1 || got[0] != want {
		t.Fatalf("commands = %q, want [%s]", got, want)
	}
	out, _ := setupRunAt(t, home, exe, "--dir", dir)
	if !strings.Contains(out, "already set up; no changes") {
		t.Errorf("rerun with quoted path changed something: %s", out)
	}
}

// Defends: with llm-extract neither on PATH nor locatable, setup must fail
// rather than write a hook that cannot run, and write nothing.
func TestSetupFailsWithoutResolvableBinary(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	if _, code := setupRunAt(t, home, "", "--dir", dir); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %v despite failure", entries)
	}
}

// Defends: Codex hooks the user already has must be kept.
func TestSetupCodexKeepsExistingHooks(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	path := codexHooksPath(dir)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	orig := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}],"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"other"}]}]}}`
	_ = os.WriteFile(path, []byte(orig), 0o644)
	if _, code := setupRun(t, home, "--dir", dir); code != 0 {
		t.Fatalf("exit %d", code)
	}
	hooks := readJSON(t, path)["hooks"].(map[string]any)
	if hooks["Stop"] == nil {
		t.Error("Stop hook lost")
	}
	if got := sessionStartCommands(t, path); len(got) != 2 || got[0] != "other" || got[1] != "llm-extract" {
		t.Errorf("SessionStart commands = %q", got)
	}
}

// Defends: a hooks.json that cannot be merged is an error, it is not
// overwritten, and no other target is written either.
func TestSetupRefusesMalformedCodexHooks(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	path := codexHooksPath(dir)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(`{"hooks":[]}`), 0o644)
	if _, code := setupRun(t, home, "--dir", dir); code == 0 {
		t.Fatal("exit 0 on unmergeable hooks.json")
	}
	if b, _ := os.ReadFile(path); string(b) != `{"hooks":[]}` {
		t.Errorf("hooks.json overwritten: %s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude")); err == nil {
		t.Error(".claude written despite failure")
	}
}

// Defends: --target claude must write only the Claude files.
func TestSetupTargetClaudeOnly(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	out, code := setupRun(t, home, "--dir", dir, "--target", "claude")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".codex")); err == nil {
		t.Error("--target claude wrote .codex")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); err != nil {
		t.Errorf("--target claude did not write settings.json: %v", err)
	}
	if strings.Contains(out, ".codex") {
		t.Errorf("output mentions codex: %s", out)
	}
}

// Defends: --target codex must write only the Codex file.
func TestSetupTargetCodexOnly(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	if _, code := setupRun(t, home, "--dir", dir, "--target", "codex"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude")); err == nil {
		t.Error("--target codex wrote .claude")
	}
	if _, err := os.Stat(codexHooksPath(dir)); err != nil {
		t.Errorf("--target codex did not write hooks.json: %v", err)
	}
}

// Defends: --target is repeatable and takes a comma list, and a repeated
// target is written once.
func TestSetupTargetRepeatable(t *testing.T) {
	for name, args := range map[string][]string{
		"repeated": {"--target", "codex", "--target", "claude", "--target", "codex"},
		"comma":    {"--target", "claude,codex"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			out, code := setupRun(t, home, append([]string{"--dir", dir}, args...)...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			if n := strings.Count(out, "wrote "+codexHooksPath(dir)); n != 1 {
				t.Errorf("codex written %d times: %s", n, out)
			}
			if !strings.Contains(out, "wrote "+filepath.Join(dir, ".claude", "settings.json")) {
				t.Errorf("claude not written: %s", out)
			}
		})
	}
}

// Defends: --target from one in-process run must not leak into the next;
// the root command is a singleton and a custom flag value is reset by hand.
func TestSetupTargetResetBetweenRuns(t *testing.T) {
	home := t.TempDir()
	setupRun(t, home, "--dir", t.TempDir(), "--target", "claude")
	dir := t.TempDir()
	setupRun(t, home, "--dir", dir)
	if _, err := os.Stat(codexHooksPath(dir)); err != nil {
		t.Errorf("default run after --target claude skipped codex: %v", err)
	}
	dir2 := t.TempDir()
	setupRun(t, home, "--dir", dir2, "--target", "codex")
	if _, err := os.Stat(filepath.Join(dir2, ".claude")); err == nil {
		t.Error("--target codex after an earlier --target claude also wrote .claude")
	}
}

// Defends: an unknown target is a usage error (exit 2) that names the valid
// targets, and nothing is written.
func TestSetupUnknownTargetIsUsageError(t *testing.T) {
	for _, v := range []string{"bogus", "claude,bogus", ""} {
		t.Run(v, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			stubHookBinary(t, true, "")
			t.Setenv("HOME", home)
			var out, errb bytes.Buffer
			code := run([]string{"setup", "--dir", dir, "--target", v}, &out, &errb)
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if msg := out.String() + errb.String(); !strings.Contains(msg, "claude") || !strings.Contains(msg, "codex") {
				t.Errorf("error does not list the valid targets: %s", msg)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("wrote %v on a usage error", entries)
			}
		})
	}
}

// Defends: --dry-run covers the Codex target too.
func TestSetupCodexDryRun(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	out, code := setupRun(t, home, "--dir", dir, "--target", "codex", "--dry-run")
	if code != 0 || !strings.Contains(out, "would write "+codexHooksPath(dir)) {
		t.Fatalf("exit %d: %s", code, out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("dry run created %v", entries)
	}
}

// Defends: every target writes only under --dir, never beside it or in HOME.
func TestSetupWritesNothingOutsideDir(t *testing.T) {
	parent, home := t.TempDir(), t.TempDir()
	dir := filepath.Join(parent, "project")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	setupRunAt(t, home, filepath.Join(t.TempDir(), "llm-extract"), "--dir", dir)
	setupRun(t, home, "--dir", dir)
	_ = filepath.WalkDir(parent, func(p string, d fs.DirEntry, err error) error {
		if p != parent && p != dir && !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			t.Errorf("wrote outside --dir: %s", p)
		}
		return err
	})
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("home was written: %v", entries)
	}
}
