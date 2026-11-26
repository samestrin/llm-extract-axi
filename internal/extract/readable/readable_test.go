package readable

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// fixture is one entry of testdata/fixtures.json: a page written for this
// repository, the sentences its main content must keep, and the boilerplate
// lines it must drop.
type fixture struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	Path          string   `json:"path"`
	Title         string   `json:"title"`
	Byline        string   `json:"byline"`
	Expect        []string `json:"expect"`
	Boilerplate   []string `json:"boilerplate"`
	NoMainContent bool     `json:"no_main_content"`
}

func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var fs []fixture
	if err := json.Unmarshal(raw, &fs); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return fs
}

// visibleText is the text a reader would see in an HTML fragment or page:
// script and style dropped, whitespace collapsed to single spaces.
func visibleText(t *testing.T, s string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "head") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(b.String()), " ")
}

// Defends the manifest itself: the success criterion names ten pages and six
// kinds, and a fixture whose expected sentence or boilerplate line is not on
// the page would make the extraction test below pass without proving anything.
func TestFixtureManifestIsHonest(t *testing.T) {
	fs := loadFixtures(t)
	if len(fs) != 10 {
		t.Fatalf("want 10 fixtures, got %d", len(fs))
	}
	kinds := map[string]bool{}
	for _, f := range fs {
		kinds[f.Kind] = true
		raw, err := os.ReadFile(filepath.Join("testdata", f.Path))
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		page := visibleText(t, string(raw))
		if !f.NoMainContent && len(f.Expect) == 0 {
			t.Errorf("%s: no expected sentence", f.Name)
		}
		if !f.NoMainContent && len(f.Boilerplate) < 3 {
			t.Errorf("%s: want at least 3 boilerplate lines (nav, footer, cookie banner), got %d", f.Name, len(f.Boilerplate))
		}
		for _, s := range append(append([]string{}, f.Expect...), f.Boilerplate...) {
			if !strings.Contains(page, s) {
				t.Errorf("%s: %q is not on the page, so the check is vacuous", f.Name, s)
			}
		}
	}
	for _, k := range []string{"article", "docs", "blog", "forum", "product", "no-article"} {
		if !kinds[k] {
			t.Errorf("no fixture of kind %q", k)
		}
	}
}

// Defends the task's success criterion: on every fixture the main content
// keeps the committed sentences and drops nav, footer and cookie banner, and
// a page with no main content is ErrNoMainContent rather than an empty success.
func TestExtractFixtures(t *testing.T) {
	base, _ := url.Parse("https://example.test/section/page.html")
	for _, f := range loadFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			file, err := os.Open(filepath.Join("testdata", f.Path))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()

			a, err := Extract(file, base)
			if f.NoMainContent {
				if !errors.Is(err, ErrNoMainContent) {
					t.Fatalf("want ErrNoMainContent, got err=%v article=%+v", err, a)
				}
				if a != (Article{}) {
					t.Fatalf("an error must come with an empty article, got %+v", a)
				}
				return
			}
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if a.Title != f.Title {
				t.Errorf("title: got %q, want %q", a.Title, f.Title)
			}
			if a.Byline != f.Byline {
				t.Errorf("byline: got %q, want %q", a.Byline, f.Byline)
			}
			text := visibleText(t, a.HTML)
			for _, s := range f.Expect {
				if !strings.Contains(text, s) {
					t.Errorf("main text is missing expected sentence %q", s)
				}
			}
			for _, s := range f.Boilerplate {
				if strings.Contains(text, s) {
					t.Errorf("main text keeps boilerplate %q", s)
				}
			}
		})
	}
}

// Defends the "no empty success" rule for the degenerate inputs a fetch can
// hand over: an empty body and a page whose body holds nothing but chrome.
func TestExtractEmptyInputIsNoMainContent(t *testing.T) {
	for name, in := range map[string]string{
		"empty":       "",
		"blank body":  "<html><head><title>Blank</title></head><body>   </body></html>",
		"only a list": "<html><body><ul><li><a href=/a>A</a></li><li><a href=/b>B</a></li></ul></body></html>",
	} {
		t.Run(name, func(t *testing.T) {
			a, err := Extract(strings.NewReader(in), nil)
			if !errors.Is(err, ErrNoMainContent) {
				t.Fatalf("want ErrNoMainContent, got err=%v article=%+v", err, a)
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// Defends against a read failure being reported as "no main content" or as
// an empty article: the caller must see the I/O error itself.
func TestExtractReadErrorIsReturned(t *testing.T) {
	a, err := Extract(io.MultiReader(strings.NewReader("<html><body><p>"), failingReader{}), nil)
	if err == nil {
		t.Fatalf("want an error, got article %+v", a)
	}
	if errors.Is(err, ErrNoMainContent) {
		t.Fatalf("a read failure is not ErrNoMainContent: %v", err)
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("error should carry the cause, got %v", err)
	}
}

// Defends link resolution against the final URL, which T2's Markdown relies on:
// a relative href in the main content comes back absolute.
func TestExtractResolvesRelativeLinks(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "article-news.html"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	base, _ := url.Parse("https://news.example.test/2026/11/harbour/")
	a, err := Extract(f, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.HTML, `href="https://news.example.test/reports/harbour-survey-2026.pdf"`) {
		t.Fatalf("relative link not made absolute:\n%s", a.HTML)
	}
}
