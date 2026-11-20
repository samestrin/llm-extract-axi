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
)

// fetchFieldsMessage builds the exact error both --fields parsers must use,
// so a drift in fetch's wording away from extract-links' goes red.
func fetchFieldsMessage(bad, valid string) string {
	return fmt.Sprintf("invalid argument %q for \"--fields\" flag: unknown field; valid fields: %s", bad, valid)
}

// errorMessage reads the message out of run()'s JSON error document.
func errorMessage(t *testing.T, stdout string) string {
	t.Helper()
	var doc struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not a JSON error document: %v\n%s", err, stdout)
	}
	if !doc.Error {
		t.Fatalf("error document does not say error: %s", stdout)
	}
	return doc.Message
}

// Defends epic 10 T2: --fields prints only the named envelope fields, under
// TOON and --json alike, and a selected zero value (bytes 0, truncated false)
// is still printed rather than dropped as empty. Printing the whole envelope,
// or dropping a selected field because its value is zero, turns this red.
func TestFetchFieldsSelectsEnvelopeFields(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	pages := []fetchPage{
		{name: "small", contentType: "text/plain", body: "hello"},
		{name: "empty", contentType: "text/plain", body: ""},
		{name: "over 4096", contentType: "text/html", body: strings.Repeat("a", 5000)},
	}
	for _, p := range pages {
		url := servePage(t, p)
		for _, asJSON := range []bool{false, true} {
			mode := "toon"
			if asJSON {
				mode = "json"
			}
			t.Run(p.name+"/"+mode, func(t *testing.T) {
				args := []string{"fetch", url, "--fields", "url,bytes,truncated"}
				if asJSON {
					args = append(args, "--json")
				}
				code, stdout, stderr := runCLI(t, args...)
				if code != 0 {
					t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
				}
				doc := decodeFetchDoc(t, stdout, asJSON)
				if len(doc) != 3 {
					t.Errorf("envelope carries more than the selected fields: %v", doc)
				}
				if doc["url"] != url {
					t.Errorf("url = %v, want %q", doc["url"], url)
				}
				if docNumber(doc, "bytes") != fmt.Sprint(len(p.body)) {
					t.Errorf("bytes = %v, want %d", doc["bytes"], len(p.body))
				}
				wantTrunc := len(p.body) > defaultHead
				if doc["truncated"] != wantTrunc {
					t.Errorf("truncated = %v, want %v", doc["truncated"], wantTrunc)
				}
				for _, k := range []string{"status", "tier", "format", "content_type", "body", "help"} {
					if _, ok := doc[k]; ok {
						t.Errorf("unselected field %q printed: %v", k, doc)
					}
				}
			})
		}
	}
}

// Defends the body field under --fields: the cut still applies, help[] comes
// only when asked for, and names are trimmed and case-folded exactly as
// extract-links --fields does them.
func TestFetchFieldsBodyAndHelp(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	page := strings.Repeat("b", 5000)
	url := servePage(t, fetchPage{contentType: "text/plain", body: page})

	code, stdout, _ := runCLI(t, "fetch", url, "--fields", " BODY , Help ", "--head", "10")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	doc := decodeFetchDoc(t, stdout, false)
	if len(doc) != 2 || doc["body"] != page[:10] {
		t.Errorf("body/help selection wrong: %v", doc)
	}
	help := fmt.Sprint(doc["help"])
	if !strings.Contains(help, "--full") || !strings.Contains(help, "--output <file>") {
		t.Errorf("help[] does not name --full and --output <file>: %v", doc["help"])
	}

	// help belongs to TOON only: selecting it under --json yields nothing,
	// as no other command's --json carries help[] either.
	code, stdout, _ = runCLI(t, "fetch", url, "--fields", "help", "--json")
	if code != 0 {
		t.Fatalf("--fields help --json exit %d: %s", code, stdout)
	}
	doc = decodeFetchDoc(t, stdout, true)
	if len(doc) != 0 {
		t.Errorf("--json carries help[]: %v", doc)
	}
}

// Defends the unknown-field rule: exit 2 before any request, with the exact
// wording extract-links --fields uses, listing fetch's own valid fields. A
// wording drift between the two parsers, or a request sent for a malformed
// invocation, turns this red.
func TestFetchFieldsUnknownIsUsageError(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("x"))
	}))
	t.Cleanup(srv.Close)

	valid := strings.Join(fetchFieldNames, ", ")
	// Under the default TOON too: the exit code comes from the wording, not
	// from the encoding the error is printed in.
	code, stdout, _ := runCLI(t, "fetch", srv.URL, "--fields", "bogus")
	if code != 2 || !strings.Contains(stdout, "unknown field") || !strings.Contains(stdout, "valid fields") {
		t.Errorf("TOON unknown field: exit %d, stdout=%q", code, stdout)
	}
	for _, bad := range []string{"bogus", "Href", ""} {
		spec := "url," + bad
		code, stdout, _ := runCLI(t, "fetch", srv.URL, "--fields", spec, "--json")
		if code != 2 {
			t.Errorf("--fields %q: exit %d, want 2; stdout=%q", spec, code, stdout)
		}
		want := fetchFieldsMessage(strings.ToLower(bad), valid)
		if got := errorMessage(t, stdout); got != want {
			t.Errorf("--fields %q message:\n got %q\nwant %q", spec, got, want)
		}
	}
	if hits != 0 {
		t.Errorf("a usage error sent %d requests", hits)
	}

	// The same unknown name through extract-links: the sentence around the
	// valid list must be identical, only the list differs.
	code, stdout, _ = runCLI(t, "extract-links", "--url", srv.URL, "--fields", "bogus", "--json")
	if code != 2 {
		t.Fatalf("extract-links --fields bogus: exit %d, want 2", code)
	}
	got := errorMessage(t, stdout)
	linkWant := fetchFieldsMessage("bogus", strings.Join(linkFields, ", "))
	if got != linkWant {
		t.Errorf("extract-links wording changed:\n got %q\nwant %q", got, linkWant)
	}
	fetchPrefix, _, _ := strings.Cut(fetchFieldsMessage("bogus", valid), "valid fields:")
	linkPrefix, _, _ := strings.Cut(got, "valid fields:")
	if fetchPrefix != linkPrefix {
		t.Errorf("fetch and extract-links --fields wording differ: %q vs %q", fetchPrefix, linkPrefix)
	}
}

// Defends --fields with --output: the file still holds the whole body, and
// the envelope carries only the selected fields. body is absent from the
// --output envelope, so selecting it adds nothing.
func TestFetchFieldsWithOutput(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	page := strings.Repeat("c", 5000)
	url := servePage(t, fetchPage{contentType: "text/plain", body: page})
	outFile := filepath.Join(t.TempDir(), "page.txt")

	code, stdout, stderr := runCLI(t, "fetch", url, "--output", outFile, "--fields", "output,bytes")
	if code != 0 {
		t.Fatalf("exit %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	got, err := os.ReadFile(outFile)
	if err != nil || string(got) != page {
		t.Errorf("file holds %d bytes, want the %d served (err %v)", len(got), len(page), err)
	}
	doc := decodeFetchDoc(t, stdout, false)
	if len(doc) != 2 || doc["output"] != outFile || docNumber(doc, "bytes") != "5000" {
		t.Errorf("--output selection wrong: %v", doc)
	}

	// body is not in the --output envelope; asking for it yields an empty
	// document, not the body the envelope never carried.
	code, stdout, _ = runCLI(t, "fetch", url, "--output", outFile, "--fields", "body", "--json")
	if code != 0 {
		t.Fatalf("--fields body --output exit %d: %s", code, stdout)
	}
	if doc := decodeFetchDoc(t, stdout, true); len(doc) != 0 {
		t.Errorf("--output envelope grew a body: %v", doc)
	}
}

// Defends --fields with --plain (and --axi=false): plain mode prints no
// envelope, so the combination is a usage error, exit 2, before any request
// and before --output writes a file. Silently ignoring --fields would tell
// the caller it got a selection when it got the raw body.
func TestFetchFieldsWithPlainIsUsageError(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("x"))
	}))
	t.Cleanup(srv.Close)
	outFile := filepath.Join(t.TempDir(), "page.txt")

	for _, args := range [][]string{
		{"--fields", "url", "--plain"},
		{"--fields", "url", "--axi=false"},
		{"--fields", "url", "--plain", "--output", outFile},
	} {
		code, stdout, _ := runCLI(t, append([]string{"fetch", srv.URL}, args...)...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2; stdout=%q", args, code, stdout)
		}
		if !strings.Contains(stdout, "--fields") || !strings.Contains(stdout, "--plain") {
			t.Errorf("%v: error does not name --fields and --plain: %q", args, stdout)
		}
	}
	if hits != 0 {
		t.Errorf("a usage error sent %d requests", hits)
	}
	if _, err := os.Stat(outFile); !os.IsNotExist(err) {
		t.Errorf("a usage error wrote the output file (err %v)", err)
	}
}

// Defends the empty spec and the absent field: --fields "" is the default
// envelope, and a selected field the run does not carry (output without
// --output) stays absent rather than printing an empty placeholder.
func TestFetchFieldsEmptySpecAndAbsentField(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	url := servePage(t, fetchPage{contentType: "text/plain", body: "hi"})

	code, stdout, _ := runCLI(t, "fetch", url, "--fields", "", "--json")
	if code != 0 {
		t.Fatalf("--fields \"\" exit %d: %s", code, stdout)
	}
	doc := decodeFetchDoc(t, stdout, true)
	for _, k := range []string{"url", "status", "tier", "format", "content_type", "bytes", "truncated", "body"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("--fields \"\" dropped %q from the default envelope: %v", k, doc)
		}
	}

	code, stdout, _ = runCLI(t, "fetch", url, "--fields", "output", "--json")
	if code != 0 {
		t.Fatalf("--fields output exit %d: %s", code, stdout)
	}
	if doc := decodeFetchDoc(t, stdout, true); len(doc) != 0 {
		t.Errorf("absent output printed as a placeholder: %v", doc)
	}
}