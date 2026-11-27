package commands

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// isReducePrompt reports whether a user prompt the fake model received is the
// reduce call's rather than a chunk's.
func isReducePrompt(user string) bool {
	return strings.HasPrefix(user, relevantReduceHeader("c"))
}

// chunkBody is the content a map prompt carries after its header.
func chunkBody(user string) string {
	const lead = "Content to extract from:\n\n"
	return user[strings.Index(user, lead)+len(lead):]
}

// firstParagraph is the number of the first paragraph of a paragraphDoc
// chunk, such as "p000300", which tells the chunks apart.
func firstParagraph(s string) string {
	return s[strings.Index(s, "p"):][:7]
}

// Defends the reduce pass itself: an input read in several chunks whose parts
// together fit one prompt is merged by exactly one more call, and that call is
// counted in model_calls and bytes_sent like every map call.
func TestExtractRelevantReduceMergesThePartsInOneCountedCall(t *testing.T) {
	doc := paragraphDoc(1000) // four chunks
	chunks := chunksFor(t, doc, "c")
	var reducePrompt string
	rec := newMapRecorder(t, func(user string) (string, int) {
		if isReducePrompt(user) {
			reducePrompt = user
			return "MERGED", http.StatusOK
		}
		return "part " + firstParagraph(chunkBody(user)), http.StatusOK
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"doc.md": doc})

	code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got := decodeExtractResult(t, out)
	if strings.Join(got.ExtractedParts, "|") != "MERGED" {
		t.Errorf("parts = %v, want the one merged answer", got.ExtractedParts)
	}
	if n := rec.calls.Load(); n != int64(len(chunks)+1) {
		t.Errorf("%d model calls, want %d map calls and one reduce", n, len(chunks))
	}
	if got.ModelCalls == nil || *got.ModelCalls != int64(len(chunks)+1) {
		t.Errorf("model_calls = %v, want %d: the reduce call is not counted", got.ModelCalls, len(chunks)+1)
	}
	if got.BytesSent == nil || *got.BytesSent != rec.bytes.Load() {
		t.Errorf("bytes_sent = %v, want %d: the reduce prompt is not counted", got.BytesSent, rec.bytes.Load())
	}
	// The reduce prompt carries every part, in document order.
	var want []string
	for _, c := range chunks {
		want = append(want, "part "+firstParagraph(c))
	}
	if !strings.HasSuffix(reducePrompt, strings.Join(want, relevantReduceSeparator)) {
		t.Errorf("the reduce prompt does not carry the parts in order:\n%q", reducePrompt)
	}
	if got.Message != "" || got.Truncated != "" || got.Total == nil || *got.Total != 1 {
		t.Errorf("a merged run reports message %q, truncated %q, total %v", got.Message, got.Truncated, got.Total)
	}
}

// Defends against reduce cutting what it merges: when the reduce prompt would
// be over one prompt, no reduce call is made, the parts come back unreduced in
// document order, and message says why. It is not truncated: the model read
// every chunk.
func TestExtractRelevantReduceOverOnePromptIsSkippedWithAMessage(t *testing.T) {
	doc := paragraphDoc(1000) // four chunks
	chunks := chunksFor(t, doc, "c")
	rec := newMapRecorder(t, func(user string) (string, int) {
		if isReducePrompt(user) {
			return "MERGED", http.StatusOK
		}
		// Four answers of 9,000 bytes join to more than one prompt.
		return firstParagraph(chunkBody(user)) + strings.Repeat("y", 9000), http.StatusOK
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"doc.md": doc})

	code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got := decodeExtractResult(t, out)
	if n := rec.calls.Load(); n != int64(len(chunks)) {
		t.Errorf("%d model calls, want only the %d map calls", n, len(chunks))
	}
	var want []string
	for _, c := range chunks {
		want = append(want, firstParagraph(c)+strings.Repeat("y", 9000))
	}
	if strings.Join(got.ExtractedParts, "|") != strings.Join(want, "|") {
		t.Errorf("the parts are not returned unreduced in document order: %d parts", len(got.ExtractedParts))
	}
	if !strings.Contains(got.Message, "reduce skipped") || !strings.Contains(got.Message, "one prompt") {
		t.Errorf("message does not say reduce was skipped for exceeding one prompt: %q", got.Message)
	}
	if strings.Contains(got.Message, "truncat") || strings.Contains(got.Message, "cut") {
		t.Errorf("message describes a skipped reduce as cut model input: %q", got.Message)
	}
	if got.Truncated != "" || got.Error != "" {
		t.Errorf("a skipped reduce reports truncated %q or error %q", got.Truncated, got.Error)
	}

	// The TOON default and plain text carry the message too, beside the parts.
	_, out, _ = runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--min")
	if !strings.Contains(out, "reduce skipped") {
		t.Errorf("--min drops the message:\n%.300s", out)
	}
	_, out, _ = runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--plain")
	if !strings.Contains(out, "reduce skipped") || !strings.Contains(out, want[0]) {
		t.Errorf("plain text drops the message or the parts:\n%.300s", out)
	}
	// With -o the plain summary on stdout still says the parts are unreduced.
	_, out, _ = runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--plain", "-o", filepath.Join(dir, "out.md"))
	if !strings.Contains(out, "Note: reduce skipped") {
		t.Errorf("the -o summary drops the message:\n%s", out)
	}
}

// Defends the boundary of "fits one prompt": a reduce prompt of exactly
// maxRelevantPromptBytes is sent, one byte more is not.
func TestExtractRelevantReduceRunsOnlyWhenItsWholePromptFits(t *testing.T) {
	doc := paragraphDoc(400) // two chunks
	chunks := chunksFor(t, doc, "c")
	if len(chunks) != 2 {
		t.Fatalf("%d chunks, want 2", len(chunks))
	}
	room := maxRelevantPromptBytes - len(relevantReduceHeader("c")) - len(relevantReduceSeparator)
	for _, tc := range []struct {
		name   string
		extra  int
		reduce bool
	}{{"exactly the cap", 0, true}, {"one byte over", 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			first := room / 2
			rec := newMapRecorder(t, func(user string) (string, int) {
				if isReducePrompt(user) {
					if len(user) > maxRelevantPromptBytes {
						t.Errorf("a reduce prompt of %d bytes was sent", len(user))
					}
					return "MERGED", http.StatusOK
				}
				if firstParagraph(chunkBody(user)) == firstParagraph(chunks[0]) {
					return strings.Repeat("a", first), http.StatusOK
				}
				return strings.Repeat("b", room-first+tc.extra), http.StatusOK
			})
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"doc.md": doc})
			code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--json")
			if code != 0 {
				t.Fatalf("exit %d:\n%.300s", code, out)
			}
			got := decodeExtractResult(t, out)
			if reduced := rec.calls.Load() == 3; reduced != tc.reduce {
				t.Errorf("%d calls; reduce made = %v, want %v", rec.calls.Load(), reduced, tc.reduce)
			}
			if !tc.reduce && len(got.ExtractedParts) != 2 {
				t.Errorf("%d parts, want both unreduced", len(got.ExtractedParts))
			}
		})
	}
}

// Defends the rule that a failure is never reported as success: a failed
// reduce call keeps the unreduced parts, names the reduce in error, and exits 1.
func TestExtractRelevantFailedReduceKeepsThePartsAndExits1(t *testing.T) {
	doc := paragraphDoc(1000) // four chunks
	chunks := chunksFor(t, doc, "c")
	rec := newMapRecorder(t, func(user string) (string, int) {
		if isReducePrompt(user) {
			return "", http.StatusBadRequest
		}
		return "part " + firstParagraph(chunkBody(user)), http.StatusOK
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"doc.md": doc})

	code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--json")
	if code != 1 {
		t.Fatalf("a failed reduce call exited %d, want 1:\n%s", code, out)
	}
	got := decodeExtractResult(t, out)
	var want []string
	for _, c := range chunks {
		want = append(want, "part "+firstParagraph(c))
	}
	if strings.Join(got.ExtractedParts, "|") != strings.Join(want, "|") {
		t.Errorf("kept parts = %v, want the unreduced %v", got.ExtractedParts, want)
	}
	if !strings.Contains(got.Error, "reduce") {
		t.Errorf("error does not name the reduce failure: %q", got.Error)
	}
	if n := rec.calls.Load(); n != int64(len(chunks)+1) {
		t.Errorf("%d calls, want %d map calls and one reduce", n, len(chunks))
	}
	// The failed reduce reached the model, so it is counted (epic 12 AC2).
	if got.ModelCalls == nil || *got.ModelCalls != int64(len(chunks)+1) {
		t.Errorf("model_calls = %v, want the %d map calls and the failed reduce", got.ModelCalls, len(chunks))
	}
	if got.Total != nil || got.Message != "" {
		t.Errorf("a failed run reports a total or a message: %+v", got)
	}
}

// Defends reduce cost: nothing to merge means no reduce call. A one-chunk
// input, a several-chunk input with one part, and a map pass with a failed
// chunk all make only their map calls.
func TestExtractRelevantReduceIsNotCalledWithNothingToMerge(t *testing.T) {
	big := paragraphDoc(600) // three chunks
	chunks := chunksFor(t, big, "c")
	for _, tc := range []struct {
		name  string
		doc   string
		reply func(body string) (string, int)
		code  int
		calls int
	}{
		{"one chunk", "small body\n", func(string) (string, int) { return "a part", http.StatusOK }, 0, 1},
		{"one part", big, func(body string) (string, int) {
			if firstParagraph(body) == firstParagraph(chunks[1]) {
				return "the only part", http.StatusOK
			}
			return "NONE", http.StatusOK
		}, 0, len(chunks)},
		{"failed chunk", big, func(body string) (string, int) {
			if firstParagraph(body) == firstParagraph(chunks[1]) {
				return "", http.StatusBadRequest
			}
			return "a part", http.StatusOK
		}, 1, len(chunks)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newMapRecorder(t, func(user string) (string, int) {
				if isReducePrompt(user) {
					t.Error("a reduce call was made with nothing to merge")
					return "MERGED", http.StatusOK
				}
				return tc.reply(chunkBody(user))
			})
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"doc.md": tc.doc})
			code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--json")
			if code != tc.code || rec.calls.Load() != int64(tc.calls) {
				t.Errorf("exit %d after %d calls, want exit %d after %d:\n%.300s", code, rec.calls.Load(), tc.code, tc.calls, out)
			}
		})
	}
}

// Defends the reduce pass as per input, like --max-chunks: in a directory run
// each file's parts are merged under its own heading, a skipped reduce names
// its file in message, and small files cost no reduce call.
func TestExtractRelevantDirectoryReducesEachFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.md": paragraphDoc(600), "b.md": paragraphDoc(600), "small.md": "small body\n"})
	aChunks := chunksFor(t, paragraphDoc(600), "File: a.md\n\nContext: c")
	rec := newMapRecorder(t, func(user string) (string, int) {
		switch {
		case strings.HasPrefix(user, relevantReduceHeader("File: a.md\n\nContext: c")):
			return "MERGED a", http.StatusOK
		case strings.HasPrefix(user, relevantReduceHeader("File: b.md\n\nContext: c")):
			t.Error("b.md's parts join to more than one prompt, yet a reduce call was made")
			return "MERGED b", http.StatusOK
		case strings.HasPrefix(user, "Context: File: b.md"):
			return strings.Repeat("b", 12000), http.StatusOK
		}
		return "a part", http.StatusOK
	})

	code, out, _ := runCLI(t, "extract-relevant", "--path", dir, "--context", "c", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%.300s", code, out)
	}
	got := decodeExtractResult(t, out)
	if len(got.ExtractedParts) != 3 || got.ExtractedParts[0] != "## a.md\n\nMERGED a" {
		t.Errorf("a.md is not merged under its heading: %.200q", got.ExtractedParts)
	}
	if !strings.Contains(got.Message, "b.md: reduce skipped") || strings.Contains(got.Message, "a.md") {
		t.Errorf("message does not name b.md's skipped reduce alone: %q", got.Message)
	}
	if want := int64(2*len(aChunks) + 1 + 1); rec.calls.Load() != want || got.ModelCalls == nil || *got.ModelCalls != want {
		t.Errorf("%d calls, model_calls %v; want %d", rec.calls.Load(), got.ModelCalls, want)
	}
	if got.ProcessedFiles != 3 {
		t.Errorf("processed_files = %d, want 3", got.ProcessedFiles)
	}
}

// Defends against a merge that loses everything: a reduce answering NONE over
// parts the map pass found keeps those parts, unreduced, and says so, rather
// than reporting no relevant content.
func TestExtractRelevantReduceAnsweringNoneKeepsTheParts(t *testing.T) {
	doc := paragraphDoc(600)
	chunks := chunksFor(t, doc, "c")
	newMapRecorder(t, func(user string) (string, int) {
		if isReducePrompt(user) {
			return "NONE", http.StatusOK
		}
		return "a part", http.StatusOK
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"doc.md": doc})
	code, out, _ := runCLI(t, "extract-relevant", "--path", filepath.Join(dir, "doc.md"), "--context", "c", "--json")
	got := decodeExtractResult(t, out)
	if code != 0 || len(got.ExtractedParts) != len(chunks) {
		t.Errorf("exit %d, %d parts; want exit 0 and all %d unreduced parts", code, len(got.ExtractedParts), len(chunks))
	}
	if !strings.Contains(got.Message, "unreduced") || got.Message == noRelevantMessage {
		t.Errorf("message = %q, want it to say the parts are unreduced", got.Message)
	}
}

// Defends a cancelled run waiting for a --concurrency slot: the reduce is
// never sent, the error names the reduce, and the parts are kept unreduced,
// never dropped or reported as merged.
func TestExtractRelevantReduceCancelledWhileWaitingKeepsTheParts(t *testing.T) {
	saved := relevantCallSlots
	t.Cleanup(func() { relevantCallSlots = saved })
	relevantCallSlots = make(chan struct{}, 1)
	relevantCallSlots <- struct{}{} // every slot is taken
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := chunkedRead{parts: []string{"one", "two"}, read: 2, total: 2}
	reduceParts(ctx, nil, &r, "c")
	if r.err == nil || !strings.Contains(r.err.Error(), "reduce") || !strings.Contains(r.err.Error(), context.Canceled.Error()) {
		t.Errorf("err = %v, want it to name the reduce and the cancel", r.err)
	}
	if strings.Join(r.parts, "|") != "one|two" {
		t.Errorf("parts = %v, want both kept unreduced", r.parts)
	}
}
