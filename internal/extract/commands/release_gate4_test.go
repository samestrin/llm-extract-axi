package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	goaxi "github.com/samestrin/go-axi"
)

// These tests defend the four behaviour findings of the EST-6628 release
// gate (epic 06 AC5): hook quoting of a backslash path, one exit code for an
// unusable key configuration across both model commands, and key-error help
// that states the pairing rule.

// backslashBinary writes an executable shell script at <tmp>/my\tools/llm-extract
// and returns its path. POSIX sh reads an unquoted backslash as an escape, so
// a hook naming this path unquoted runs <tmp>/mytools/llm-extract instead.
func backslashBinary(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), `my\tools`)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "llm-extract")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho hook-ran-ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// Defends F5: shellQuote treated a backslash as safe unquoted, so a binary
// at a path like /opt/my\tools/llm-extract produced a hook that sh ran as
// /opt/mytools/llm-extract and failed at every session start. The hook must
// run the real binary through a POSIX shell, for both shell-hook targets,
// and a rerun must recognise the quoted entry and change nothing.
func TestSetupHookRunsBackslashPathThroughSh(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	exe := backslashBinary(t)
	if out, code := setupRunAt(t, home, exe, "--dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, p := range []string{filepath.Join(dir, ".claude", "settings.json"), codexHooksPath(dir)} {
		cmds := sessionStartCommands(t, p)
		if len(cmds) != 1 {
			t.Fatalf("%s: SessionStart commands = %q, want one", p, cmds)
		}
		out, err := exec.Command("sh", "-c", cmds[0]).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "hook-ran-ok") {
			t.Errorf("%s: sh -c %q did not run the binary: %v\n%s", p, cmds[0], err, out)
		}
	}
	out, _ := setupRunAt(t, home, exe, "--dir", dir)
	if !strings.Contains(out, "already set up; no changes") {
		t.Errorf("rerun at the quoted backslash path changed something: %s", out)
	}
}

// Defends F5's rerun half: a hook written by the old code carries the broken
// unquoted backslash path. A rerun must recognise it as ours and repair it to
// the quoted form, not report it unchanged and leave it failing.
func TestSetupRepairsLegacyUnquotedBackslashHook(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	exe := backslashBinary(t)
	legacy := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":` + mustJSON(exe) + `}]}]}}`
	for _, p := range []string{filepath.Join(dir, ".claude", "settings.json"), codexHooksPath(dir)} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, code := setupRunAt(t, home, exe, "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	want := "'" + exe + "'"
	for _, p := range []string{filepath.Join(dir, ".claude", "settings.json"), codexHooksPath(dir)} {
		if !strings.Contains(out, "wrote "+p) {
			t.Errorf("legacy broken hook in %s not repaired: %s", p, out)
		}
		if cmds := sessionStartCommands(t, p); len(cmds) != 1 || cmds[0] != want {
			t.Errorf("%s: commands = %q, want [%s]", p, cmds, want)
		}
	}
}

// Defends F5 at the unit level, both ways: a backslash forces quoting, and
// the known-good paths that never needed quoting stay unquoted.
func TestShellQuoteBackslashIsNotSafeUnquoted(t *testing.T) {
	if got, want := shellQuote(`/opt/my\tools/llm-extract`), `'/opt/my\tools/llm-extract'`; got != want {
		t.Errorf("shellQuote(backslash path) = %s, want %s", got, want)
	}
	for _, s := range []string{"llm-extract", "/usr/local/bin/llm-extract", "/opt/my-tools_v1.2/llm-extract", "C:", "a-b_c./d"} {
		if got := shellQuote(s); got != s {
			t.Errorf("shellQuote(%q) = %s, want it unquoted", s, got)
		}
	}
}

// Defends F3: the same unusable key configuration exited 2 from
// extract-relevant but 1 from extract-links --context, because the ranking
// error was rebuilt with errors.New and lost its usage marker. Exit 1 tells
// an agent a retry may help; both commands must exit 2 for both key-config
// errors, in every output mode, without reaching the default endpoint.
func TestUnusableKeyConfigExitsUsageFromBothCommands(t *testing.T) {
	for _, tc := range []struct {
		name              string
		openai, anthropic string
	}{
		{"no key", "", ""},
		{"anthropic key without base URL", "", "sk-ant-must-not-leave"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", tc.openai)
			t.Setenv("ANTHROPIC_API_KEY", tc.anthropic)
			t.Setenv("OPENAI_BASE_URL", "")
			_, calls := standInForDefaultURL(t)
			page := linksPage(t)
			doc := writeDoc(t)
			for _, mode := range []string{"--json", "--plain", "--axi"} {
				code, out, stderr := runCLI(t, "extract-links", "--url", page, "--context", "start", mode)
				if code != int(goaxi.ExitUsage) {
					t.Errorf("extract-links %s exited %d, want %d (usage)\nstdout=%s\nstderr=%s", mode, code, goaxi.ExitUsage, out, stderr)
				}
				code, out, stderr = runCLI(t, "extract-relevant", "--path", doc, "--context", "c", mode)
				if code != int(goaxi.ExitUsage) {
					t.Errorf("extract-relevant %s exited %d, want %d (usage)\nstdout=%s\nstderr=%s", mode, code, goaxi.ExitUsage, out, stderr)
				}
			}
			if n := calls(); n != 0 {
				t.Errorf("the default endpoint received %d model calls, want 0", n)
			}
		})
	}
}

// Defends F3's other side: a runtime ranking failure (the model answered
// 500) is still exit 1, so the usage classification has not swallowed
// ordinary failures a retry may fix.
func TestRankingRuntimeFailureStillExitsOne(t *testing.T) {
	failingLLM(t, 500)
	code, out, _ := runCLI(t, "extract-links", "--url", linksPage(t), "--context", "start", "--json")
	if code != 1 {
		t.Errorf("a model 500 during ranking exited %d, want 1:\n%s", code, out)
	}
}

// Defends F4: the help[] for a missing key said "Set OPENAI_API_KEY or
// ANTHROPIC_API_KEY", advice that fails again with ErrBaseURLRequired when
// only the Anthropic key is set. It must state the pairing rule, and the
// Anthropic-without-base-URL error must get its own help naming
// OPENAI_BASE_URL instead of the generic usage line.
func TestKeyErrorHelpStatesThePairingRule(t *testing.T) {
	noAPIKey(t)
	t.Setenv("OPENAI_BASE_URL", "")
	_, out, _ := runCLI(t, "extract-relevant", "--path", writeDoc(t), "--context", "c", "--axi")
	help := strings.Join(strs(t, axiError(t, out), "help"), "\n")
	if !strings.Contains(help, "OPENAI_API_KEY") || !strings.Contains(help, "ANTHROPIC_API_KEY with OPENAI_BASE_URL") {
		t.Errorf("missing-key help does not state the pairing rule: %q", help)
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-probe")
	_, out, _ = runCLI(t, "extract-relevant", "--path", writeDoc(t), "--context", "c", "--axi")
	help = strings.Join(strs(t, axiError(t, out), "help"), "\n")
	if !strings.Contains(help, "OPENAI_BASE_URL") {
		t.Errorf("Anthropic-without-base-URL help does not name OPENAI_BASE_URL: %q", help)
	}
}

// Defends F4 on extract-links: its ranking-failure help[] carried the same
// stale "OPENAI_API_KEY or ANTHROPIC_API_KEY" advice, which cannot fix an
// Anthropic key with no endpoint. It must state the pairing rule too.
func TestExtractLinksRankingFailureHelpStatesThePairingRule(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-probe")
	t.Setenv("OPENAI_BASE_URL", "")
	code, out, _ := runCLI(t, "extract-links", "--url", linksPage(t), "--context", "start", "--axi")
	if code == 0 {
		t.Fatalf("ranking with an unusable key exited 0:\n%s", out)
	}
	h := helpOf(t, out)
	if len(h) == 0 {
		t.Fatalf("no help[] in:\n%s", out)
	}
	first, _ := h[0].(string)
	if !strings.Contains(first, "OPENAI_BASE_URL") {
		t.Errorf("ranking-failure help does not state the pairing rule: %q", first)
	}
}
