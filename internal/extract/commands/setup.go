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

	"github.com/samestrin/llm-extract-axi/v2/pkg/output"
)

var (
	setupDir     string
	setupDryRun  bool
	setupJSON    bool
	setupTargets = newTargetList()
)

// setupResult is what setup reports: the files it wrote (or, under
// --dry-run, would write) and the ones already right. The lists are never
// nil, so an empty one prints as an empty list. Help is set under TOON only.
type setupResult struct {
	Written   []string `json:"written"`
	Unchanged []string `json:"unchanged"`
	DryRun    bool     `json:"dry_run"`
	Help      []string `json:"help,omitempty"`
}

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

Run ` + "`llm-extract`" + ` with no arguments to see where the binary is and which commands reach outside. If ` + "`llm-extract`" + ` is not on PATH, install it first: ` + "`curl -fsSL https://raw.githubusercontent.com/samestrin/llm-extract-axi/main/install.sh | bash`" + ` puts it in ` + "`~/.local/bin`" + `; this skill only calls it by name.

- ` + "`llm-extract fetch <url>`" + ` fetches a URL. Network, no model. Prints a TOON envelope: ` + "`bytes`" + ` is the whole-body size, ` + "`truncated`" + ` says the body was cut, and ` + "`body`" + ` holds the first 4096 bytes. ` + "`--head N`" + ` sets the cut, ` + "`--full`" + ` returns the whole body, ` + "`--fields a,b`" + ` selects envelope fields, ` + "`--output <file>`" + ` saves the whole body to a file. ` + "`--format markdown`" + ` makes the body the main content of an HTML page as Markdown, with no model: ` + "`bytes`" + ` and ` + "`truncated`" + ` then describe the Markdown, ` + "`source_bytes`" + ` the fetched HTML, and ` + "`title`" + `/` + "`byline`" + ` come from the page. A page that is not HTML, or has no main content, is an exit-1 error whose ` + "`help`" + ` line names running again without ` + "`--format markdown`" + `, never an empty body. A body that is not UTF-8 text (an image, a PDF) comes back as ` + "`body: \"\"`" + ` with ` + "`truncated: true`" + `; save its bytes with ` + "`--output <file>`" + `.
- ` + "`llm-extract extract-links --url <url> [--context <text>]`" + ` lists and ranks links. Network; a model only with --context.
- ` + "`llm-extract extract-relevant --path <file|dir|url> --context <text>`" + ` reduces content to what the context needs. Always a model; network only for a URL.

Output is TOON by default; ` + "`--plain`" + ` prints human-readable text (for ` + "`fetch`" + `, the body bytes alone, still cut unless ` + "`--full`" + `; with ` + "`--output <file>`" + `, the line ` + "`Saved <n> bytes [HTTP <status> via <tier>, <format>] -> <file>`" + `).

A failed fetch or model call exits non-zero with ` + "`error`" + ` and the reason on stdout in the active output format; it is never reported as an empty result. A failed run may still carry results beside ` + "`error`" + ` (the unranked links when ` + "`extract-links --context`" + ` has no API key, the files a directory run finished): treat them as partial. Exit 2 means the command or its configuration is wrong (a bad flag, no API key): fix that before running again. Exit 1 is a run that failed, such as a fetch the server refused; the ` + "`help`" + ` lines name the next step.
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

Existing settings keys and hooks are kept, in their order and with their
numbers as written. Running it again changes nothing. SKILL.md and the
OpenCode plugin are setup's own files: a hand edit to either is overwritten
on the next run, so keep your own notes elsewhere.

--dir must be an existing directory, and not your home directory: hooks there
would run in every session. setup never prompts, makes no network or model
call, and never writes outside the project directory: a .claude, .codex or
.opencode (or a settings file) that links outside it is refused (exit 2).

The result is a TOON document by default: written[] (the files written, or
under --dry-run the files that would be), unchanged[], dry_run and help[].
--json prints it as JSON; --plain prints one "wrote", "would write" or
"unchanged" line per file.

Examples:
  # Show what would be written for every agent, writing nothing
  llm-extract setup --dry-run

  # Register with Claude Code only, in another project
  llm-extract setup --target claude --dir ../my-project`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(cmd.OutOrStdout(), setupDir, setupTargets.names, setupDryRun)
		},
	}
	cmd.Flags().BoolVar(&setupJSON, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&setupDir, "dir", ".", "Project directory to set up")
	cmd.Flags().BoolVar(&setupDryRun, "dry-run", false, "Print what would be written without writing")
	cmd.Flags().Var(setupTargets, "target", "Agent to set up: "+strings.Join(targetNames(), ", ")+" (repeatable)")
	return cmd
}

// runSetup applies every selected target's edits and reports each path. It
// plans everything before writing anything, so a malformed settings file in
// one target aborts before any target is written.
func runSetup(w io.Writer, dir string, targets []string, dryRun bool) error {
	if err := checkSetupDir(dir); err != nil {
		return err
	}
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
	// Checked for every file before any is written, under --dry-run too.
	for _, p := range plans {
		if err := checkInsideDir(dir, p.path); err != nil {
			return err
		}
	}

	res := setupResult{Written: []string{}, Unchanged: []string{}, DryRun: dryRun}
	for _, p := range plans {
		if !p.changed {
			res.Unchanged = append(res.Unchanged, p.path)
			continue
		}
		res.Written = append(res.Written, p.path)
		if dryRun {
			continue
		}
		// The project directory exists (checkSetupDir); this creates only
		// .claude, .codex and .opencode inside it.
		if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(p.path), err)
		}
		if err := os.WriteFile(p.path, p.data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p.path, err)
		}
	}
	return printSetupResult(w, res)
}

// printSetupResult writes the result as JSON under --json, TOON with help[]
// by default, and one line per file under --plain.
func printSetupResult(w io.Writer, res setupResult) error {
	if setupJSON {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	if GlobalAXIOutput {
		res.Help = setupHelp(res)
		var doc bytes.Buffer
		if err := output.EncodeAXI(&doc, res); err != nil {
			return err
		}
		_, err := w.Write(doc.Bytes())
		return err
	}
	verb := "wrote"
	if res.DryRun {
		verb = "would write"
	}
	for _, p := range res.Written {
		_, _ = fmt.Fprintf(w, "%s %s\n", verb, p)
	}
	for _, p := range res.Unchanged {
		_, _ = fmt.Fprintf(w, "unchanged %s\n", p)
	}
	if len(res.Written) == 0 {
		_, _ = fmt.Fprintln(w, "already set up; no changes")
	}
	return nil
}

// setupHelp is the help[] block: what to do after a dry run, a write, or a
// run that found everything in place.
func setupHelp(res setupResult) []string {
	switch {
	case res.DryRun && len(res.Written) > 0:
		return []string{"Run again without --dry-run to write these files"}
	case len(res.Written) > 0:
		return []string{"Start a new agent session in this project to load the hook", "Run 'llm-extract setup' again after the binary moves"}
	default:
		return []string{"Already set up; nothing to do"}
	}
}

// checkSetupDir refuses a --dir that is not an existing directory, or that is
// the home directory. A mistyped path must not grow a project tree nobody
// has, and hooks under ~/.claude or ~/.codex run in every session, not one
// project's. Both are usage errors (exit 2): the same invocation never
// succeeds. Symlinks are resolved, so a link to home is caught too.
func checkSetupDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return output.UsageError(fmt.Errorf("--dir %s: %w; pass an existing project directory", dir, err))
	}
	if !info.IsDir() {
		return output.UsageError(fmt.Errorf("--dir %s is not a directory; pass an existing project directory", dir))
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	if samePath(dir, home) {
		return output.UsageError(fmt.Errorf("--dir %s is your home directory: setup writes per-project hooks, and in home they would run in every session; run it in a project directory", dir))
	}
	return nil
}

// checkInsideDir refuses a path that resolves outside dir. A symlinked
// .claude, .codex or .opencode -- or a linked settings file -- would make
// setup write through it, and .claude -> ~/.claude installs a hook for every
// session. A path that does not exist yet is judged by its nearest existing
// parent; a dangling link is refused, since writing it creates its target.
func checkInsideDir(dir, path string) error {
	root, err := resolvePath(dir)
	if err != nil {
		return output.UsageError(fmt.Errorf("--dir %s: %w", dir, err))
	}
	real, err := resolvePath(path)
	if err == nil && !within(root, real) {
		err = fmt.Errorf("it resolves to %s", real)
	}
	if err != nil {
		return output.UsageError(fmt.Errorf("%s is outside --dir %s (%v): setup never writes outside the project; remove the symlink or run setup in that directory", path, dir, err))
	}
	return nil
}

// resolvePath makes p absolute and resolves its symlinks. The part of p that
// does not exist yet is joined on unresolved; a link whose target is missing
// is an error.
func resolvePath(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	var rest []string
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(append([]string{real}, rest...)...), nil
		}
		if _, lerr := os.Lstat(p); lerr == nil {
			return "", fmt.Errorf("%s exists but cannot be resolved: %w", p, err)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}

// samePath reports whether a and b name the same directory once made
// absolute and resolved through symlinks.
func samePath(a, b string) bool {
	resolve := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return filepath.Clean(p)
	}
	return resolve(a) == resolve(b)
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
	settings := newJSONObject()
	indent := "  "
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return plannedWrite{}, fmt.Errorf("read %s: %w", path, err)
	default:
		// Decoded in order and with numbers kept as written, so the merge
		// rewrites only what it must: the user's keys stay where they were
		// and 12345678901234567890 is not rounded through float64.
		if settings, err = decodeJSONObject(raw); err != nil {
			return plannedWrite{}, fmt.Errorf("%s is not a JSON object; fix or remove it and re-run: %v", path, err)
		}
		indent = detectIndent(raw)
	}

	hooks, err := childObject(settings, "hooks", path)
	if err != nil {
		return plannedWrite{}, err
	}
	var groups []any
	if v, ok := hooks.get("SessionStart"); ok {
		if groups, ok = v.([]any); !ok {
			return plannedWrite{}, fmt.Errorf(`%s: "hooks.SessionStart" is not a list`, path)
		}
	}
	found, repaired := repairHooks(groups, command)
	if found && !repaired {
		return plannedWrite{path: path, data: raw}, nil
	}
	if !found {
		hook := newJSONObject()
		hook.set("type", "command")
		hook.set("command", command)
		group := newJSONObject()
		group.set("hooks", []any{hook})
		groups = append(groups, group)
	}
	hooks.set("SessionStart", groups)
	settings.set("hooks", hooks)

	data, err := marshalJSONObject(settings, indent)
	if err != nil {
		return plannedWrite{}, err
	}
	return plannedWrite{path: path, data: data, changed: true}, nil
}

func childObject(m *jsonObject, key, path string) (*jsonObject, error) {
	v, ok := m.get(key)
	if !ok {
		return newJSONObject(), nil
	}
	c, ok := v.(*jsonObject)
	if !ok {
		return nil, fmt.Errorf("%s: %q is not an object", path, key)
	}
	return c, nil
}

// repairHooks finds the SessionStart entries that run llm-extract and points
// any that run something other than command at command, in place.
func repairHooks(groups []any, command string) (found, repaired bool) {
	for _, g := range groups {
		gm, ok := g.(*jsonObject)
		if !ok {
			continue
		}
		v, _ := gm.get("hooks")
		hs, _ := v.([]any)
		for _, h := range hs {
			hm, ok := h.(*jsonObject)
			if !ok {
				continue
			}
			cv, _ := hm.get("command")
			c, _ := cv.(string)
			if !isOurCommand(c) {
				continue
			}
			found = true
			if c != command {
				hm.set("command", command)
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
