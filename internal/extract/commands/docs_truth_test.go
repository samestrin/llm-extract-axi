package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// These tests defend the user-facing docs (README.md, docs/output.md and the
// committed Agent Skill) against the epic 09 drift they carried once: the
// docs described text as the default, errors on stderr, help[] as --axi-only
// and setup as Claude-Code-only, while the code had moved to TOON by default,
// errors on stdout and three setup targets. Each test first probes the live
// behaviour through run, then fails on a doc that claims the opposite, so a
// doc cannot pass by staying silent while the code changes under it.

// TestDocsMatchTheTOONDefault defends the default-output claim: a bare run is
// TOON with help[], so no doc may describe text as the default or --axi as
// the switch that turns TOON on, and each output doc must name --plain, the
// flag that selects text.
func TestDocsMatchTheTOONDefault(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 0 {
		t.Fatalf("bare run exited %d: %s", code, errb.String())
	}
	if json.Valid(out.Bytes()) || !strings.Contains(out.String(), "help[") {
		t.Fatalf("bare run is not TOON with help[]; the probe, not the docs, is wrong:\n%s", out.String())
	}
	for _, doc := range []string{"README.md", "docs/output.md"} {
		s := readRepoFile(t, doc)
		if !strings.Contains(s, "--plain") {
			t.Errorf("%s never names --plain, the flag that selects text output", doc)
		}
		for _, stale := range []string{
			"text by default",
			"Text (default)",
			"switches output to TOON",
			"next steps appear only",
		} {
			if strings.Contains(s, stale) {
				t.Errorf("%s claims %q, but TOON with help[] is the default", doc, stale)
			}
		}
	}
}

// TestDocsSayErrorsGoToStdout defends the error-stream claim: an error is on
// stdout in the active format with stderr empty, so no doc may put the
// failure reason on stderr, and the Agent Skill must say stdout.
func TestDocsSayErrorsGoToStdout(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--bogus"}, &out, &errb); code != 2 {
		t.Fatalf("unknown flag exited %d, want 2", code)
	}
	if out.Len() == 0 || errb.Len() != 0 {
		t.Fatalf("error not on stdout with empty stderr; the probe, not the docs, is wrong: stdout=%q stderr=%q", out.String(), errb.String())
	}
	for _, doc := range []string{"README.md", "docs/output.md", "integrations/claude-code/skill/SKILL.md"} {
		s := readRepoFile(t, doc)
		for _, stale := range []string{
			"reason on stderr",
			"reason is on stderr",
			"print on stderr",
			"errors stay on stderr",
		} {
			if strings.Contains(s, stale) {
				t.Errorf("%s claims %q, but errors are on stdout in every format", doc, stale)
			}
		}
	}
	if s := readRepoFile(t, "integrations/claude-code/skill/SKILL.md"); !strings.Contains(s, "on stdout") {
		t.Error("the Agent Skill does not say the failure reason is on stdout")
	}
}

// TestReadmeNamesEverySetupTarget defends the setup claim: the default setup
// plans a write for every registered target, so the README must name each
// target and the --target flag, and must not claim setup is Claude-Code-only.
// A new target registered in setupTargetList without a README mention fails
// here too.
func TestReadmeNamesEverySetupTarget(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"setup", "--dir", dir, "--dry-run"}, &out, &errb); code != 0 {
		t.Fatalf("setup --dry-run exited %d: %s", code, errb.String())
	}
	display := map[string]string{"claude": "Claude Code", "codex": "Codex", "opencode": "OpenCode"}
	for _, name := range targetNames() {
		if !strings.Contains(out.String(), "."+name) {
			t.Fatalf("default setup did not plan the %s target; the probe, not the docs, is wrong:\n%s", name, out.String())
		}
	}
	readme := readRepoFile(t, "README.md")
	for _, name := range targetNames() {
		want, ok := display[name]
		if !ok {
			t.Fatalf("setup target %q has no display name in this test; document it in the README and add it here", name)
		}
		if !strings.Contains(readme, want) {
			t.Errorf("README does not name the %s setup target", want)
		}
	}
	if !strings.Contains(readme, "--target") {
		t.Error("README does not name setup's --target flag")
	}
	for _, stale := range []string{"targets Claude Code only", "no Codex or OpenCode target"} {
		if strings.Contains(readme, stale) {
			t.Errorf("README claims %q, but setup registers every target by default", stale)
		}
	}
	var help, helpErr bytes.Buffer
	if code := run([]string{"setup", "--help"}, &help, &helpErr); code != 0 {
		t.Fatalf("setup --help exited %d: %s", code, helpErr.String())
	}
	if !strings.Contains(help.String(), "--target") {
		t.Error("setup --help does not list --target")
	}
	for _, name := range targetNames() {
		if !strings.Contains(help.String(), name) {
			t.Errorf("setup --help does not name the %s target", name)
		}
	}
}
