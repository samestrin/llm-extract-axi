package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

var (
	setupDir     string
	setupDryRun  bool
	setupTargets = newTargetList()
)

// setupBinary is the name the SessionStart hooks run. A bare run prints the
// content-first home view, which is what an agent should see at session start.
const setupBinary = "llm-extract"

// setupLookPath and setupExecutable find the binary the hooks run; tests
// replace them.
var (
	setupLookPath   = exec.LookPath
	setupExecutable = os.Executable
)

// setupTarget is one agent setup can register with. plan returns the files
// the target needs, read and merged but not yet written.
type setupTarget struct {
	name string
	plan func(dir, bin string) ([]plannedWrite, error)
}

// setupTargetList is every target, in the order they are written. With no
// --target flag, setup writes all of them.
var setupTargetList = []setupTarget{
	{name: "claude", plan: planClaude},
	{name: "codex", plan: planCodex},
	{name: "opencode", plan: planOpenCode},
}

// setupSkill is the Agent Skill written to .claude/skills/llm-extract/SKILL.md.
const setupSkill = `---
name: llm-extract
description: Pull only what is needed from a large file or a web page instead of reading it whole. Use when a page, document or directory is too big to read in full.
---

# llm-extract

Run ` + "`llm-extract`" + ` with no arguments to see where the binary is and which commands reach outside.

- ` + "`llm-extract fetch <url>`" + ` fetches a URL. Network, no model.
- ` + "`llm-extract extract-links --url <url> [--context <text>]`" + ` lists and ranks links. Network; a model only with --context.
- ` + "`llm-extract extract-relevant --path <file|dir|url> --context <text>`" + ` reduces content to what the context needs. Always a model; network only for a URL.

Output is TOON by default; ` + "`--plain`" + ` prints human-readable text.

A failed fetch or model call exits non-zero with the reason on stdout in the active output format. Treat that as a failure, never as an empty result.
`

func newSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Register llm-extract with Claude Code, Codex and OpenCode in a project",
		Long: `Register llm-extract with coding agents in a project, under the project
directory:

  claude    a SessionStart hook in .claude/settings.json and an Agent Skill in
            .claude/skills/llm-extract/SKILL.md
  codex     a SessionStart hook in .codex/hooks.json
  opencode  a plugin in .opencode/plugins/llm-extract.js that adds the home
            view to each session

--target picks the agents (repeat it or give a comma list); the default is
all of them. The hooks run llm-extract by name when it is on PATH, and by
absolute path otherwise; running setup again after the binary moves points
them at the new path.

Existing settings keys and hooks are kept. Running it again changes nothing.
It never prompts, makes no network or model call, and never writes outside
the project directory (in particular, not to your home directory).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(cmd.OutOrStdout(), setupDir, setupTargets.names, setupDryRun)
		},
	}
	cmd.Flags().StringVar(&setupDir, "dir", ".", "Project directory to set up")
	cmd.Flags().BoolVar(&setupDryRun, "dry-run", false, "Print what would be written without writing")
	cmd.Flags().Var(setupTargets, "target", "Agent to set up: "+strings.Join(targetNames(), ", ")+" (repeatable)")
	return cmd
}

// runSetup applies every selected target's edits and reports each path. It
// plans everything before writing anything, so a malformed settings file in
// one target aborts before any target is written.
func runSetup(w io.Writer, dir string, targets []string, dryRun bool) error {
	bin, err := hookBinary()
	if err != nil {
		return err
	}
	var plans []plannedWrite
	for _, t := range setupTargetList {
		if !slices.Contains(targets, t.name) {
			continue
		}
		p, err := t.plan(dir, bin)
		if err != nil {
			return err
		}
		plans = append(plans, p...)
	}

	changed := false
	for _, p := range plans {
		verb := "unchanged"
		if p.changed {
			changed = true
			verb = "wrote"
			if dryRun {
				verb = "would write"
			}
			if !dryRun {
				if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
					return fmt.Errorf("create %s: %w", filepath.Dir(p.path), err)
				}
				if err := os.WriteFile(p.path, p.data, 0o644); err != nil {
					return fmt.Errorf("write %s: %w", p.path, err)
				}
			}
		}
		_, _ = fmt.Fprintf(w, "%s %s\n", verb, p.path)
	}
	if !changed {
		_, _ = fmt.Fprintln(w, "already set up; no changes")
	}
	return nil
}

// planClaude is the Claude Code target: the hook in .claude/settings.json and
// the Agent Skill.
func planClaude(dir, bin string) ([]plannedWrite, error) {
	settings, err := planHooks(filepath.Join(dir, ".claude", "settings.json"), shellQuote(bin))
	if err != nil {
		return nil, err
	}
	skill, err := planFile(filepath.Join(dir, ".claude", "skills", "llm-extract", "SKILL.md"), []byte(setupSkill))
	if err != nil {
		return nil, err
	}
	return []plannedWrite{settings, skill}, nil
}

// hookBinary is the binary the hooks run: the bare name when PATH finds it,
// otherwise this binary's absolute path. A hook naming a binary that cannot
// be found would fail at every session start, so failing to locate one is an
// error.
func hookBinary() (string, error) {
	if _, err := setupLookPath(setupBinary); err == nil {
		return setupBinary, nil
	}
	exe, err := setupExecutable()
	if err != nil {
		return "", fmt.Errorf("%s is not on PATH and its own path is unknown; add it to PATH and re-run: %w", setupBinary, err)
	}
	return exe, nil
}

// shellQuote single-quotes s unless every byte is safe unquoted. A
// backslash is not safe: unquoted, POSIX sh reads it as an escape, so a hook
// naming /opt/my\tools/llm-extract would run /opt/mytools/llm-extract and
// fail at every session start. The quoting is for POSIX sh only; cmd.exe
// and PowerShell are not supported (the README states the limit).
func shellQuote(s string) string {
	safe := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-./:", r)
	}
	if strings.IndexFunc(s, func(r rune) bool { return !safe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isOurCommand reports whether a hook command runs llm-extract with no
// arguments, by bare name or by any path, quoted or not. That is how a rerun
// finds the entry it wrote before the binary moved.
func isOurCommand(c string) bool {
	if len(c) >= 2 && strings.HasPrefix(c, "'") && strings.HasSuffix(c, "'") {
		c = strings.ReplaceAll(c[1:len(c)-1], `'\''`, "'")
	}
	base := strings.TrimSuffix(filepath.Base(c), ".exe")
	return base == setupBinary && (c == setupBinary || filepath.IsAbs(c))
}

type plannedWrite struct {
	path    string
	data    []byte
	changed bool
}

func planFile(path string, want []byte) (plannedWrite, error) {
	have, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return plannedWrite{}, fmt.Errorf("read %s: %w", path, err)
	}
	return plannedWrite{path: path, data: want, changed: err != nil || !bytes.Equal(have, want)}, nil
}

// planHooks merges the SessionStart hook into a settings file with a "hooks"
// object, the shape Claude Code's settings.json and Codex's hooks.json share.
// An entry of ours that runs a different path, because the binary moved, is
// pointed at command. A file that is not a JSON object, or whose hooks do not
// have the expected shape, is an error: guessing would mean overwriting the
// user's configuration.
func planHooks(path, command string) (plannedWrite, error) {
	settings := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return plannedWrite{}, fmt.Errorf("read %s: %w", path, err)
	default:
		if err := json.Unmarshal(raw, &settings); err != nil || settings == nil {
			return plannedWrite{}, fmt.Errorf("%s is not a JSON object; fix or remove it and re-run: %v", path, err)
		}
	}

	hooks, err := childMap(settings, "hooks", path)
	if err != nil {
		return plannedWrite{}, err
	}
	var groups []any
	if v, ok := hooks["SessionStart"]; ok {
		if groups, ok = v.([]any); !ok {
			return plannedWrite{}, fmt.Errorf(`%s: "hooks.SessionStart" is not a list`, path)
		}
	}
	found, repaired := repairHooks(groups, command)
	if found && !repaired {
		return plannedWrite{path: path, data: raw}, nil
	}
	if !found {
		groups = append(groups, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": command}},
		})
	}
	hooks["SessionStart"] = groups
	settings["hooks"] = hooks

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return plannedWrite{}, err
	}
	return plannedWrite{path: path, data: append(data, '\n'), changed: true}, nil
}

func childMap(m map[string]any, key, path string) (map[string]any, error) {
	v, ok := m[key]
	if !ok {
		return map[string]any{}, nil
	}
	c, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: %q is not an object", path, key)
	}
	return c, nil
}

// repairHooks finds the SessionStart entries that run llm-extract and points
// any that run something other than command at command, in place.
func repairHooks(groups []any, command string) (found, repaired bool) {
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			c, _ := hm["command"].(string)
			if !isOurCommand(c) {
				continue
			}
			found = true
			if c != command {
				hm["command"] = command
				repaired = true
			}
		}
	}
	return found, repaired
}

// targetNames lists the valid --target values.
func targetNames() []string {
	names := make([]string, len(setupTargetList))
	for i, t := range setupTargetList {
		names[i] = t.name
	}
	return names
}

// targetList is the --target flag. It rejects an unknown target while
// parsing, so cobra reports it as an invalid argument (exit 2) before
// anything is planned. The first Set replaces the default; later ones add.
type targetList struct {
	names   []string
	changed bool
}

func newTargetList() *targetList { return &targetList{names: targetNames()} }

func (l *targetList) String() string { return "[" + strings.Join(l.names, ",") + "]" }

func (l *targetList) Type() string { return "target" }

func (l *targetList) Set(v string) error {
	var add []string
	for _, n := range strings.Split(v, ",") {
		n = strings.TrimSpace(n)
		if !slices.Contains(targetNames(), n) {
			return fmt.Errorf("unknown target %q; use one of: %s", n, strings.Join(targetNames(), ", "))
		}
		add = append(add, n)
	}
	if !l.changed {
		l.names = nil
		l.changed = true
	}
	l.names = append(l.names, add...)
	return nil
}

func (l *targetList) Append(v string) error { return l.Set(v) }

func (l *targetList) Replace(vs []string) error {
	l.names = append([]string(nil), vs...)
	l.changed = false
	return nil
}

func (l *targetList) GetSlice() []string { return append([]string(nil), l.names...) }

func init() {
	RootCmd.AddCommand(newSetupCmd())
}
