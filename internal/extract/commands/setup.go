package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	setupDir    string
	setupDryRun bool
)

// setupHookCommand is what the SessionStart hook runs. A bare run prints the
// content-first home view, which is what an agent should see at session start.
const setupHookCommand = "llm-extract"

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

A failed fetch or model call exits non-zero with the reason on stderr. Treat that as a failure, never as an empty result.
`

func newSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install the Claude Code SessionStart hook and Agent Skill in a project",
		Long: `Write a SessionStart hook entry into .claude/settings.json and an Agent
Skill into .claude/skills/llm-extract/SKILL.md, under the project directory.

Existing settings keys and hooks are kept. Running it again changes nothing.
It never prompts, makes no network or model call, and never writes outside
the project directory (in particular, not to your home directory).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(cmd.OutOrStdout(), setupDir, setupDryRun)
		},
	}
	cmd.Flags().StringVar(&setupDir, "dir", ".", "Project directory to set up")
	cmd.Flags().BoolVar(&setupDryRun, "dry-run", false, "Print what would be written without writing")
	return cmd
}

// runSetup applies both edits and reports each path. It plans everything
// before writing anything, so a malformed settings.json aborts before the
// skill is written.
func runSetup(w io.Writer, dir string, dryRun bool) error {
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	skillPath := filepath.Join(dir, ".claude", "skills", "llm-extract", "SKILL.md")

	settings, err := planSettings(settingsPath)
	if err != nil {
		return err
	}
	skill, err := planFile(skillPath, []byte(setupSkill))
	if err != nil {
		return err
	}

	changed := false
	for _, p := range []plannedWrite{settings, skill} {
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

// planSettings merges the hook into settings.json. A file that is not a JSON
// object, or whose hooks do not have the expected shape, is an error: guessing
// would mean overwriting the user's configuration.
func planSettings(path string) (plannedWrite, error) {
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
	if hasHook(groups) {
		return plannedWrite{path: path, data: raw}, nil
	}
	groups = append(groups, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": setupHookCommand}},
	})
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

// hasHook reports whether any SessionStart group already runs our command.
func hasHook(groups []any) bool {
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if hm["command"] == setupHookCommand {
				return true
			}
		}
	}
	return false
}

func init() {
	RootCmd.AddCommand(newSetupCmd())
}
