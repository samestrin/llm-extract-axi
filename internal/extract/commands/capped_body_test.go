package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// testBodyCap is the cap the tests below lower maxRemoteBodyBytes to, so no
// test allocates the 16 MiB default.
const testBodyCap = 256

// lowerBodyCap sets the remote body cap to testBodyCap for one test.
func lowerBodyCap(t *testing.T) {
	t.Helper()
	prev := maxRemoteBodyBytes
	maxRemoteBodyBytes = testBodyCap
	t.Cleanup(func() { maxRemoteBodyBytes = prev })
}

// sizedHTML returns an HTML page of exactly n bytes holding one link and one
// paragraph, padded inside a comment so the padding never reaches the text.
func sizedHTML(t *testing.T, n int) string {
	t.Helper()
	head := `<html><body><p>install with brew</p><p><a href="/docs">Docs</a></p><!--`
	tail := `--></body></html>`
	pad := n - len(head) - len(tail)
	if pad < 0 {
		t.Fatalf("page of %d bytes is too small for the fixture", n)
	}
	return head + strings.Repeat("x", pad) + tail
}

// sizedServer serves body with the given Content-Type.
func sizedServer(t *testing.T, contentType, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// assertNamesCap fails unless msg names the size cap and its value.
func assertNamesCap(t *testing.T, msg string) {
	t.Helper()
	if !strings.Contains(msg, "cap") || !strings.Contains(msg, strconv.Itoa(testBodyCap)) {
		t.Errorf("error does not name the %d-byte cap: %q", testBodyCap, msg)
	}
}

// Defends extract-relevant against reading a remote body of any size: a body
// one byte over the cap, HTML or not, is an error naming the cap, and nothing
// is sent to the model, so no extraction from a truncated page is reported.
func TestExtractRelevantRefusesABodyOverTheCap(t *testing.T) {
	for _, tc := range []struct{ name, contentType, body string }{
		{"html", "text/html; charset=utf-8", sizedHTML(t, testBodyCap+1)},
		{"plain", "text/plain", strings.Repeat("y", testBodyCap+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lowerBodyCap(t)
			page := sizedServer(t, tc.contentType, tc.body)
			var calls atomic.Int32
			srv := fakeLLM(t, func(string) string { calls.Add(1); return "brew install" })
			pointExtractRelevantAt(t, srv)

			out, err := runExtractRelevantCmd(t, "--path", page, "--context", "installation", "--json")
			if err == nil {
				t.Fatal("an over-cap body returned no error, so the process would exit 0")
			}
			assertNamesCap(t, err.Error())
			got := decodeExtractResult(t, out)
			assertNamesCap(t, got.Error)
			if len(got.ExtractedParts) != 0 || got.Total != nil {
				t.Errorf("over-cap body produced a partial result: %+v", got)
			}
			if calls.Load() != 0 {
				t.Errorf("the model was called %d times for an over-cap body", calls.Load())
			}
		})
	}
}

// Defends the boundary: a body of exactly the cap is read whole and succeeds,
// HTML or not, so the cap does not refuse a page it allows.
func TestExtractRelevantAcceptsABodyAtTheCap(t *testing.T) {
	plain := strings.Repeat("y", testBodyCap-4) + "tail"
	for _, tc := range []struct{ name, contentType, body, want string }{
		{"html", "text/html; charset=utf-8", sizedHTML(t, testBodyCap), "install with brew"},
		{"plain", "text/plain", plain, plain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lowerBodyCap(t)
			page := sizedServer(t, tc.contentType, tc.body)
			var prompt atomic.Value
			srv := fakeLLM(t, func(p string) string { prompt.Store(p); return "brew install" })
			pointExtractRelevantAt(t, srv)

			out, err := runExtractRelevantCmd(t, "--path", page, "--context", "installation", "--json")
			if err != nil {
				t.Fatalf("a body at the cap failed: %v\n%s", err, out)
			}
			got := decodeExtractResult(t, out)
			if len(got.ExtractedParts) != 1 || got.ExtractedParts[0] != "brew install" {
				t.Errorf("ExtractedParts = %v", got.ExtractedParts)
			}
			p, _ := prompt.Load().(string)
			if !strings.Contains(p, tc.want) {
				t.Errorf("the whole body did not reach the model; want %q in:\n%s", tc.want, p)
			}
		})
	}
}

// Defends extract-links against handing an unbounded body to the parser: a
// body one byte over the cap is an error naming the cap with no links.
func TestExtractLinksRefusesABodyOverTheCap(t *testing.T) {
	lowerBodyCap(t)
	page := sizedServer(t, "text/html", sizedHTML(t, testBodyCap+1))

	out, err := runLinksFields(t, "--url", page, "--json")
	if err == nil {
		t.Fatal("an over-cap body returned no error, so the process would exit 0")
	}
	assertNamesCap(t, err.Error())
	var got ExtractLinksResult
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("output is not JSON: %v\n%s", jerr, out)
	}
	assertNamesCap(t, got.Error)
	if len(got.Links) != 0 || got.Total != nil {
		t.Errorf("over-cap body produced a partial result: %+v", got)
	}
}

// Defends the extract-links boundary: a body of exactly the cap succeeds and
// its link is found.
func TestExtractLinksAcceptsABodyAtTheCap(t *testing.T) {
	lowerBodyCap(t)
	page := sizedServer(t, "text/html", sizedHTML(t, testBodyCap))

	out, err := runLinksFields(t, "--url", page, "--json")
	if err != nil {
		t.Fatalf("a body at the cap failed: %v\n%s", err, out)
	}
	var got ExtractLinksResult
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("output is not JSON: %v\n%s", jerr, out)
	}
	if len(got.Links) != 1 || !strings.HasSuffix(got.Links[0].Href, "/docs") {
		t.Errorf("Links = %+v", got.Links)
	}
}

// Defends the cap's source: it is the fetch tier's configured cap, so
// FETCH_MAX_BYTES moves both commands with it rather than a second number.
func TestRemoteBodyCapComesFromFetchConfig(t *testing.T) {
	t.Setenv("FETCH_MAX_BYTES", "4096")
	if got := loadRemoteBodyCap(); got != 4096 {
		t.Errorf("loadRemoteBodyCap() = %d, want 4096", got)
	}
}
