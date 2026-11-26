package readable

import (
	"net/url"
	"strings"

	"github.com/go-shiori/dom"
	"golang.org/x/net/html"
)

// effectiveBaseURL applies the document's first usable base href to the final
// response URL. Readability resolves ordinary relative links, but does not
// discover base elements itself.
func effectiveBaseURL(doc *html.Node, pageURL *url.URL) *url.URL {
	for _, el := range dom.GetElementsByTagName(doc, "base") {
		href := strings.TrimSpace(dom.GetAttribute(el, "href"))
		if href == "" {
			continue
		}
		ref, err := url.Parse(href)
		if err != nil {
			continue
		}
		if pageURL != nil {
			return pageURL.ResolveReference(ref)
		}
		if ref.IsAbs() {
			return ref
		}
	}
	return pageURL
}

// normalizeLinks finishes Readability's URL handling: fragments are resolved
// against the effective base too, and unsafe or malformed targets lose their
// href without losing the anchor's visible contents.
func normalizeLinks(root *html.Node, baseURL *url.URL) {
	for _, link := range dom.GetElementsByTagName(root, "a") {
		href := strings.TrimSpace(dom.GetAttribute(link, "href"))
		if href == "" {
			dom.RemoveAttribute(link, "href")
			continue
		}
		target, err := url.Parse(href)
		if err != nil || strings.EqualFold(target.Scheme, "javascript") || strings.EqualFold(target.Scheme, "data") {
			dom.RemoveAttribute(link, "href")
			continue
		}
		if baseURL != nil {
			target = baseURL.ResolveReference(target)
		}
		dom.SetAttribute(link, "href", target.String())
	}
}
