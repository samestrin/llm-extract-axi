package commands

import "path/filepath"

// planCodex is the Codex target: a SessionStart hook in the project's
// .codex/hooks.json, which has the same "hooks" shape as Claude Code's
// settings. Codex runs a SessionStart command's stdout as developer context,
// and hooks are on by default, so no [features] entry in config.toml is
// written. Format: https://developers.openai.com/codex/hooks
func planCodex(dir, bin string) ([]plannedWrite, error) {
	p, err := planHooks(filepath.Join(dir, ".codex", "hooks.json"), shellQuote(bin))
	if err != nil {
		return nil, err
	}
	return []plannedWrite{p}, nil
}
