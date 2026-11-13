package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Defends the demo's hidden setup: the tape names one URL, so a wrong path must
// 404 instead of returning the page. Without this a typo in the tape records a
// successful-looking demo of a page the tool never asked for.
func TestHandlerServesOnlyTheDemoPage(t *testing.T) {
	body := []byte("<html>fixture</html>")
	srv := httptest.NewServer(handler(body))
	defer srv.Close()

	res, err := http.Get(srv.URL + demoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET %s = %d, want 200", demoPath, res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}

	for _, path := range []string{"/", "/docs-page.html.bak", "/../benchmarks/fixtures/docs-page.html"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
	}
}

// Defends the tape against a fixture that moved: the default page is the one
// the demo and the token benchmark both show, so a rename must fail here
// rather than in the operator's recording.
func TestDefaultPageIsTheCommittedFixture(t *testing.T) {
	// The package directory is docs/demo/serve-fixture, three levels below the root.
	path := filepath.Join("..", "..", "..", defaultPage)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("default page %s is missing (%v); the tape would fail to start its server", path, err)
	}
	if info.Size() == 0 {
		t.Errorf("default page %s is empty", path)
	}
}
