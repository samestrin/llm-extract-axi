package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// countingLLM is fakeLLM that counts the requests it answers and records the
// prompts, so a test can prove a secret never reached the model.
func countingLLM(t *testing.T, reply string) (calls *atomic.Int32, prompts func() string) {
	t.Helper()
	var n atomic.Int32
	var mu sync.Mutex
	var seen []string
	srv := fakeLLM(t, func(p string) string {
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
		n.Add(1)
		return reply
	})
	pointExtractRelevantAt(t, srv)
	return &n, func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(seen, "\n")
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Defends EST-8259 item 1: --path defaulted to ".", so a bare
// `extract-relevant --context x` in a project sent the directory -- .env
// included -- to a model. A missing --path is now a usage error, and no model
// call is made.
func TestExtractRelevantRequiresPath(t *testing.T) {
	calls, _ := countingLLM(t, "SECRET")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".env": "API_TOKEN=hunter2", "notes.txt": "notes"})
	t.Chdir(dir)

	for _, args := range [][]string{
		{"extract-relevant", "--context", "x"},
		{"extract-relevant", "--context", "x", "--path", ""},
	} {
		code, out, _ := runCLI(t, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2:\n%s", args, code, out)
		}
		if !strings.Contains(out, "path") {
			t.Errorf("%v: error does not name --path:\n%s", args, out)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("%d model calls made with no --path", n)
	}
}

// Defends EST-8259 item 1: a directory run read .env files and every other
// dotfile, so secrets went to the model. A directory run now skips dotfiles,
// dot directories and *.env; naming one explicitly still reads it.
func TestExtractRelevantDirectorySkipsDotfilesAndEnv(t *testing.T) {
	calls, prompts := countingLLM(t, "NONE")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".env":             "SECRET_DOTENV",
		"prod.env":         "SECRET_PRODENV",
		".secrets.json":    `{"k":"SECRET_DOTJSON"}`,
		".aws/config.toml": "SECRET_DOTDIR",
		"notes.txt":        "plain notes",
	})

	code, out, _ := runCLI(t, "extract-relevant", "--path", dir, "--context", "x", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d model calls, want 1 (notes.txt only)", n)
	}
	if p := prompts(); strings.Contains(p, "SECRET") || !strings.Contains(p, "plain notes") {
		t.Errorf("prompts sent:\n%s", p)
	}
	var got ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.TotalFiles != 1 {
		t.Errorf("total_files = %d (%v), want 1:\n%s", got.TotalFiles, err, out)
	}
}

// Defends the other half of item 1: the skip is for a directory walk only. A
// dotfile or dot directory the caller names is read, because they asked.
func TestExtractRelevantReadsNamedDotfile(t *testing.T) {
	calls, prompts := countingLLM(t, "found")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".notes.md": "NAMED_DOTFILE", ".docs/a.md": "NAMED_DOTDIR"})

	for _, p := range []string{filepath.Join(dir, ".notes.md"), filepath.Join(dir, ".docs")} {
		if code, out, _ := runCLI(t, "extract-relevant", "--path", p, "--context", "x", "--json"); code != 0 {
			t.Fatalf("%s: exit %d:\n%s", p, code, out)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("%d model calls, want 2", n)
	}
	if p := prompts(); !strings.Contains(p, "NAMED_DOTFILE") || !strings.Contains(p, "NAMED_DOTDIR") {
		t.Errorf("named dotfiles not read:\n%s", p)
	}
}

// Defends EST-8259 item 2: setup with --dir at the home directory, or run from
// home with the default --dir, wrote ~/.claude/settings.json and
// ~/.codex/hooks.json -- user-wide hooks for every session -- though its help
// says it never touches home. It is refused with exit 2 and writes nothing,
// including through a symlink to home.
func TestSetupRefusesHomeDirectory(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"--dir home":    {"--dir", home},
		"--dir symlink": {"--dir", link},
		"--dir home/.":  {"--dir", home + "/."},
		"cwd is home":   nil,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			t.Chdir(home)
			out, code := setupRun(t, home, args...)
			if code != 2 {
				t.Errorf("exit %d, want 2:\n%s", code, out)
			}
			if !strings.Contains(out, "home directory") {
				t.Errorf("error does not say why:\n%s", out)
			}
			if entries, _ := os.ReadDir(home); len(entries) != 0 {
				t.Errorf("home was written: %v", entries)
			}
		})
	}
}

// Defends EST-8259 item 2: a mistyped --dir created the whole tree with
// MkdirAll, so a typo silently set up a project nobody has. --dir must be an
// existing directory; anything else is exit 2 and nothing is created.
func TestSetupRequiresExistingDirectory(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"missing": filepath.Join(base, "no", "such", "project"),
		"a file":  file,
	} {
		t.Run(name, func(t *testing.T) {
			out, code := setupRun(t, t.TempDir(), "--dir", dir)
			if code != 2 {
				t.Errorf("exit %d, want 2:\n%s", code, out)
			}
			if _, err := os.Stat(filepath.Join(base, "no")); err == nil {
				t.Error("setup created the missing directory")
			}
		})
	}
}
