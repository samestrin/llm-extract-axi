// Package readable finds the main content of an HTML page: the article,
// post or documentation body, without navigation, footers or cookie banners.
//
// It wraps codeberg.org/readeck/go-readability, a pure-Go port of Mozilla's
// Readability.js (MIT, no CGO). A page with nothing that reads as main
// content is ErrNoMainContent, never an empty Article.
package readable

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	readability "codeberg.org/readeck/go-readability/v2"
	"github.com/go-shiori/dom"
	"golang.org/x/net/html"
)

// ErrNoMainContent means the page parsed but holds nothing that reads as
// main content: a landing page, a login form, an empty body.
var ErrNoMainContent = errors.New("no main content found on the page")

// Article is the main content of a page.
type Article struct {
	Title  string
	Byline string
	// HTML is the cleaned main content as an HTML fragment. Relative links
	// are resolved against the page URL when one is given.
	HTML string
}

// Extract reads an HTML page and returns its main content. pageURL is the
// page's final URL, used to make relative links absolute; it may be nil.
func Extract(r io.Reader, pageURL *url.URL) (Article, error) {
	doc, err := dom.Parse(r)
	if err != nil {
		return Article{}, fmt.Errorf("parse html: %w", err)
	}
	// Readability always returns something, falling back to the whole body,
	// so its own readerable check is what tells a page with an article from
	// a page of chrome.
	parser := readability.NewParser()
	if !parser.CheckDocument(doc) {
		return Article{}, ErrNoMainContent
	}
	a, err := parser.ParseAndMutate(doc, pageURL)
	if err != nil {
		return Article{}, fmt.Errorf("extract main content: %w", err)
	}
	if a.Node == nil || strings.TrimSpace(dom.TextContent(a.Node)) == "" {
		return Article{}, ErrNoMainContent
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, a.Node); err != nil {
		return Article{}, fmt.Errorf("render main content: %w", err)
	}
	return Article{Title: a.Title(), Byline: a.Byline(), HTML: buf.String()}, nil
}
