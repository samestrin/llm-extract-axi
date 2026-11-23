package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// Defends EST-8259 item 3: setup round-tripped settings.json through
// map[string]any, so 12345678901234567890 came back as 12345678901234567000
// and every key was re-sorted. A merge keeps the user's numbers exact, their
// key order and their indent.
func TestSetupKeepsSettingsBytesStable(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.json")
	orig := "{\n    \"zeta\": 12345678901234567890,\n    \"alpha\": {\n        \"y\": 1.50,\n        \"b\": \"<&>\"\n    },\n    \"hooks\": {\n        \"PreToolUse\": []\n    }\n}\n"
	writeFiles(t, dir, map[string]string{".claude/settings.json": orig})

	if out, code := setupRun(t, home, "--dir", dir, "--target", "claude"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"12345678901234567890", "1.50", `"<&>"`, "\n    \"zeta\"", "\n        \"y\""} {
		if !strings.Contains(got, want) {
			t.Errorf("settings lost %q:\n%s", want, got)
		}
	}
	order := []string{`"zeta"`, `"alpha"`, `"y"`, `"b"`, `"hooks"`, `"PreToolUse"`, `"SessionStart"`}
	for i := 1; i < len(order); i++ {
		if strings.Index(got, order[i-1]) > strings.Index(got, order[i]) {
			t.Errorf("%s moved after %s:\n%s", order[i-1], order[i], got)
		}
	}
	// A second run finds the hook in the rewritten file and changes nothing.
	if _, code := setupRun(t, home, "--dir", dir, "--target", "claude"); code != 0 {
		t.Fatal("second run failed")
	}
	if b2, _ := os.ReadFile(settings); string(b2) != got {
		t.Errorf("second run rewrote settings:\n%s", b2)
	}
}

// Defends EST-8259 item 3: an edited SKILL.md is replaced without a prompt.
// That is the design (the file is setup's own), so the help says so.
func TestSetupHelpSaysSkillIsOverwritten(t *testing.T) {
	_, out, _ := runCLI(t, "setup", "--help")
	if !strings.Contains(out, "hand edit") || !strings.Contains(out, "overwritten") {
		t.Errorf("setup --help does not warn that SKILL.md edits are overwritten:\n%s", out)
	}
}

// hitCounter is a page server that counts its requests.
func hitCounter(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		_, _ = io.WriteString(w, `<html><body><a href="/a">a</a></body></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

// Defends EST-8259 item 4: a bad number was silently replaced -- --max-bytes 0
// became 16 MiB, --timeout -1 became 30 s, --retries -3 became 0, and
// extract-relevant --timeout 0 failed after the run started. Each is now a
// usage error, exit 2, before any request.
func TestBadNumbersAreRefused(t *testing.T) {
	page, hits := hitCounter(t)
	calls, _ := countingLLM(t, "x")
	src := filepath.Join(t.TempDir(), "a.md")
	writeFiles(t, filepath.Dir(src), map[string]string{"a.md": "text"})

	for _, args := range [][]string{
		{"fetch", page, "--max-bytes", "0"},
		{"fetch", page, "--max-bytes", "-5"},
		{"fetch", page, "--timeout", "0"},
		{"fetch", page, "--timeout", "-1"},
		{"fetch", page, "--retries", "-3"},
		{"extract-links", "--url", page, "--timeout", "0"},
		{"extract-relevant", "--path", src, "--context", "x", "--timeout", "0"},
		{"extract-relevant", "--path", src, "--context", "x", "--timeout", "-5"},
		{"extract-relevant", "--path", src, "--context", "x", "--concurrency", "0"},
	} {
		code, out, _ := runCLI(t, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2:\n%s", args, code, out)
		}
		if !strings.Contains(out, args[len(args)-2]) {
			t.Errorf("%v: error does not name the flag:\n%s", args, out)
		}
	}
	if hits.Load() != 0 || calls.Load() != 0 {
		t.Errorf("%d page requests and %d model calls made for refused numbers", hits.Load(), calls.Load())
	}
	// --retries 0 is a real choice, not a bad number.
	if code, out, _ := runCLI(t, "fetch", page, "--retries", "0"); code != 0 {
		t.Errorf("--retries 0: exit %d:\n%s", code, out)
	}
}

// Defends EST-8259 item 5: -o was checked only after the model calls, so a
// bad path cost every call and then exited 1. It is checked first: exit 2,
// no call.
func TestExtractRelevantChecksOutputBeforeCalling(t *testing.T) {
	calls, _ := countingLLM(t, "x")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.md": "text"})
	for _, bad := range []string{filepath.Join(dir, "no", "such", "out.md"), dir} {
		code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "a.md"), "--context", "x", "-o", bad)
		if code != 2 {
			t.Errorf("-o %s: exit %d, want 2:\n%s", bad, code, out)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("%d model calls before a bad -o was refused", n)
	}
	// A good -o still works, and the check leaves nothing behind in the
	// directory beside the output.
	good := filepath.Join(dir, "out.md")
	if code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "a.md"), "--context", "x", "-o", good); code != 0 {
		t.Fatalf("good -o: exit %d:\n%s", code, out)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("directory holds %v, want a.md and out.md", entries)
	}
}

// Defends EST-8259 item 6: bytes_sent and model were counted before the
// send, so a run whose model was unreachable reported a model that never ran.
func TestModelUseNotReportedWhenNoCallSucceeded(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	pointExtractRelevantAt(t, srv)
	srv.Close()
	noRetryDelay(t)
	src := filepath.Join(t.TempDir(), "a.md")
	writeFiles(t, filepath.Dir(src), map[string]string{"a.md": "text"})

	code, out, _ := runCLI(t, "extract-relevant", "--path", src, "--context", "x", "--json")
	if code == 0 {
		t.Fatalf("unreachable model exited 0:\n%s", out)
	}
	if strings.Contains(out, "bytes_sent") || strings.Contains(out, "fake-model") {
		t.Errorf("a run that reached no model reports one:\n%s", out)
	}
}

// Defends EST-8259 item 7: --head was silently ignored with --output (the
// file always gets the whole body). It is a usage error now, before the
// request.
func TestFetchHeadWithOutputIsRefused(t *testing.T) {
	page, hits := hitCounter(t)
	dst := filepath.Join(t.TempDir(), "page.html")
	code, out, _ := runCLI(t, "fetch", page, "--output", dst, "--head", "10")
	if code != 2 || !strings.Contains(out, "--head") {
		t.Errorf("exit %d, want 2 naming --head:\n%s", code, out)
	}
	if hits.Load() != 0 {
		t.Error("request made")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("output file written")
	}
}

// Defends EST-8259 item 7: the line `fetch --output --plain` prints was
// documented nowhere; the docs said --plain prints body bytes.
func TestFetchSavedLineIsDocumented(t *testing.T) {
	_, help, _ := runCLI(t, "fetch", "--help")
	docs := map[string]string{"fetch --help": help, "SKILL.md": setupSkill}
	for _, f := range []string{"README.md", "docs/output.md"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", f))
		if err != nil {
			t.Fatal(err)
		}
		docs[f] = string(b)
	}
	for name, text := range docs {
		if !strings.Contains(text, "Saved <n> bytes") {
			t.Errorf("%s does not document the --output --plain line", name)
		}
	}
}

// Defends EST-8259 item 8: `setup extra` was reported as an unknown command
// with commands: null. An extra argument is the wrong number of arguments,
// exit 2, with the command's flags -- on every command that takes none.
func TestExtraArgumentIsWrongArgCount(t *testing.T) {
	for _, args := range [][]string{
		{"setup", "extra"},
		{"extract-links", "--url", "https://a.invalid", "extra"},
		{"extract-relevant", "--path", "x", "--context", "x", "extra"},
	} {
		code, doc, out := runDoc(t, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2:\n%s", args, code, out)
		}
		msg, _ := doc["message"].(string)
		if !strings.Contains(msg, "accepts 0 arg(s)") {
			t.Errorf("%v: message %q", args, msg)
		}
		if _, ok := doc["commands"]; ok {
			t.Errorf("%v: lists commands:\n%s", args, out)
		}
		if flags, _ := doc["flags"].([]any); len(flags) == 0 {
			t.Errorf("%v: no flags listed:\n%s", args, out)
		}
	}
}

// Defends EST-8259 item 9: setup printed plain lines whatever the format,
// with no help[], and had no --json. Its result is a TOON document by
// default, JSON under --json, and the old lines under --plain.
func TestSetupResultIsStructured(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	stubHookBinary(t, true, "")

	code, doc, out := runDoc(t, "setup", "--dir", dir, "--target", "codex", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	hooks := filepath.Join(dir, ".codex", "hooks.json")
	if w, _ := doc["written"].([]any); len(w) != 1 || w[0] != hooks {
		t.Errorf("written = %v:\n%s", doc["written"], out)
	}
	if doc["dry_run"] != true {
		t.Errorf("dry_run = %v", doc["dry_run"])
	}
	if h, _ := doc["help"].([]any); len(h) == 0 || !strings.Contains(h[0].(string), "without --dry-run") {
		t.Errorf("help = %v", doc["help"])
	}

	code, doc, out = runDoc(t, "setup", "--dir", dir, "--target", "codex")
	if code != 0 || doc["dry_run"] != false {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	code, doc, out = runDoc(t, "setup", "--dir", dir, "--target", "codex")
	if u, _ := doc["unchanged"].([]any); code != 0 || len(u) != 1 || u[0] != hooks {
		t.Errorf("rerun: exit %d, unchanged = %v:\n%s", code, doc["unchanged"], out)
	}
	if w, ok := doc["written"].([]any); !ok || len(w) != 0 {
		t.Errorf("rerun: written = %v, want an empty list:\n%s", doc["written"], out)
	}

	code, jsonOut, _ := runCLI(t, "setup", "--dir", dir, "--target", "codex", "--json")
	var got struct {
		Written   []string `json:"written"`
		Unchanged []string `json:"unchanged"`
		DryRun    *bool    `json:"dry_run"`
		Help      []string `json:"help"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &got); err != nil || code != 0 || got.DryRun == nil || len(got.Unchanged) != 1 || got.Help != nil {
		t.Errorf("--json: exit %d, %v:\n%s", code, err, jsonOut)
	}
}

// Defends EST-8259 item 10: a page over the size cap got the hint "Check the
// URL is reachable, or raise --timeout", which cannot fix a size cap.
func TestExtractLinksOverCapHintNamesTheCap(t *testing.T) {
	prev := maxRemoteBodyBytes
	maxRemoteBodyBytes = 16
	t.Cleanup(func() { maxRemoteBodyBytes = prev })
	page, _ := hitCounter(t)

	code, doc, out := runDoc(t, "extract-links", "--url", page)
	if code == 0 {
		t.Fatalf("over-cap page exited 0:\n%s", out)
	}
	h, _ := doc["help"].([]any)
	if len(h) == 0 || !strings.Contains(h[0].(string), "FETCH_MAX_BYTES") || strings.Contains(h[0].(string), "--timeout") {
		t.Errorf("help = %v", h)
	}
}
