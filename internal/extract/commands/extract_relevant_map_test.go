package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goaxi "github.com/samestrin/go-axi"
)

// mapRecorder stands in for the model endpoint during a map pass. reply gets
// each call's user message and returns the answer and an HTTP status; every
// call is counted, and the bytes of the answered ones are summed the way
// bytes_sent counts them: system prompt plus user prompt. sent sums the same
// bytes over every call, failed ones included.
type mapRecorder struct {
	calls    atomic.Int64
	answered atomic.Int64
	bytes    atomic.Int64
	sent     atomic.Int64
	inFlight atomic.Int64
	peak     atomic.Int64
	mu       sync.Mutex
	prompts  []string
}

func newMapRecorder(t *testing.T, reply func(user string) (string, int)) *mapRecorder {
	t.Helper()
	rec := &mapRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := rec.inFlight.Add(1)
		defer rec.inFlight.Add(-1)
		for {
			p := rec.peak.Load()
			if n <= p || rec.peak.CompareAndSwap(p, n) {
				break
			}
		}
		rec.calls.Add(1)
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var system, user string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				user = m.Content
			}
		}
		rec.sent.Add(int64(len(system) + len(user)))
		rec.mu.Lock()
		rec.prompts = append(rec.prompts, user)
		rec.mu.Unlock()
		// Held briefly so concurrent calls overlap and the peak is real.
		time.Sleep(5 * time.Millisecond)
		answer, status := reply(user)
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"chunk exploded"}}`))
			return
		}
		rec.answered.Add(1)
		rec.bytes.Add(int64(len(system) + len(user)))
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": answer}}}})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	pointExtractRelevantAt(t, srv)
	noRetryDelay(t)
	return rec
}

// mapCalls counts the map prompts the fake received, leaving out the reduce
// call that follows them.
func (rec *mapRecorder) mapCalls() int64 {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var n int64
	for _, p := range rec.prompts {
		if strings.Contains(p, "Content to extract from:") {
			n++
		}
	}
	return n
}

// paragraphDoc is n 100-byte paragraphs, each led by its number, so a chunk
// can be told by the paragraphs it holds.
func paragraphDoc(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		line := fmt.Sprintf("p%06d ", i)
		b.WriteString(line + strings.Repeat("x", 98-len(line)) + "\n\n")
	}
	return b.String()
}

// chunksFor is the chunking the map pass must use for content under the
// prompt header contextText builds.
func chunksFor(t *testing.T, content, contextText string) []string {
	t.Helper()
	chunks, err := chunkContent(content, len(relevantPromptHeader(contextText)))
	if err != nil {
		t.Fatal(err)
	}
	return chunks
}

// Defends epic 12's reason for being: the prompt was cut at 30,000 bytes, so
// a fact past the cut was never read. A fact only in the fifth chunk of a
// file is found, and every chunk was sent.
func TestExtractRelevantFindsAFactOnlyInChunk5(t *testing.T) {
	const needle = "NEEDLE-IN-CHUNK-FIVE"
	doc := paragraphDoc(1500) // 150,000 bytes: six chunks
	doc = strings.Replace(doc, "p001330 "+strings.Repeat("x", len(needle)), "p001330 "+needle, 1)
	chunks := chunksFor(t, doc, "c")
	if len(chunks) < 6 {
		t.Fatalf("the document makes %d chunks, want at least 6", len(chunks))
	}
	for i, c := range chunks {
		if strings.Contains(c, needle) != (i == 4) {
			t.Fatalf("the needle is in chunk %d; the test needs it only in chunk 5", i+1)
		}
	}
	rec := newMapRecorder(t, func(user string) (string, int) {
		if strings.Contains(user, needle) {
			return "found " + needle, http.StatusOK
		}
		return "NONE", http.StatusOK
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"doc.md": doc})

	code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got := decodeExtractResult(t, out)
	if len(got.ExtractedParts) != 1 || !strings.Contains(got.ExtractedParts[0], needle) {
		t.Errorf("the fact in chunk 5 was not found: %+v", got.ExtractedParts)
	}
	if got.Truncated != "" {
		t.Errorf("a file under --max-chunks reports truncated: %q", got.Truncated)
	}
	if n := rec.calls.Load(); n != int64(len(chunks)) {
		t.Errorf("%d model calls, want one per chunk (%d)", n, len(chunks))
	}
}

// Defends VISION's visible cost: a chunked run makes many calls, and
// model_calls and bytes_sent must be what the model actually received, in
// --json, --min (mc, bs) and the TOON default.
func TestExtractRelevantModelCallsAndBytesSentEqualTheRealCalls(t *testing.T) {
	doc := paragraphDoc(1000) // four chunks
	want := len(chunksFor(t, doc, "c"))
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"doc.md": doc})
	path := filepath.Join(dir, "doc.md")

	rec := newMapRecorder(t, func(string) (string, int) { return "a part", http.StatusOK })
	code, out, _ := runCLI(t, "extract-relevant", "--path", path, "--context", "c", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got := decodeExtractResult(t, out)
	// One call per chunk, then one reduce merging the parts.
	if rec.calls.Load() != int64(want)+1 || rec.mapCalls() != int64(want) {
		t.Fatalf("the fake saw %d calls (%d map), want %d map calls and the reduce", rec.calls.Load(), rec.mapCalls(), want)
	}
	if got.ModelCalls == nil || *got.ModelCalls != rec.answered.Load() {
		t.Errorf("model_calls = %v, want %d", got.ModelCalls, rec.answered.Load())
	}
	if got.BytesSent == nil || *got.BytesSent != rec.bytes.Load() {
		t.Errorf("bytes_sent = %v, want %d", got.BytesSent, rec.bytes.Load())
	}
	if len(got.ExtractedParts) != 1 {
		t.Errorf("%d parts kept, want the one merged part", len(got.ExtractedParts))
	}

	_, out, _ = runCLI(t, "extract-relevant", "--path", path, "--context", "c", "--min")
	if !strings.Contains(out, fmt.Sprintf("mc: %d", want+1)) {
		t.Errorf("--min does not carry mc: %d:\n%s", want+1, out)
	}
	_, out, _ = runCLI(t, "extract-relevant", "--path", path, "--context", "c")
	if !strings.Contains(out, fmt.Sprintf("model_calls: %d", want+1)) {
		t.Errorf("TOON does not carry model_calls: %d:\n%s", want+1, out)
	}
}

// Defends --max-chunks as the cost bound: a URL body over it is read only as
// far as the bound, and the result says truncated with the chunks read and
// the total, and help[] names --max-chunks.
func TestExtractRelevantOverMaxChunksReportsTruncated(t *testing.T) {
	doc := paragraphDoc(1500)
	total := len(chunksFor(t, doc, "c"))
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(doc))
	}))
	t.Cleanup(page.Close)
	rec := newMapRecorder(t, func(string) (string, int) { return "a part", http.StatusOK })

	code, out, _ := runCLI(t, "extract-relevant", "--path", page.URL, "--context", "c", "--max-chunks", "2", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got := decodeExtractResult(t, out)
	if n := rec.mapCalls(); n != 2 {
		t.Errorf("%d map calls past --max-chunks 2", n)
	}
	if want := fmt.Sprintf("read 2 of %d chunks", total); !strings.Contains(got.Truncated, want) {
		t.Errorf("truncated = %q, want it to say %q", got.Truncated, want)
	}
	if !strings.Contains(got.Truncated, "--max-chunks") {
		t.Errorf("truncated does not name --max-chunks: %q", got.Truncated)
	}

	withAXI(t)
	_, out, _ = runCLI(t, "extract-relevant", "--path", page.URL, "--context", "c", "--max-chunks", "2")
	toon, err := goaxi.Decode(strings.NewReader(out))
	if err != nil {
		t.Fatalf("not TOON: %v\n%s", err, out)
	}
	help, _ := toon.(map[string]any)["help"].([]any)
	if !strings.Contains(fmt.Sprint(help...), "--max-chunks") {
		t.Errorf("help[] does not name --max-chunks: %v", help)
	}
}

// Defends the bounds against overflow: a huge --max-chunks must not wrap the
// read limit or the run deadline negative, which would read nothing or time
// out at once and report it as the input's fault.
func TestChunkBoundsSaturate(t *testing.T) {
	if got := chunkReadLimit(3); got != 3*maxRelevantPromptBytes {
		t.Errorf("chunkReadLimit(3) = %d", got)
	}
	if got := chunkReadLimit(math.MaxInt); got != math.MaxInt64-1 {
		t.Errorf("chunkReadLimit(MaxInt) = %d, want it saturated", got)
	}
	if got := runDeadline(60, 2, 8); got != 16*time.Minute {
		t.Errorf("runDeadline(60, 2, 8) = %v, want 16m", got)
	}
	if got := runDeadline(60, 2, math.MaxInt); got != time.Duration(math.MaxInt64) {
		t.Errorf("runDeadline with a huge --max-chunks = %v, want it saturated", got)
	}
}

// Defends the error wording for one failed chunk of several: it names the
// chunk and how many were read, and keeps the parts of the others.
func TestExtractRelevantOneFailedChunkIsNamed(t *testing.T) {
	doc := paragraphDoc(600) // three chunks
	chunks := chunksFor(t, doc, "c")
	marker := chunks[2][strings.LastIndex(chunks[2], "p"):][:7] // the last paragraph, in chunk 3 alone
	newMapRecorder(t, func(user string) (string, int) {
		if strings.Contains(user, marker) {
			return "", http.StatusBadRequest
		}
		return "a part", http.StatusOK
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"doc.md": doc})
	code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--json")
	got := decodeExtractResult(t, out)
	if code != 1 || !strings.Contains(got.Error, fmt.Sprintf("chunk 3 of %d failed", len(chunks))) || len(got.ExtractedParts) != len(chunks)-1 {
		t.Errorf("exit %d, error %q, %d parts; want exit 1 naming chunk 3 and the other parts kept", code, got.Error, len(got.ExtractedParts))
	}
}

// Defends the bounded read of a named file: it is read up to --max-chunks
// chunks of bytes plus one byte, so a file past that reports truncated with
// "at least" for the total, since the rest was never read to count it.
func TestExtractRelevantFileOverMaxChunksReportsTruncated(t *testing.T) {
	doc := paragraphDoc(1500) // six chunks
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"doc.md": doc})
	rec := newMapRecorder(t, func(string) (string, int) { return "a part", http.StatusOK })

	code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--max-chunks", "2", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got := decodeExtractResult(t, out)
	read := doc[:2*maxRelevantPromptBytes+1]
	if want := fmt.Sprintf("read 2 of at least %d chunks", len(chunksFor(t, read, "c"))); !strings.Contains(got.Truncated, want) {
		t.Errorf("truncated = %q, want it to say %q", got.Truncated, want)
	}
	if n := rec.mapCalls(); n != 2 {
		t.Errorf("%d map calls, want 2", n)
	}
}

// Defends the same bound per directory file: a file over --max-chunks was
// skipped as too_large at 100 KiB, and is now read up to the bound and named
// in truncated, while a small file beside it is read whole.
func TestExtractRelevantDirectoryFileOverMaxChunksReportsTruncated(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"big.md": paragraphDoc(1500), "small.md": "small body\n"})
	rec := newMapRecorder(t, func(string) (string, int) { return "a part", http.StatusOK })

	code, out, _ := runCLI(t, "extract-relevant", "--path", dir, "--context", "c", "--max-chunks", "3", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got := decodeExtractResult(t, out)
	if !strings.Contains(got.Truncated, "big.md") || !strings.Contains(got.Truncated, "read 3 of at least") {
		t.Errorf("truncated does not name big.md read to the bound: %q", got.Truncated)
	}
	if strings.Contains(got.Truncated, "small.md") {
		t.Errorf("truncated names a file read whole: %q", got.Truncated)
	}
	if got.Skipped["too_large"] != 0 || got.TotalFiles != 2 || got.ProcessedFiles != 2 {
		t.Errorf("a large text file was not read: %+v", got)
	}
	// big.md's three parts are merged by one reduce call; small.md's one
	// part needs none.
	if n := rec.mapCalls(); n != 4 || got.ModelCalls == nil || *got.ModelCalls != 5 {
		t.Errorf("%d map calls, model_calls %v; want 3 map calls for big.md, 1 for small.md and big.md's reduce", n, got.ModelCalls)
	}
}

// Defends the rule that a failure is never read as NONE: a failed chunk call
// keeps the parts of the chunks that answered, in document order, names the
// failed chunk numbers in error, and exits 1.
func TestExtractRelevantFailedChunkKeepsTheGoodPartsAndExits1(t *testing.T) {
	doc := paragraphDoc(1200) // five chunks
	chunks := chunksFor(t, doc, "c")
	if len(chunks) < 5 {
		t.Fatalf("%d chunks, want 5", len(chunks))
	}
	// Each chunk is answered with the number of its first paragraph, so the
	// order of the kept parts is checkable.
	first := func(s string) string { return s[strings.Index(s, "p"):][:7] }
	failing := map[string]bool{first(chunks[1]): true, first(chunks[3]): true}
	newMapRecorder(t, func(user string) (string, int) {
		body := user[strings.Index(user, "Content to extract from:\n\n")+len("Content to extract from:\n\n"):]
		if failing[first(body)] {
			return "", http.StatusBadRequest
		}
		return "part " + first(body), http.StatusOK
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"doc.md": doc})

	code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--concurrency", "3", "--json")
	if code != 1 {
		t.Fatalf("a failed chunk call exited %d, want 1:\n%s", code, out)
	}
	got := decodeExtractResult(t, out)
	var want []string
	for i, c := range chunks {
		if i != 1 && i != 3 {
			want = append(want, "part "+first(c))
		}
	}
	if strings.Join(got.ExtractedParts, "|") != strings.Join(want, "|") {
		t.Errorf("kept parts = %v, want %v in document order", got.ExtractedParts, want)
	}
	if !strings.Contains(got.Error, "chunks 2, 4") || !strings.Contains(got.Error, fmt.Sprintf("of %d", len(chunks))) {
		t.Errorf("error does not name chunks 2 and 4: %q", got.Error)
	}
	if got.Total != nil || got.Message != "" {
		t.Errorf("a failed run reports a total or an empty-result message: %+v", got)
	}
}

// Defends epic 12 AC2, every model call counted: a chunk call and a reduce
// call that reached the model and got an error still cost a request, so they
// are in model_calls and their prompts in bytes_sent. Counting only the calls
// that answered under-reported the cost of a partial run.
func TestExtractRelevantFailedCallsAreCountedInModelCallsAndBytesSent(t *testing.T) {
	doc := paragraphDoc(1000) // four chunks
	chunks := chunksFor(t, doc, "c")
	cases := []struct {
		name  string
		fails func(user string) bool
	}{
		{"failed chunk", func(user string) bool {
			return !isReducePrompt(user) && firstParagraph(chunkBody(user)) == firstParagraph(chunks[1])
		}},
		{"failed reduce", isReducePrompt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newMapRecorder(t, func(user string) (string, int) {
				if tc.fails(user) {
					return "", http.StatusBadRequest
				}
				return "a part", http.StatusOK
			})
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"doc.md": doc})

			code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--json")
			if code != 1 {
				t.Fatalf("a failed call exited %d, want 1:\n%s", code, out)
			}
			got := decodeExtractResult(t, out)
			if rec.answered.Load() == rec.calls.Load() {
				t.Fatalf("no call failed: %d calls, all answered", rec.calls.Load())
			}
			if got.ModelCalls == nil || *got.ModelCalls != rec.calls.Load() {
				t.Errorf("model_calls = %v, want all %d calls the model received", got.ModelCalls, rec.calls.Load())
			}
			if got.BytesSent == nil || *got.BytesSent != rec.sent.Load() {
				t.Errorf("bytes_sent = %v, want the %d bytes of every call sent", got.BytesSent, rec.sent.Load())
			}
		})
	}
}

// Defends the line docs/output.md draws for epic 12 AC2: a call whose request
// reached the model and then timed out is counted, because the model may have
// run it; only a model that was never reached goes uncounted.
func TestExtractRelevantTimedOutCallIsCounted(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Drained so the server sees the client hang up; held past --timeout.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	pointExtractRelevantAt(t, srv)
	noRetryDelay(t)
	src := filepath.Join(t.TempDir(), "a.md")
	writeFiles(t, filepath.Dir(src), map[string]string{"a.md": "text"})

	code, out, _ := runCLI(t, "extract-relevant", "--path", src, "--context", "c", "--timeout", "1", "--json")
	if code == 0 {
		t.Fatalf("a timed-out call exited 0:\n%s", out)
	}
	got := decodeExtractResult(t, out)
	if calls.Load() == 0 {
		t.Fatal("the fake model never received the request")
	}
	if got.ModelCalls == nil || *got.ModelCalls != calls.Load() || got.BytesSent == nil || *got.BytesSent == 0 {
		t.Errorf("model_calls = %v, bytes_sent = %v; want the %d requests the model received", got.ModelCalls, got.BytesSent, calls.Load())
	}
}

// Defends --concurrency as the bound on chunk calls in flight, in a directory
// run as much as on one file: files and their chunks share one limit.
func TestExtractRelevantChunkCallsStayWithinConcurrency(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.md": paragraphDoc(1000), "b.md": paragraphDoc(1000)})
	rec := newMapRecorder(t, func(string) (string, int) { return "NONE", http.StatusOK })

	code, out, _ := runCLI(t, "extract-relevant", "--path", dir, "--context", "c", "--concurrency", "2", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if p := rec.peak.Load(); p > 2 {
		t.Errorf("%d model calls in flight, over --concurrency 2", p)
	}
	if rec.calls.Load() < 8 {
		t.Errorf("%d calls, want every chunk of both files", rec.calls.Load())
	}
}

// Defends --max-chunks below 1: zero chunks would read nothing and report
// success. It is a usage error, exit 2, before any model call.
func TestExtractRelevantMaxChunksBelowOneIsRefused(t *testing.T) {
	rec := newMapRecorder(t, func(string) (string, int) { return "x", http.StatusOK })
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.md": "text"})
	for _, n := range []string{"0", "-1"} {
		code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "a.md"), "--context", "c", "--max-chunks", n)
		if code != 2 || !strings.Contains(out, "max-chunks") {
			t.Errorf("--max-chunks %s: exit %d, want 2 naming the flag:\n%s", n, code, out)
		}
	}
	if n := rec.calls.Load(); n != 0 {
		t.Errorf("%d model calls for a refused --max-chunks", n)
	}
}
