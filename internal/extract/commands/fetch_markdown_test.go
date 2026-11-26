package commands

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/samestrin/llm-extract-axi/v2/internal/extract/readable"
)

// The article fixture T1 committed, with its title, byline, a sentence of
// main text and a line of boilerplate that must not survive.
const (
	mdFixture     = "../readable/testdata/article-news.html"
	mdTitle       = "Harbour survey finds seagrass recovering faster than expected"
	mdByline      = "Mara Okonkwo"
	mdSentence    = "Seagrass meadows in the inner harbour have spread across almost twice the area recorded five years ago"
	mdBoilerplate = "Subscribe for $1 a week"
)

// mdPage is a page served for the markdown tests, counting its requests.
type mdPage struct {
	url  string
	html []byte
	hits *atomic.Int32
}

// serveMarkdownPage serves body under contentType. A nil contentType header
// value sends no Content-Type at all (Go would otherwise sniff one).
func serveMarkdownPage(t *testing.T, contentType string, body []byte) mdPage {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if contentType == "" {
			w.Header()["Content-Type"] = nil
		} else {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return mdPage{url: srv.URL + "/news/seagrass.html", html: body, hits: &hits}
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wantMarkdown is the Markdown T2's converter makes of p, against the URL the
// page was served from: the oracle the CLI's body must match byte for byte.
func wantMarkdown(t *testing.T, p mdPage) string {
	t.Helper()
	u, _ := url.Parse(p.url)
	art, err := readable.Extract(bytes.NewReader(p.html), u)
	if err != nil {
		t.Fatal(err)
	}
	md, err := art.Markdown()
	if err != nil {
		t.Fatal(err)
	}
	return md
}

// bigArticle is an article whose Markdown runs well past the 4096-byte
// default cut, with a multi-byte rune in every paragraph so a cut can land
// inside one.
func bigArticle() []byte {
	var b strings.Builder
	b.WriteString("<html><head><title>Long read</title></head><body><nav>Home | About</nav><article><h1>Long read</h1>")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "<p>Paragraph %d of the long read, café prose that goes on for a while so the main content is plainly an article and not a page of links.</p>", i)
	}
	b.WriteString("</article><footer>Footer text</footer></body></html>")
	return []byte(b.String())
}

// Defends epic 11 T3's envelope: under --format markdown, body is the
// Markdown of the main content, format says markdown, bytes is the size of
// the whole Markdown, source_bytes the size of the fetched HTML, and title
// and byline come from the page; in raw mode none of the new fields appear.
// Returning the HTML, sizing bytes against the HTML, or leaking the new
// fields into raw results turns a cell red.
func TestFetchMarkdownEnvelopeFields(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	p := serveMarkdownPage(t, "text/html; charset=utf-8", readFixture(t, mdFixture))
	md := wantMarkdown(t, p)
	for _, asJSON := range []bool{false, true} {
		for _, format := range []string{"markdown", "raw"} {
			t.Run(fmt.Sprintf("%s/json=%v", format, asJSON), func(t *testing.T) {
				args := []string{"fetch", p.url, "--format", format}
				if asJSON {
					args = append(args, "--json")
				}
				code, stdout, stderr := runCLI(t, args...)
				if code != 0 {
					t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
				}
				doc := decodeFetchDoc(t, stdout, asJSON)
				if doc["format"] != format {
					t.Errorf("format = %v, want %s", doc["format"], format)
				}
				if format == "raw" {
					for _, k := range []string{"source_bytes", "title", "byline"} {
						if _, ok := doc[k]; ok {
							t.Errorf("raw result carries %s: %v", k, doc[k])
						}
					}
					if docNumber(doc, "bytes") != fmt.Sprint(len(p.html)) {
						t.Errorf("raw bytes = %v, want %d", doc["bytes"], len(p.html))
					}
					return
				}
				if doc["body"] != md {
					t.Errorf("body is not the Markdown:\n%v", doc["body"])
				}
				body := fmt.Sprint(doc["body"])
				if !strings.Contains(body, mdSentence) || strings.Contains(body, mdBoilerplate) {
					t.Errorf("body misses the main text or keeps boilerplate:\n%s", body)
				}
				if docNumber(doc, "bytes") != fmt.Sprint(len(md)) {
					t.Errorf("bytes = %v, want the Markdown size %d", doc["bytes"], len(md))
				}
				if docNumber(doc, "source_bytes") != fmt.Sprint(len(p.html)) {
					t.Errorf("source_bytes = %v, want the HTML size %d", doc["source_bytes"], len(p.html))
				}
				if doc["title"] != mdTitle || doc["byline"] != mdByline {
					t.Errorf("title=%v byline=%v", doc["title"], doc["byline"])
				}
				if doc["truncated"] != false {
					t.Errorf("truncated = %v for a body under the cut", doc["truncated"])
				}
				if doc["content_type"] != "text/html; charset=utf-8" {
					t.Errorf("content_type = %v", doc["content_type"])
				}
			})
		}
	}
}

// Defends the cut: --head, the default 4096 and --full cut the Markdown, not
// the HTML, and truncated and help[] are relative to the whole Markdown.
// Cutting the HTML, or measuring truncated against source_bytes, turns this
// red.
func TestFetchMarkdownCutAppliesToMarkdown(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	p := serveMarkdownPage(t, "text/html", bigArticle())
	md := wantMarkdown(t, p)
	if len(md) <= defaultHead {
		t.Fatalf("fixture Markdown is %d bytes; it must exceed the default cut", len(md))
	}
	for _, tc := range []struct {
		name  string
		args  []string
		limit int
	}{
		{"default", nil, defaultHead},
		{"--head 100", []string{"--head", "100"}, 100},
		{"--full", []string{"--full"}, len(md)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"fetch", p.url, "--format", "markdown", "--json"}, tc.args...)
			code, stdout, stderr := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
			}
			doc := decodeFetchDoc(t, stdout, true)
			want := string(headCut([]byte(md), tc.limit))
			if doc["body"] != want {
				t.Errorf("body is %d bytes, want the Markdown cut at %d (%d bytes)", len(fmt.Sprint(doc["body"])), tc.limit, len(want))
			}
			if docNumber(doc, "bytes") != fmt.Sprint(len(md)) {
				t.Errorf("bytes = %v, want %d", doc["bytes"], len(md))
			}
			if doc["truncated"] != (len(want) < len(md)) {
				t.Errorf("truncated = %v, body %d of %d", doc["truncated"], len(want), len(md))
			}
		})
	}
	// TOON carries help[] naming --full when the Markdown was cut.
	code, stdout, _ := runCLI(t, "fetch", p.url, "--format", "markdown", "--head", "100")
	doc := decodeFetchDoc(t, stdout, false)
	if code != 0 || !strings.Contains(fmt.Sprint(doc["help"]), "--full") {
		t.Errorf("cut Markdown has no help[] naming --full: exit %d\n%s", code, stdout)
	}
}

// Defends decision Q3: a page that is not HTML is a run error (exit 1) that
// names the content type, with help[] pointing at plain fetch, and the page
// is requested once, with no pre-flight request. An empty success, a usage
// exit or a silently raw body turns this red.
func TestFetchMarkdownNonHTMLIsError(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	for _, ct := range []string{"application/json", "image/png", "text/plain; charset=utf-8"} {
		t.Run(ct, func(t *testing.T) {
			p := serveMarkdownPage(t, ct, []byte(`{"a":1}`))
			code, doc, out := runDoc(t, "fetch", p.url, "--format", "markdown")
			if code != 1 {
				t.Errorf("exit %d, want 1:\n%s", code, out)
			}
			mt := strings.SplitN(ct, ";", 2)[0]
			if doc["error"] != true || !strings.Contains(fmt.Sprint(doc["message"]), mt) {
				t.Errorf("error does not name %s:\n%s", mt, out)
			}
			help := fmt.Sprint(doc["help"])
			if !strings.Contains(help, "without --format markdown") {
				t.Errorf("help[] does not point at plain fetch: %s", help)
			}
			if strings.Contains(out, `"a":1`) || strings.Contains(out, "body") {
				t.Errorf("the page reached stdout:\n%s", out)
			}
			if n := p.hits.Load(); n != 1 {
				t.Errorf("%d requests, want exactly 1", n)
			}
		})
	}
}

// Defends a missing Content-Type: the bytes decide, so an HTML page served
// with no type still converts, and a JSON one is still refused.
func TestFetchMarkdownNoContentTypeSniffs(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	p := serveMarkdownPage(t, "", readFixture(t, mdFixture))
	code, stdout, stderr := runCLI(t, "fetch", p.url, "--format", "markdown", "--json")
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if doc := decodeFetchDoc(t, stdout, true); doc["body"] != wantMarkdown(t, p) {
		t.Errorf("untyped HTML did not convert:\n%s", stdout)
	}
	j := serveMarkdownPage(t, "", []byte(`{"a":1}`))
	if code, _, out := runDoc(t, "fetch", j.url, "--format", "markdown"); code != 1 {
		t.Errorf("untyped JSON exited %d, want 1:\n%s", code, out)
	}
}

// Defends "never an empty body": a page with no main content is an error
// with help[] (exit 1), in every output mode, and no --output file is left.
func TestFetchMarkdownNoMainContentIsError(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	for _, fixture := range []string{"no-article-login.html", "no-article-landing.html"} {
		p := serveMarkdownPage(t, "text/html", readFixture(t, "../readable/testdata/"+fixture))
		t.Run(fixture, func(t *testing.T) {
			code, doc, out := runDoc(t, "fetch", p.url, "--format", "markdown")
			if code != 1 || doc["error"] != true {
				t.Fatalf("exit %d, want an exit-1 error:\n%s", code, out)
			}
			if !strings.Contains(fmt.Sprint(doc["message"]), "no main content") {
				t.Errorf("message does not say no main content:\n%s", out)
			}
			if !strings.Contains(fmt.Sprint(doc["help"]), "without --format markdown") {
				t.Errorf("help[] has no next step:\n%s", out)
			}
			file := filepath.Join(t.TempDir(), "page.md")
			code, stdout, errOut := runFetchCmd(t, p.url, "--format", "markdown", "--output", file)
			if code != 1 || !fetchError(stdout, errOut) {
				t.Errorf("--plain --output: exit %d stdout=%q stderr=%q", code, stdout, errOut)
			}
			if _, err := os.Stat(file); err == nil {
				t.Errorf("an --output file was written for a page with no main content")
			}
		})
	}
}

// Defends decision Q4: --output saves the whole Markdown and reports its size
// in bytes, under the envelope and under --plain's Saved line; --plain alone
// prints the Markdown bytes under the same cut as the envelope.
func TestFetchMarkdownOutputAndPlain(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	p := serveMarkdownPage(t, "text/html", bigArticle())
	md := wantMarkdown(t, p)

	file := filepath.Join(t.TempDir(), "page.md")
	code, stdout, stderr := runCLI(t, "fetch", p.url, "--format", "markdown", "--output", file, "--json")
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := string(readFixture(t, file)); got != md {
		t.Errorf("--output file is not the whole Markdown: %d bytes, want %d", len(got), len(md))
	}
	doc := decodeFetchDoc(t, stdout, true)
	if docNumber(doc, "bytes") != fmt.Sprint(len(md)) || doc["output"] != file {
		t.Errorf("envelope: %s", stdout)
	}
	if _, ok := doc["body"]; ok {
		t.Errorf("--output envelope carries body")
	}

	plainFile := filepath.Join(t.TempDir(), "plain.md")
	code, stdout, _ = runFetchCmd(t, p.url, "--format", "markdown", "--output", plainFile)
	want := fmt.Sprintf("Saved %d bytes [HTTP 200 via direct, markdown] -> %s\n", len(md), plainFile)
	if code != 0 || stdout != want {
		t.Errorf("--plain --output: exit %d\n got %q\nwant %q", code, stdout, want)
	}
	if got := string(readFixture(t, plainFile)); got != md {
		t.Errorf("--plain --output file is not the whole Markdown")
	}

	for _, tc := range []struct {
		args  []string
		limit int
	}{{nil, defaultHead}, {[]string{"--head", "50"}, 50}, {[]string{"--full"}, len(md)}} {
		code, stdout, stderr := runFetchCmd(t, append([]string{p.url, "--format", "markdown"}, tc.args...)...)
		if want := string(headCut([]byte(md), tc.limit)); code != 0 || stdout != want {
			t.Errorf("--plain %v: exit %d, %d bytes, want the Markdown cut at %d (%d bytes)", tc.args, code, len(stdout), tc.limit, len(want))
		}
		cut := tc.limit < len(md)
		if cut != strings.Contains(stderr, fmt.Sprintf("of %d bytes", len(md))) {
			t.Errorf("--plain %v: stderr hint %q does not match a cut of the Markdown", tc.args, stderr)
		}
	}
}

// Defends --fields: source_bytes, title and byline are selectable, and
// selecting only them in raw mode is an error that says why, not an empty
// document.
func TestFetchMarkdownFieldsSelectNewFields(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	p := serveMarkdownPage(t, "text/html", readFixture(t, mdFixture))
	code, stdout, stderr := runCLI(t, "fetch", p.url, "--format", "markdown", "--fields", "source_bytes,title,byline", "--json")
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	doc := decodeFetchDoc(t, stdout, true)
	if len(doc) != 3 || docNumber(doc, "source_bytes") != fmt.Sprint(len(p.html)) || doc["title"] != mdTitle || doc["byline"] != mdByline {
		t.Errorf("--fields selected %v", doc)
	}
	code, out, _ := runCLI(t, "fetch", p.url, "--fields", "title,byline", "--json")
	if code != 1 || !strings.Contains(out, "--format markdown") {
		t.Errorf("raw --fields title,byline: exit %d, want 1 naming --format markdown:\n%s", code, out)
	}
}

// Defends the flag checks: a bad combination under --format markdown is
// still a usage error (exit 2) refused before any request, and --format text
// stays rejected.
func TestFetchMarkdownBadFlagsSendNoRequest(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	p := serveMarkdownPage(t, "text/html", readFixture(t, mdFixture))
	dir := t.TempDir()
	for _, args := range [][]string{
		{"--format", "markdown", "--head", "0"},
		{"--format", "markdown", "--head", "5", "--full"},
		{"--format", "markdown", "--head", "5", "--output", filepath.Join(dir, "x.md")},
		{"--format", "markdown", "--fields", "nope"},
		{"--format", "markdown", "--output", dir},
		{"--format", "text"},
		{"--format", "Markdown"},
	} {
		code, _, out := runDoc(t, append([]string{"fetch", p.url}, args...)...)
		if code != 2 {
			t.Errorf("%v exited %d, want 2:\n%s", args, code, out)
		}
	}
	if n := p.hits.Load(); n != 0 {
		t.Errorf("%d request(s) sent for invocations that were refused", n)
	}
}
