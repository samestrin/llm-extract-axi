// Command serve-fixture serves the demo page on the loopback interface, so the
// recording in docs/demo/llm-extract.tape needs no outside host.
//
// The tape starts it in a hidden step and kills it in another:
//
//	go build -o build/serve-fixture ./docs/demo/serve-fixture
//	build/serve-fixture &
//
// Usage, from the repository root:
//
//	go run ./docs/demo/serve-fixture [-addr 127.0.0.1:8765] [-page benchmarks/fixtures/docs-page.html]
//
// It serves exactly one path, /docs-page.html, and 404s everything else: a typo
// in the tape must fail the run, not quietly return a page that makes the demo
// look right.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

// demoPath is the only path served. The tape's commands name it.
const demoPath = "/docs-page.html"

// defaultPage is the fixture the demo shows, relative to the repository root.
// The token benchmark (benchmarks/README.md) measures the same page.
var defaultPage = filepath.Join("benchmarks", "fixtures", "docs-page.html")

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "loopback address to listen on")
	page := flag.String("page", defaultPage, "page to serve at "+demoPath)
	flag.Parse()

	body, err := os.ReadFile(*page)
	if err != nil {
		log.Fatalf("serve-fixture: %v", err)
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("serve-fixture: %v", err)
	}
	fmt.Printf("serving %s on http://%s%s\n", *page, listener.Addr(), demoPath)
	log.Fatal(http.Serve(listener, handler(body)))
}

// handler serves page at demoPath and nothing else.
func handler(page []byte) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(demoPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	return mux
}
