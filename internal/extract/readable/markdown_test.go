package readable

import (
	"bytes"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-readable-golden", false, "update readable Markdown golden files")

// Defends T2's golden-file contract: every fixture with main content has a
// reviewed Markdown golden, while the no-main-content fixtures remain errors
// in TestExtractFixtures instead of producing empty Markdown successes.
func TestArticleMarkdownFixtures(t *testing.T) {
	base, _ := url.Parse("https://example.test/section/page.html")
	for _, f := range loadFixtures(t) {
		if f.NoMainContent {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", f.Path))
			if err != nil {
				t.Fatal(err)
			}
			article, err := Extract(bytes.NewReader(raw), base)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			got, err := article.Markdown()
			if err != nil {
				t.Fatalf("Markdown: %v", err)
			}

			goldenPath := filepath.Join("testdata", "golden", strings.TrimSuffix(f.Path, ".html")+".md")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (generate with -update-readable-golden, then review the diff): %v", err)
			}
			if got != string(want) {
				t.Errorf("Markdown differs from %s\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, want)
			}
		})
	}
}

// Defends each HTML construct T2 promises to preserve as Markdown. Golden
// files alone could otherwise lose one construct in every fixture unnoticed.
func TestArticleMarkdownStructures(t *testing.T) {
	article := Article{HTML: `<h1>Heading</h1><h2>Subheading</h2>
<ul><li>Unordered item</li></ul><ol><li>Ordered item</li></ol>
<p><a href="https://example.test/target">Absolute link</a></p>
<pre><code class="language-go">fmt.Println("hello")</code></pre>
<table><thead><tr><th>Header</th><th>Meaning</th></tr></thead>
<tbody><tr><td>Cell</td><td>Value</td></tr></tbody></table>`}
	got, err := article.Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	for _, want := range []string{
		"# Heading",
		"## Subheading",
		"- Unordered item",
		"1. Ordered item",
		"[Absolute link](https://example.test/target)",
		"```go\nfmt.Println(\"hello\")\n```",
		"| Header",
		"| Cell",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Markdown is missing %q:\n%s", want, got)
		}
	}
}

// Defends every URL form named by T2 against the final response URL. In
// particular, fragments must not leak through as relative links, a base
// element must take effect, and unsafe targets must not survive in Markdown.
func TestArticleMarkdownLinkForms(t *testing.T) {
	tests := []struct {
		name       string
		head       string
		href       string
		wantTarget string
	}{
		{name: "parent relative", href: "../x", wantTarget: "https://example.test/docs/x"},
		{name: "root relative", href: "/x", wantTarget: "https://example.test/x"},
		{name: "protocol relative", href: "//other.test/x", wantTarget: "https://other.test/x"},
		{name: "fragment", href: "#frag", wantTarget: "https://example.test/docs/guides/page.html#frag"},
		{name: "base href", head: `<base href="../assets/">`, href: "x", wantTarget: "https://example.test/docs/assets/x"},
		{name: "mailto", href: "mailto:author@example.test", wantTarget: "mailto:author@example.test"},
		{name: "javascript", href: " JavaScript:alert(1)"},
		{name: "data", href: "data:text/plain,hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page := fmt.Sprintf(`<!doctype html><html><head><title>Links</title>%s</head><body><main><article>
<h1>Link example</h1><p>%s</p><p>%s</p><p><a href="%s">link text</a></p>
</article></main></body></html>`, tt.head,
				strings.Repeat("This article has enough substantive prose for main-content extraction. ", 8),
				strings.Repeat("A second paragraph keeps the link inside the selected article. ", 8), tt.href)
			base, _ := url.Parse("https://example.test/docs/guides/page.html")
			article, err := Extract(strings.NewReader(page), base)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			got, err := article.Markdown()
			if err != nil {
				t.Fatalf("Markdown: %v", err)
			}
			if !strings.Contains(got, "link text") {
				t.Fatalf("link text was dropped:\n%s", got)
			}
			if tt.wantTarget == "" {
				if strings.Contains(got, "[link text](") || strings.Contains(strings.ToLower(got), "javascript:") || strings.Contains(got, "data:") {
					t.Fatalf("unsafe target survived instead of text alone:\n%s", got)
				}
				return
			}
			want := "[link text](" + tt.wantTarget + ")"
			if !strings.Contains(got, want) {
				t.Fatalf("missing %q:\n%s", want, got)
			}
		})
	}
}
