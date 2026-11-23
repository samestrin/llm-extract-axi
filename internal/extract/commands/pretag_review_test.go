package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runCLIWithin is runCLI that fails the test instead of hanging when the run
// does not finish: a directory run that opens /dev/zero or a FIFO never does.
func runCLIWithin(t *testing.T, d time.Duration, args ...string) (int, string) {
	t.Helper()
	type res struct {
		code int
		out  string
	}
	done := make(chan res, 1)
	go func() {
		code, out, _ := runCLI(t, args...)
		done <- res{code, out}
	}()
	select {
	case r := <-done:
		return r.code, r.out
	case <-time.After(d):
		t.Fatalf("%v did not finish in %s", args, d)
		return 0, ""
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// Defends EST-8274 item 1: a directory run followed a file symlink out of
// --path. Walk sized the link itself, which passed the 100 KB cap, and
// os.ReadFile then read the target, so t2/link.txt -> ../outside/target.txt
// sent the outside file to the model; a link to /dev/zero hung the run. Only
// regular files whose real path is under --path are read. A link inside the
// tree still works, and each skip is counted.
func TestExtractRelevantDirectoryStaysInsidePath(t *testing.T) {
	calls, prompts := countingLLM(t, "NONE")
	base := t.TempDir()
	writeFiles(t, base, map[string]string{
		"outside/target.txt": "OUTSIDE_SECRET",
		"t2/in.txt":          "INSIDE_TEXT",
	})
	dir := filepath.Join(base, "t2")
	symlink(t, "../outside/target.txt", filepath.Join(dir, "link.txt"))
	symlink(t, filepath.Join(base, "outside", "target.txt"), filepath.Join(dir, "abs.txt"))
	symlink(t, "/dev/zero", filepath.Join(dir, "zero.txt"))
	symlink(t, "in.txt", filepath.Join(dir, "alias.txt"))
	symlink(t, "missing.txt", filepath.Join(dir, "dangling.txt"))
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, "sub", filepath.Join(dir, "dirlink.txt"))

	code, out := runCLIWithin(t, 20*time.Second, "extract-relevant", "--path", dir, "--context", "x", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if p := prompts(); strings.Contains(p, "OUTSIDE_SECRET") || !strings.Contains(p, "INSIDE_TEXT") {
		t.Errorf("prompts sent:\n%s", p)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("%d model calls, want 2 (in.txt and the in-tree alias)", n)
	}
	var got ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"outside_path": 3, "not_regular": 1, "unreadable": 1}
	for reason, n := range want {
		if got.Skipped[reason] != n {
			t.Errorf("skipped[%s] = %d, want %d: %v", reason, got.Skipped[reason], n, got.Skipped)
		}
	}
}

// Defends EST-8274 item 1: a --path that is itself a symlink to a directory
// is the directory the caller named; its files are read, not skipped as
// outside it.
func TestExtractRelevantDirectoryPathMayBeASymlink(t *testing.T) {
	_, prompts := countingLLM(t, "NONE")
	base := t.TempDir()
	writeFiles(t, base, map[string]string{"real/a.txt": "REAL_TEXT"})
	link := filepath.Join(base, "link")
	symlink(t, filepath.Join(base, "real"), link)

	if code, out := runCLIWithin(t, 20*time.Second, "extract-relevant", "--path", link, "--context", "x", "--json"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(prompts(), "REAL_TEXT") {
		t.Errorf("file under a symlinked --path not read:\n%s", prompts())
	}
}

// Defends EST-8274 item 3: the skip rule looked only at dotfiles and
// extensions, so credentials.json, secrets.yaml and secrets.toml went to the
// model. Credential-like names are skipped by default and counted; a file
// named outright is still read.
func TestExtractRelevantDirectorySkipsCredentialNames(t *testing.T) {
	calls, prompts := countingLLM(t, "NONE")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"credentials.json":            "SECRET_CREDS",
		"Credentials.yml":             "SECRET_CREDS_UPPER",
		"secrets.yaml":                "SECRET_YAML",
		"secrets.toml":                "SECRET_TOML",
		"secret.txt":                  "SECRET_TXT",
		"api_token.txt":               "SECRET_TOKEN",
		"server.key":                  "SECRET_KEY",
		"my-service-account.json":     "SECRET_SA",
		"gcp/service_account_ci.json": "SECRET_SA2",
		"notes.txt":                   "plain notes",
	})

	code, out := runCLIWithin(t, 20*time.Second, "extract-relevant", "--path", dir, "--context", "x", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if p := prompts(); strings.Contains(p, "SECRET") || !strings.Contains(p, "plain notes") {
		t.Errorf("prompts sent:\n%s", p)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d model calls, want 1", n)
	}
	var got ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	// server.key has no text extension, but the credential rule names it first.
	if got.Skipped["credential_name"] != 9 {
		t.Errorf("skipped = %v, want credential_name: 9", got.Skipped)
	}

	if code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "secrets.yaml"), "--context", "x"); code != 0 {
		t.Fatalf("named file: exit %d:\n%s", code, out)
	}
	if !strings.Contains(prompts(), "SECRET_YAML") {
		t.Error("a credential file named as --path was not read")
	}
}

// Defends EST-8274 item 6: a file over 100 KB and a binary file were skipped
// with nothing said, so a caller could not tell a file was never read. The
// result counts each skip by reason, under --json, --min and --plain.
func TestExtractRelevantReportsSkippedCounts(t *testing.T) {
	countingLLM(t, "NONE")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"big.txt":   strings.Repeat("x", 100*1024+1),
		"image.png": "PNG",
		".env":      "SECRET",
		"a.txt":     "text",
	})

	code, out := runCLIWithin(t, 20*time.Second, "extract-relevant", "--path", dir, "--context", "x", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	var got ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	for reason, n := range map[string]int{"too_large": 1, "not_text": 1, "dotfile": 1} {
		if got.Skipped[reason] != n {
			t.Errorf("skipped[%s] = %d, want %d: %v", reason, got.Skipped[reason], n, got.Skipped)
		}
	}

	_, out = runCLIWithin(t, 20*time.Second, "extract-relevant", "--path", dir, "--context", "x", "--min")
	if !strings.Contains(out, "sk:") || !strings.Contains(out, "too_large: 1") {
		t.Errorf("--min does not carry the skipped counts:\n%s", out)
	}
	_, out = runCLIWithin(t, 20*time.Second, "extract-relevant", "--path", dir, "--context", "x", "--plain")
	if !strings.Contains(out, "SKIPPED: ") || !strings.Contains(out, "1 too_large") {
		t.Errorf("--plain does not say what was skipped:\n%s", out)
	}
}

// Defends EST-8274 item 2: setup wrote through symlinked agent directories.
// With .claude, .codex and .opencode linked to ../ext/*, every file landed in
// ext/; .claude -> ~/.claude would install a user-wide hook. A target that
// resolves outside the resolved --dir is exit 2, and nothing is written,
// under --dry-run too.
func TestSetupRefusesTargetsOutsideDir(t *testing.T) {
	cases := map[string]func(t *testing.T, base, dir string){
		"agent dirs linked out": func(t *testing.T, base, dir string) {
			for _, d := range []string{".claude", ".codex", ".opencode"} {
				if err := os.MkdirAll(filepath.Join(base, "ext", d), 0o755); err != nil {
					t.Fatal(err)
				}
				symlink(t, filepath.Join("..", "ext", d), filepath.Join(dir, d))
			}
		},
		"settings file linked out": func(t *testing.T, base, dir string) {
			writeFiles(t, base, map[string]string{"ext/settings.json": "{}\n"})
			if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			symlink(t, filepath.Join(base, "ext", "settings.json"), filepath.Join(dir, ".claude", "settings.json"))
		},
		"dangling link out": func(t *testing.T, base, dir string) {
			if err := os.MkdirAll(filepath.Join(base, "ext"), 0o755); err != nil {
				t.Fatal(err)
			}
			symlink(t, filepath.Join(base, "ext", "new-codex"), filepath.Join(dir, ".codex"))
		},
	}
	for name, arrange := range cases {
		for _, dry := range []bool{false, true} {
			label := name
			if dry {
				label += " dry-run"
			}
			t.Run(label, func(t *testing.T) {
				base := t.TempDir()
				dir := filepath.Join(base, "proj")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				arrange(t, base, dir)
				before := snapshot(t, filepath.Join(base, "ext"))
				args := []string{"--dir", dir}
				if dry {
					args = append(args, "--dry-run")
				}
				out, code := setupRun(t, t.TempDir(), args...)
				if code != 2 {
					t.Errorf("exit %d, want 2:\n%s", code, out)
				}
				if !strings.Contains(out, "outside") {
					t.Errorf("error does not say why:\n%s", out)
				}
				if after := snapshot(t, filepath.Join(base, "ext")); after != before {
					t.Errorf("ext/ changed:\nbefore %s\nafter  %s", before, after)
				}
			})
		}
	}
}

// Defends the other half of item 2: links that stay inside the project, and a
// --dir that is itself a symlink, are not "outside".
func TestSetupAllowsLinksInsideDir(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(real, "agent", "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join("agent", "claude"), filepath.Join(real, ".claude"))
	link := filepath.Join(base, "proj-link")
	symlink(t, real, link)

	if out, code := setupRun(t, t.TempDir(), "--dir", link, "--target", "claude"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(real, "agent", "claude", "settings.json")); err != nil {
		t.Errorf("settings not written through the in-project link: %v", err)
	}
}

// snapshot lists every path under root with its content, so a test can tell
// that nothing was written there.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		b.WriteString(p)
		if info.Mode().IsRegular() {
			data, _ := os.ReadFile(p)
			b.WriteString("=" + string(data))
		}
		b.WriteString(";")
		return nil
	})
	return b.String()
}

// Defends EST-8274 item 4: a bad fetch --output (a directory, newdir/,
// nope/x.txt) was refused only after the network fetch, with exit 1. It is
// refused first: exit 2, no request.
func TestFetchChecksOutputBeforeRequest(t *testing.T) {
	page, hits := hitCounter(t)
	dir := t.TempDir()
	for _, bad := range []string{dir, filepath.Join(dir, "newdir") + "/", filepath.Join(dir, "nope", "x.txt")} {
		code, out, _ := runCLI(t, "fetch", page, "--output", bad)
		if code != 2 {
			t.Errorf("--output %s: exit %d, want 2:\n%s", bad, code, out)
		}
		if !strings.Contains(out, "--output") {
			t.Errorf("--output %s: error does not name the flag:\n%s", bad, out)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests made before a bad --output was refused", n)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the check left %v behind", entries)
	}
	good := filepath.Join(dir, "page.html")
	if code, out, _ := runCLI(t, "fetch", page, "--output", good); code != 0 {
		t.Fatalf("good --output: exit %d:\n%s", code, out)
	}
	if b, err := os.ReadFile(good); err != nil || !strings.Contains(string(b), "<a href") {
		t.Errorf("good --output not written: %v %q", err, b)
	}
}

// Defends EST-8274 item 5: a model-call error printed the base URL whole, so
// a key in its query or a password in an unparsable URL reached the output.
func TestModelErrorDoesNotPrintBaseURLSecrets(t *testing.T) {
	noRetryDelay(t)
	src := filepath.Join(t.TempDir(), "a.md")
	writeFiles(t, filepath.Dir(src), map[string]string{"a.md": "text"})
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, base := range []string{
		"http://127.0.0.1:1/v1?api-key=QTOKEN",
		"http://user:BASEPASS@127.0.0.1:1/v1",
		"ht!tp://user:BASEPASS@x",
	} {
		t.Setenv("OPENAI_BASE_URL", base)
		code, out, stderr := runCLI(t, "extract-relevant", "--path", src, "--context", "x", "--json")
		if code == 0 {
			t.Errorf("%s: exit 0:\n%s", base, out)
		}
		if strings.Contains(out+stderr, "QTOKEN") || strings.Contains(out+stderr, "BASEPASS") {
			t.Errorf("%s: secret printed:\n%s%s", base, out, stderr)
		}
	}
}

// Defends EST-8274 items 1 and 3 together: the name rules looked at a link's
// own name, so notes.txt -> .env or a.md -> credentials.json inside --path
// sent the secret under an innocent name. A link is judged by the file it
// leads to as well.
func TestExtractRelevantLinkJudgedByTarget(t *testing.T) {
	calls, prompts := countingLLM(t, "NONE")
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".env":             "SECRET_DOTENV",
		".aws/config.txt":  "SECRET_DOTDIR",
		"credentials.json": "SECRET_CREDS",
		"plain.txt":        "plain notes",
	})
	symlink(t, ".env", filepath.Join(dir, "notes.txt"))
	symlink(t, filepath.Join(".aws", "config.txt"), filepath.Join(dir, "aws.txt"))
	symlink(t, "credentials.json", filepath.Join(dir, "a.md"))

	code, out := runCLIWithin(t, 20*time.Second, "extract-relevant", "--path", dir, "--context", "x", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if p := prompts(); strings.Contains(p, "SECRET") {
		t.Errorf("a secret went out under a link's name:\n%s", p)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d model calls, want 1 (plain.txt)", n)
	}
}
