package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupRun runs `setup --dir dir` with HOME pointed at a temp dir and returns
// stdout, the exit code and the home dir.
func setupRun(t *testing.T, home string, args ...string) (string, int) {
	t.Helper()
	t.Setenv("HOME", home)
	var out, errb bytes.Buffer
	code := run(append([]string{"setup"}, args...), &out, &errb)
	return out.String(), code
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Defends: setup must write the hook and the skill, and print the paths.
func TestSetupWritesHookAndSkill(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	out, code := setupRun(t, home, "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	settings := filepath.Join(dir, ".claude", "settings.json")
	skill := filepath.Join(dir, ".claude", "skills", "llm-extract", "SKILL.md")
	for _, p := range []string{settings, skill} {
		if !strings.Contains(out, "wrote "+p) {
			t.Errorf("output does not name %s:\n%s", p, out)
		}
	}
	b, _ := os.ReadFile(settings)
	if !strings.Contains(string(b), `"SessionStart"`) || !strings.Contains(string(b), `"command": "llm-extract"`) {
		t.Errorf("settings missing hook: %s", b)
	}
	s, _ := os.ReadFile(skill)
	if !strings.HasPrefix(string(s), "---\nname: llm-extract\n") {
		t.Errorf("skill missing frontmatter: %s", s)
	}
}

// Defends: a second run must change no bytes and say so.
func TestSetupIsIdempotent(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	setupRun(t, home, "--dir", dir)
	settings := filepath.Join(dir, ".claude", "settings.json")
	skill := filepath.Join(dir, ".claude", "skills", "llm-extract", "SKILL.md")
	s1, _ := os.ReadFile(settings)
	k1, _ := os.ReadFile(skill)
	st1, _ := os.Stat(settings)

	out, code := setupRun(t, home, "--dir", dir)
	if code != 0 || !strings.Contains(out, "already set up; no changes") || strings.Contains(out, "wrote ") {
		t.Fatalf("second run exit %d: %s", code, out)
	}
	s2, _ := os.ReadFile(settings)
	k2, _ := os.ReadFile(skill)
	st2, _ := os.Stat(settings)
	if !bytes.Equal(s1, s2) || !bytes.Equal(k1, k2) || !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("second run rewrote files")
	}
}

// Defends: merging must not drop the user's other keys or other hooks.
func TestSetupKeepsExistingSettings(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := `{"model":"opus","permissions":{"allow":["Bash(ls)"]},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"x"}]}],"SessionStart":[{"hooks":[{"type":"command","command":"other"}]}]}}`
	if err := os.WriteFile(settings, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code := setupRun(t, home, "--dir", dir); code != 0 {
		t.Fatalf("exit %d", code)
	}
	m := readJSON(t, settings)
	if m["model"] != "opus" || m["permissions"] == nil {
		t.Errorf("top-level keys lost: %v", m)
	}
	hooks := m["hooks"].(map[string]any)
	if hooks["PreToolUse"] == nil {
		t.Error("PreToolUse hook lost")
	}
	groups := hooks["SessionStart"].([]any)
	if len(groups) != 2 || !strings.Contains(mustJSON(groups[0]), `"other"`) || !strings.Contains(mustJSON(groups[1]), `"llm-extract"`) {
		t.Errorf("SessionStart = %v", groups)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Defends: --dry-run must report but write nothing.
func TestSetupDryRunWritesNothing(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	out, code := setupRun(t, home, "--dir", dir, "--dry-run")
	if code != 0 || !strings.Contains(out, "would write") || strings.Contains(out, "wrote ") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("dry run created %v", entries)
	}
}

// Defends: setup must never touch the home directory.
func TestSetupLeavesHomeAlone(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	setupRun(t, home, "--dir", dir)
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("home was written: %v", entries)
	}
}

// Defends: a settings.json that cannot be merged is an error and is not
// overwritten, and no skill is written either.
func TestSetupRefusesMalformedSettings(t *testing.T) {
	for name, content := range map[string]string{
		"invalid":      `{not json`,
		"not object":   `[1]`,
		"hooks scalar": `{"hooks":3}`,
		"start scalar": `{"hooks":{"SessionStart":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir, home := t.TempDir(), t.TempDir()
			settings := filepath.Join(dir, ".claude", "settings.json")
			_ = os.MkdirAll(filepath.Dir(settings), 0o755)
			_ = os.WriteFile(settings, []byte(content), 0o644)
			_, code := setupRun(t, home, "--dir", dir)
			if code == 0 {
				t.Fatal("exit 0 on unmergeable settings")
			}
			if b, _ := os.ReadFile(settings); string(b) != content {
				t.Errorf("settings overwritten: %s", b)
			}
			if _, err := os.Stat(filepath.Join(dir, ".claude", "skills")); err == nil {
				t.Error("skill written despite failure")
			}
		})
	}
}

// Defends: a hand-edited skill is restored, and an existing hook elsewhere
// in the file is recognised so the hook is not added twice.
func TestSetupRepairsSkillAndDetectsHook(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	setupRun(t, home, "--dir", dir)
	skill := filepath.Join(dir, ".claude", "skills", "llm-extract", "SKILL.md")
	_ = os.WriteFile(skill, []byte("edited"), 0o644)
	out, _ := setupRun(t, home, "--dir", dir)
	if !strings.Contains(out, "wrote "+skill) {
		t.Errorf("skill not restored: %s", out)
	}
	b, _ := os.ReadFile(skill)
	if string(b) != setupSkill {
		t.Error("skill content wrong")
	}
	if strings.Count(mustJSON(readJSON(t, filepath.Join(dir, ".claude", "settings.json"))), `"llm-extract"`) != 1 {
		t.Error("hook duplicated")
	}
}

// Defends: unreadable paths (a directory where the file should be) surface
// as errors, not silent success.
func TestSetupReportsUnreadableSettings(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".claude", "settings.json"), 0o755)
	if _, code := setupRun(t, home, "--dir", dir); code == 0 {
		t.Error("exit 0 when settings.json is a directory")
	}
}

// TestCommittedSkillMatchesSetupSkill defends against the committed skill at
// integrations/claude-code/skill/SKILL.md drifting from the one `setup`
// writes, which would make `npx skills add` install a different skill.
func TestCommittedSkillMatchesSetupSkill(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "integrations", "claude-code", "skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != setupSkill {
		t.Errorf("committed SKILL.md differs from setupSkill:\n%s", b)
	}
	if !strings.HasPrefix(string(b), "---\nname: llm-extract\n") {
		t.Errorf("SKILL.md front matter must start with name: llm-extract")
	}
}
