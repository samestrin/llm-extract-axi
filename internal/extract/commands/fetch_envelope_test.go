package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	goaxi "github.com/samestrin/go-axi"
)

// fetchPage is one page the envelope table serves.
type fetchPage struct {
	name, contentType, body string
	// headCut and defaultCut are the body bytes a cut at 10 and at 4096
	// keeps: the cut backs off to a rune boundary, and a binary body
	// carries no text at all.
	headCut, defaultCut string
	binary              bool
}

// envelopePages are the four kinds of page the table crosses with every mode.
func envelopePages() []fetchPage {
	small := "<p>small page é</p>"
	big := strings.Repeat("a", 5000)
	// A three-byte rune straddles both cuts: bytes 9-11 and 4094-4096.
	multi := strings.Repeat("a", 9) + "€" + strings.Repeat("a", 4094-12) + "€" + "tail"
	bin := "\x89PNG\r\n\x1a\n\x00\xff\xfe" + strings.Repeat("\xff\x00", 2600)
	return []fetchPage{
		{name: "small", contentType: "text/html; charset=utf-8", body: small, headCut: small[:10], defaultCut: small},
		{name: "over 4096", contentType: "text/html", body: big, headCut: big[:10], defaultCut: big[:4096]},
		{name: "rune at cut", contentType: "text/plain", body: multi, headCut: multi[:9], defaultCut: multi[:4094]},
		{name: "binary", contentType: "image/png", body: bin, headCut: bin[:10], defaultCut: bin[:4096], binary: true},
	}
}

// servePage serves p with its content type.
func servePage(t *testing.T, p fetchPage) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", p.contentType)
		_, _ = w.Write([]byte(p.body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// decodeFetchDoc reads a fetch envelope from TOON or JSON stdout.
func decodeFetchDoc(t *testing.T, stdout string, asJSON bool) map[string]any {
	t.Helper()
	if asJSON {
		var doc map[string]any
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%q", err, stdout)
		}
		return doc
	}
	if json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is JSON, want TOON:\n%s", stdout)
	}
	v, err := goaxi.Decode(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("stdout is not TOON: %v\n%q", err, stdout)
	}
	doc, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("TOON is not a document: %T", v)
	}
	return doc
}

// docNumber reads a number from a decoded document, TOON or JSON.
func docNumber(doc map[string]any, key string) string {
	return fmt.Sprint(doc[key])
}

// Defends epic 10 T1: fetch prints an envelope, not the raw page, with the
// size, truncated and a help[] naming --full and --output when it cut; --head
// and --full set the cut; --plain prints body bytes only with the size hint on
// stderr; --json is the same capped document; --output saves the whole body
// and drops body from the envelope. A binary body never lands in a text field.
// Printing the raw page again, cutting mid-rune, or claiming a cut body is
// whole turns a cell of this red.
func TestFetchEnvelopeTable(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	modes := []string{"default", "--head 10", "--full", "--plain", "--json", "--output"}
	for _, p := range envelopePages() {
		url := servePage(t, p)
		for _, mode := range modes {
			t.Run(p.name+"/"+mode, func(t *testing.T) {
				args := []string{"fetch", url}
				var outFile string
				switch mode {
				case "--head 10":
					args = append(args, "--head", "10")
				case "--full", "--plain", "--json":
					args = append(args, mode)
				case "--output":
					outFile = filepath.Join(t.TempDir(), "page.bin")
					args = append(args, "--output", outFile)
				}
				code, stdout, stderr := runCLI(t, args...)
				if code != 0 {
					t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
				}

				if mode == "--plain" {
					// Body bytes and nothing else on stdout; the hint goes to
					// stderr, and only when the body was cut.
					if stdout != p.defaultCut {
						t.Errorf("--plain stdout is not the body cut at 4096: got %d bytes, want %d", len(stdout), len(p.defaultCut))
					}
					cut := len(p.defaultCut) < len(p.body)
					if cut != (stderr != "") {
						t.Errorf("stderr = %q; cut=%v", stderr, cut)
					}
					if cut && (!strings.Contains(stderr, "--full") || !strings.Contains(stderr, fmt.Sprint(len(p.body)))) {
						t.Errorf("size hint does not name --full and the size: %q", stderr)
					}
					return
				}

				if stderr != "" {
					t.Errorf("stderr = %q, want empty", stderr)
				}
				doc := decodeFetchDoc(t, stdout, mode == "--json")
				for _, k := range []string{"url", "status", "tier", "format", "content_type", "bytes", "truncated"} {
					if _, ok := doc[k]; !ok {
						t.Errorf("envelope lacks %q: %v", k, doc)
					}
				}
				if docNumber(doc, "bytes") != fmt.Sprint(len(p.body)) {
					t.Errorf("bytes = %v, want %d", doc["bytes"], len(p.body))
				}
				if doc["status"] == nil || docNumber(doc, "status") != "200" || doc["tier"] != "direct" || doc["format"] != "raw" {
					t.Errorf("status/tier/format wrong: %v", doc)
				}
				if doc["content_type"] != p.contentType {
					t.Errorf("content_type = %v, want %q", doc["content_type"], p.contentType)
				}

				if mode == "--output" {
					got, err := os.ReadFile(outFile)
					if err != nil || string(got) != p.body {
						t.Errorf("file holds %d bytes, want the %d served (err %v)", len(got), len(p.body), err)
					}
					if _, ok := doc["body"]; ok {
						t.Errorf("--output envelope carries body: %v", doc["body"])
					}
					if doc["output"] != outFile {
						t.Errorf("output = %v, want %q", doc["output"], outFile)
					}
					if doc["truncated"] != false {
						t.Errorf("truncated = %v, want false: the file is whole", doc["truncated"])
					}
					return
				}
				if _, ok := doc["output"]; ok {
					t.Errorf("output set without --output: %v", doc["output"])
				}

				body, ok := doc["body"].(string)
				if !ok {
					t.Fatalf("body missing or not a string: %v", doc["body"])
				}
				if !utf8.ValidString(body) {
					t.Errorf("body is not valid UTF-8: %q", body)
				}
				want := p.defaultCut
				switch mode {
				case "--head 10":
					want = p.headCut
				case "--full":
					want = p.body
				}
				if p.binary {
					want = ""
				}
				if body != want {
					t.Errorf("body = %d bytes %.40q, want %d bytes %.40q", len(body), body, len(want), want)
				}
				wantTrunc := p.binary || len(want) < len(p.body)
				if doc["truncated"] != wantTrunc {
					t.Errorf("truncated = %v, want %v", doc["truncated"], wantTrunc)
				}

				help, hasHelp := doc["help"]
				if mode == "--json" {
					if hasHelp {
						t.Errorf("--json carries help[], as no other command's --json does: %v", help)
					}
					return
				}
				helpText := fmt.Sprint(help)
				switch {
				case p.binary:
					if !strings.Contains(helpText, "--output") {
						t.Errorf("binary help[] does not point to --output: %v", help)
					}
				case wantTrunc:
					if !strings.Contains(helpText, "--full") || !strings.Contains(helpText, "--output <file>") {
						t.Errorf("help[] does not name --full and --output <file>: %v", help)
					}
				}
				// #55: help[] names flags only, never page-controlled text.
				if strings.Contains(helpText, url) || strings.Contains(helpText, "127.0.0.1") {
					t.Errorf("help[] carries the URL: %v", help)
				}
			})
		}
	}
}

// Defends the --head rules: a cut below one byte, or a cut together with
// --full, is a malformed invocation (exit 2) before any request is sent, in
// the wording ExitCodeFor reads as a usage error.
func TestFetchHeadUsageErrors(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("x"))
	}))
	t.Cleanup(srv.Close)
	for _, args := range [][]string{
		{"--head", "0"},
		{"--head", "-3"},
		{"--head", "5", "--full"},
		{"--full", "--head", "5", "--json"},
	} {
		code, stdout, stderr := runCLI(t, append([]string{"fetch", srv.URL}, args...)...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2; stdout=%q stderr=%q", args, code, stdout, stderr)
		}
		if !strings.Contains(stdout, "invalid argument") || !strings.Contains(stdout, "--head") {
			t.Errorf("%v: error does not name --head: %q", args, stdout)
		}
	}
	if hits != 0 {
		t.Errorf("a usage error sent %d requests", hits)
	}
}

// Defends the text checks: a non-text content type, or a text type whose body
// is not UTF-8, gets an empty body and truncated, never mojibake in a string.
// A text type whose body is valid UTF-8 is printed.
func TestFetchEnvelopeNonText(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	cases := []struct {
		ct, body string
		text     bool
	}{
		{"application/octet-stream", "plain ascii", false},
		{"application/pdf", "%PDF-1.7", false},
		{"text/html", "caf\xe9", false},
		{"application/json", `{"a":1}`, true},
		{"application/xhtml+xml", "<x/>", true},
		{"image/svg+xml", "<svg/>", true},
		// A content type that does not parse says nothing about the bytes,
		// so the body is withheld rather than trusted.
		{`text/html; charset="unterminated`, "<p>x</p>", false},
		// Every application type the switch accepts as text, so a typo in
		// one sends nothing down the binary path unnoticed.
		{"application/xml", "<a/>", true},
		{"application/javascript", "var a;", true},
		{"application/ecmascript", "var b;", true},
		{"application/x-javascript", "var c;", true},
		{"application/x-ndjson", `{"a":1}`, true},
		{"application/yaml", "a: 1", true},
		{"application/x-yaml", "b: 2", true},
		{"application/toml", "a=1", true},
		{"application/x-www-form-urlencoded", "a=1&b=2", true},
		{"", "no type", true},
	}
	for _, c := range cases {
		t.Run(c.ct, func(t *testing.T) {
			url := servePage(t, fetchPage{contentType: c.ct, body: c.body})
			code, stdout, stderr := runCLI(t, "fetch", url, "--json")
			if code != 0 {
				t.Fatalf("exit %d; %q %q", code, stdout, stderr)
			}
			doc := decodeFetchDoc(t, stdout, true)
			if c.text && (doc["body"] != c.body || doc["truncated"] != false) {
				t.Errorf("text body dropped: %v", doc)
			}
			if !c.text && (doc["body"] != "" || doc["truncated"] != true) {
				t.Errorf("non-text body printed: %v", doc)
			}
		})
	}
}

// Defends --head with --plain: the cut follows --head, and --plain --full
// prints the whole body with no hint, the migration path for the old output.
func TestFetchPlainHeadAndFull(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	page := strings.Repeat("b", 5000)
	url := servePage(t, fetchPage{contentType: "text/plain", body: page})
	code, stdout, stderr := runCLI(t, "fetch", url, "--plain", "--head", "7")
	if code != 0 || stdout != page[:7] || !strings.Contains(stderr, "5000") {
		t.Errorf("--plain --head 7: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI(t, "fetch", url, "--plain", "--full")
	if code != 0 || stdout != page || stderr != "" {
		t.Errorf("--plain --full: exit %d, %d bytes, stderr %q", code, len(stdout), stderr)
	}
}
