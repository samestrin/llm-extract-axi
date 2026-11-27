package readable

import (
	"net/url"
	"strings"
	"testing"

	"github.com/go-shiori/dom"
	"golang.org/x/net/html"
)

// Defends the scheme allow-list: only http, https, mailto and tel targets
// (and scheme-less relative/fragment targets) may keep an href. A hostile
// scheme such as vbscript: or file: must lose its href while the anchor's
// visible text is kept, never silently rendered as a usable link.
func TestNormalizeLinksSchemeAllowList(t *testing.T) {
	tests := []struct {
		name string
		href string
		want string // resolved href; "" means the href must be removed
	}{
		{name: "http kept", href: "http://example.test/x", want: "http://example.test/x"},
		{name: "https kept", href: "https://example.test/x", want: "https://example.test/x"},
		{name: "mailto kept", href: "mailto:author@example.test", want: "mailto:author@example.test"},
		{name: "tel kept", href: "tel:+15551234567", want: "tel:+15551234567"},
		{name: "javascript dropped", href: "javascript:alert(1)"},
		{name: "mixed-case javascript dropped", href: " JaVaScRiPt:alert(1)"},
		{name: "data dropped", href: "data:text/plain,hello"},
		{name: "vbscript dropped", href: "vbscript:msgbox(1)"},
		{name: "file dropped", href: "file:///etc/passwd"},
		{name: "ftp dropped", href: "ftp://example.test/pub/x"},
		{name: "relative kept and resolved", href: "../x", want: "https://example.test/x"},
		{name: "fragment kept and resolved", href: "#frag", want: "https://example.test/docs/page.html#frag"},
		{name: "malformed dropped", href: "http://[::1"},
	}
	base, err := url.Parse("https://example.test/docs/page.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(`<div><a href="` + tt.href + `">link text</a></div>`))
			if err != nil {
				t.Fatal(err)
			}
			normalizeLinks(doc, base)
			links := dom.GetElementsByTagName(doc, "a")
			if len(links) != 1 {
				t.Fatalf("got %d anchors, want 1", len(links))
			}
			link := links[0]
			if got := dom.TextContent(link); got != "link text" {
				t.Fatalf("anchor text = %q, want %q", got, "link text")
			}
			if tt.want == "" {
				if dom.HasAttribute(link, "href") {
					t.Fatalf("href survived: %q", dom.GetAttribute(link, "href"))
				}
				return
			}
			if got := dom.GetAttribute(link, "href"); got != tt.want {
				t.Fatalf("href = %q, want %q", got, tt.want)
			}
		})
	}
}

// Defends scheme-less targets when there is no base URL: a relative link
// must keep its href unchanged, not be treated as an unknown scheme and
// dropped.
func TestNormalizeLinksRelativeWithoutBase(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<div><a href="../x">link text</a></div>`))
	if err != nil {
		t.Fatal(err)
	}
	normalizeLinks(doc, nil)
	link := dom.GetElementsByTagName(doc, "a")[0]
	if got := dom.GetAttribute(link, "href"); got != "../x" {
		t.Fatalf("href = %q, want %q", got, "../x")
	}
}

// Defends base discovery for callers that have no final URL, against a
// malformed base replacing a valid response URL with an unusable target, and
// against an empty base preventing discovery of a later usable base.
func TestEffectiveBaseURLEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		bases []string
		final string
		want  string
	}{
		{name: "absolute base without final URL", bases: []string{"https://static.example.test/manual/"}, want: "https://static.example.test/manual/"},
		{name: "malformed base ignored", bases: []string{"%"}, final: "https://example.test/final/page", want: "https://example.test/final/page"},
		{name: "empty base ignored", bases: []string{""}, final: "https://example.test/final/page", want: "https://example.test/final/page"},
		{name: "empty base skipped for next base", bases: []string{"", "https://static.example.test/manual/"}, final: "https://example.test/final/page", want: "https://static.example.test/manual/"},
		{name: "relative base without final URL", bases: []string{"manual/"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var head strings.Builder
			for _, baseRef := range tt.bases {
				head.WriteString(`<base href="` + baseRef + `">`)
			}
			doc, err := html.Parse(strings.NewReader(`<html><head>` + head.String() + `</head><body></body></html>`))
			if err != nil {
				t.Fatal(err)
			}
			var final *url.URL
			if tt.final != "" {
				final, err = url.Parse(tt.final)
				if err != nil {
					t.Fatal(err)
				}
			}
			got := effectiveBaseURL(doc, final)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("got %q, want nil", got)
				}
				return
			}
			if got == nil || got.String() != tt.want {
				t.Fatalf("got %v, want %q", got, tt.want)
			}
		})
	}
}
