package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
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

// TestDocsMatchTheFetchEnvelope defends the epic 10 fetch truth: a successful
// fetch is a TOON envelope with the whole-body size, a truncated flag and
// help[] naming --full, and --full returns the whole body. No doc may still
// describe a successful fetch as a raw-byte pipe or say no command has
// --full. The probes run first, so a doc fails only while the code agrees
// with the contract the docs must state.
func TestDocsMatchTheFetchEnvelope(t *testing.T) {
	body := strings.Repeat("0123456789abcdef\n", 512) // 8704 bytes, over the 4096 default
	srv := pageServer(t, http.StatusOK, body)

	code, doc, out := runDoc(t, "fetch", srv.URL)
	if code != 0 || doc["truncated"] != true || !strings.Contains(out, fmt.Sprintf("bytes: %d", len(body))) {
		t.Fatalf("default fetch is not a cut envelope with the whole-body size; the probe, not the docs, is wrong (exit %d):\n%s", code, out)
	}
	if b, _ := doc["body"].(string); len(b) >= len(body) {
		t.Fatalf("default fetch did not cut the body; the probe, not the docs, is wrong:\n%s", out)
	}
	if !strings.Contains(out, "--full") {
		t.Fatalf("a cut envelope's help[] does not name --full; the probe, not the docs, is wrong:\n%s", out)
	}
	code, doc, out = runDoc(t, "fetch", srv.URL, "--full")
	if code != 0 || doc["truncated"] != false || doc["body"] != body {
		t.Fatalf("--full did not return the whole body; the probe, not the docs, is wrong (exit %d):\n%s", code, out)
	}

	for _, path := range []string{"README.md", "docs/output.md", "docs/axi-audit.md", "docs/axi-catalog-entry.yaml"} {
		s := readRepoFile(t, path)
		for _, stale := range []string{
			"fetch is not TOON", "`fetch` is not TOON",
			"no command has --full", "no command has `--full`",
			"stays the raw body",
			"prints the raw body",
			"fetch prints the whole body",
			"byte for byte in every encoding mode",
		} {
			if strings.Contains(s, stale) {
				t.Errorf("%s claims %q, but fetch prints a TOON envelope with --head and --full", path, stale)
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

// TestSkillDescribesTheFetchEnvelope defends the epic 10 skill text: fetch
// now prints a capped TOON envelope, so an Agent Skill that still describes
// fetch as "fetches a URL" would leave an agent with a 4096-byte cut and no
// idea how to get the rest. The probe first proves the live behaviour (a page
// over 4096 bytes comes back truncated with help[] naming --full), then fails
// on a skill that does not name the envelope and the flags that control it.
func TestSkillDescribesTheFetchEnvelope(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 5000)))
	}))
	t.Cleanup(srv.Close)
	var out, errb bytes.Buffer
	if code := run([]string{"fetch", srv.URL}, &out, &errb); code != 0 {
		t.Fatalf("fetch exited %d: %s%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "truncated: true") || !strings.Contains(out.String(), "--full") {
		t.Fatalf("a 5000-byte page is not a truncated envelope with help[] naming --full; the probe, not the skill, is wrong:\n%s", out.String())
	}
	s := readRepoFile(t, "integrations/claude-code/skill/SKILL.md")
	for _, want := range []string{"truncated", "4096", "--head", "--full", "--fields", "--output"} {
		if !strings.Contains(s, want) {
			t.Errorf("the Agent Skill does not name %q, so an agent cannot learn the fetch envelope from it", want)
		}
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

// TestDocsDescribeChunkedModelInput defends epic 12's docs truth (T4):
// extract-relevant reads each input in chunks that each fit one prompt,
// --max-chunks bounds the chunks sent (one model call per chunk, counted in
// model_calls), and no doc may still claim the model reads only the first
// 30,000 bytes of an input. The probe first proves the live behaviour — a
// file over the bound reports unread chunks naming --max-chunks, and
// model_calls equals the requests that actually arrived — then fails on a
// doc that states the old cut or does not name the bound and its cost.
func TestDocsDescribeChunkedModelInput(t *testing.T) {
	var calls atomic.Int32
	srv := fakeLLM(t, func(string) string { calls.Add(1); return "kept" })
	pointExtractRelevantAt(t, srv)
	big := filepath.Join(t.TempDir(), "big.md")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 40000)), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI(t, "extract-relevant", "--path", big, "--context", "c", "--max-chunks", "1", "--json")
	var r ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(r.Truncated, "--max-chunks") || r.ModelCalls == nil || *r.ModelCalls != int64(calls.Load()) {
		t.Fatalf("a file over --max-chunks 1 is not a truncated result whose model_calls counts the real calls; the probe, not the docs, is wrong (exit %d):\n%s", code, out)
	}

	for _, path := range []string{"README.md", "docs/output.md", "docs/axi-audit.md", "docs/axi-catalog-entry.yaml", "integrations/claude-code/skill/SKILL.md"} {
		s := readRepoFile(t, path)
		for _, stale := range []string{
			"cuts each prompt",
			"only the first 30,000 bytes",
			"each prompt cut at 30,000 bytes",
			"cut their model input at a fixed limit",
			"30,000-byte input",
		} {
			if strings.Contains(s, stale) {
				t.Errorf("%s claims %q, but extract-relevant reads in chunks bounded by --max-chunks, not a 30,000-byte cut", path, stale)
			}
		}
		for _, want := range []string{"--max-chunks", "model_calls"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s does not name %q, so a reader cannot learn the chunk bound and its cost from it", path, want)
			}
		}
	}

	var help, helpErr bytes.Buffer
	if code := run([]string{"extract-relevant", "--help"}, &help, &helpErr); code != 0 {
		t.Fatalf("extract-relevant --help exited %d: %s", code, helpErr.String())
	}
	if !strings.Contains(help.String(), "chunk") {
		t.Error("extract-relevant --help does not say the input is read in chunks")
	}
}

// TestDocsDescribeFetchMarkdown defends epic 11's fetch --format markdown:
// the probe first proves the live behaviour (the body is Markdown, and the
// envelope carries source_bytes, title and byline), then fails on a doc that
// does not name the format or still says raw is the only one. A page that is
// not HTML is probed too, so the docs' exit-1 claim is checked against the
// code rather than restated.
func TestDocsDescribeFetchMarkdown(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	p := serveMarkdownPage(t, "text/html", readFixture(t, mdFixture))
	code, doc, out := runDoc(t, "fetch", p.url, "--format", "markdown")
	if code != 0 || doc["format"] != "markdown" {
		t.Fatalf("--format markdown is not the live format; the probe, not the docs, is wrong (exit %d):\n%s", code, out)
	}
	for _, field := range []string{"source_bytes", "title", "byline"} {
		if _, ok := doc[field]; !ok {
			t.Fatalf("markdown envelope has no %q; the probe, not the docs, is wrong:\n%s", field, out)
		}
	}
	plain := serveMarkdownPage(t, "text/plain", []byte("not html"))
	if code, out, _ := runCLI(t, "fetch", plain.url, "--format", "markdown"); code != 1 || !strings.Contains(out, "--format markdown") {
		t.Fatalf("a non-HTML page is not an exit-1 error naming --format markdown; the probe, not the docs, is wrong (exit %d):\n%s", code, out)
	}

	for _, path := range []string{"README.md", "docs/output.md", "integrations/claude-code/skill/SKILL.md"} {
		s := readRepoFile(t, path)
		if !strings.Contains(s, "--format markdown") {
			t.Errorf("%s does not name --format markdown, so a reader cannot learn the format from it", path)
		}
		for _, stale := range []string{"`raw` only", "accepts only `raw`", "rejected until ported"} {
			if strings.Contains(s, stale) {
				t.Errorf("%s claims %q, but --format takes raw or markdown", path, stale)
			}
		}
	}
}

// TestDocsMatchMinimalErrorKeys defends epic 14's minimal error shape: under
// --json --min an error is one line with the abbreviated keys err and msg and
// no n, while TOON keeps error and message. The probes run first, so docs
// fails only while the code agrees with the contract it must state.
func TestDocsMatchMinimalErrorKeys(t *testing.T) {
	code, out, errb := runCLI(t, "extract-links", "--json", "--min", "--bogus")
	if code != 2 || errb != "" {
		t.Fatalf("extract-links --json --min --bogus exited %d (stderr %q), want 2 with empty stderr", code, errb)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("minimal JSON error is not one JSON document: %v\n%s", err, out)
	}
	if doc["err"] != true || doc["msg"] == "" {
		t.Fatalf("minimal JSON error has no err/msg; the probe, not the docs, is wrong:\n%s", out)
	}
	for _, k := range []string{"error", "message", "n"} {
		if _, ok := doc[k]; ok {
			t.Fatalf("minimal JSON error carries the key %q; the probe, not the docs, is wrong:\n%s", k, out)
		}
	}
	_, toon, _ := runCLI(t, "extract-links", "--bogus")
	if !strings.Contains(toon, "error: true") || !strings.Contains(toon, "message:") {
		t.Fatalf("TOON error lost its full keys; the probe, not the docs, is wrong:\n%s", toon)
	}

	docs := readRepoFile(t, "docs/output.md")
	for _, want := range []string{"`err`", "`msg`"} {
		if !strings.Contains(docs, want) {
			t.Errorf("docs/output.md never names %s, so a --min JSON caller cannot learn the error keys from it", want)
		}
	}
	if strings.Contains(docs, "`--json`: one `{\"error\": true, \"message\": ...}` document.") &&
		!strings.Contains(docs, "With `--min`") {
		t.Error("docs/output.md still gives {error, message} as the only --json error shape")
	}
}

// TestChangelogRecordsEveryEpicFix defends epic 14 T6: each of T1 to T4 has a
// Fixed entry in Unreleased, and the file names no issue id or epic number,
// so a reader gets the change in user terms and no internal reference.
func TestChangelogRecordsEveryEpicFix(t *testing.T) {
	log := readRepoFile(t, "CHANGELOG.md")
	for _, bad := range regexp.MustCompile(`\bEST-\d+\b|\bepic [0-9]+\b`).FindAllString(log, -1) {
		t.Errorf("CHANGELOG.md names %q; a changelog entry describes the change to a user, not the issue or epic behind it", bad)
	}

	start := strings.Index(log, "## [Unreleased]")
	if start < 0 {
		t.Fatal("CHANGELOG.md has no Unreleased section")
	}
	unreleased := log[start:]
	if end := strings.Index(unreleased[len("## [Unreleased]"):], "\n## ["); end >= 0 {
		unreleased = unreleased[:len("## [Unreleased]")+end]
	}
	for _, want := range []struct{ change, probe string }{
		{"T1", "converts to no Markdown text"},
		{"T2", "--full` with `--output"},
		{"T3", "`err`"},
		{"T4", "mailto"},
	} {
		if !strings.Contains(unreleased, want.probe) {
			t.Errorf("Unreleased does not record %s (no %q), so a reader cannot learn that change from the changelog", want.change, want.probe)
		}
	}
}
