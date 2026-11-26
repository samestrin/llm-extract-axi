package readable

import (
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Defends base discovery for callers that have no final URL, and against a
// malformed base replacing a valid response URL with an unusable target.
func TestEffectiveBaseURLEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		baseRef string
		final   string
		want    string
	}{
		{name: "absolute base without final URL", baseRef: "https://static.example.test/manual/", want: "https://static.example.test/manual/"},
		{name: "malformed base ignored", baseRef: "%", final: "https://example.test/final/page", want: "https://example.test/final/page"},
		{name: "empty base ignored", baseRef: "", final: "https://example.test/final/page", want: "https://example.test/final/page"},
		{name: "relative base without final URL", baseRef: "manual/", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(`<html><head><base href="` + tt.baseRef + `"></head><body></body></html>`))
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
