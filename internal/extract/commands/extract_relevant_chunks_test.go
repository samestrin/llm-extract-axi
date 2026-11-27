package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/samestrin/llm-extract-axi/v2/pkg/llmapi"
)

// chunkTestContexts are the two context shapes extractRelevantContent sees:
// a single file or URL passes --context as-is; a directory run prefixes the
// File: line (processDirectory).
var chunkTestContexts = []string{
	"API endpoint definitions",
	"File: sub/dir/doc.md\n\nContext: API endpoint definitions",
}

// chunkSizeFor is the per-chunk byte budget the tests expect: the whole-prompt
// cap minus the header extractRelevantContent builds around the content.
func chunkSizeFor(t *testing.T, contextText string) int {
	t.Helper()
	return maxRelevantPromptBytes - len(relevantPromptHeader(contextText))
}

// paragraphContent builds n unique paragraphs of about para bytes each,
// separated by blank lines. Unique markers keep every chunk's position in the
// content unambiguous, so the coverage check cannot match the wrong repeat.
func paragraphContent(n, para int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "paragraph %05d: ", i)
		b.WriteString(strings.Repeat("word ", (para-17)/5))
	}
	return b.String()
}

// rejoinChunks rebuilds the input from the chunks by stripping each chunk's
// overlap with the tail of the result so far, and fails when a chunk does not
// overlap that tail by about relevantChunkOverlapBytes. It returns the
// rebuilt content, which must equal the input byte for byte: that is the
// every-byte-covered check.
func rejoinChunks(t *testing.T, chunks []string) string {
	t.Helper()
	if len(chunks) == 0 {
		return ""
	}
	rebuilt := chunks[0]
	for i, c := range chunks[1:] {
		maxOv := min(relevantChunkOverlapBytes, len(c))
		ov := -1
		for k := maxOv; k >= 0; k-- {
			if strings.HasSuffix(rebuilt, c[:k]) {
				ov = k
				break
			}
		}
		// The overlap is relevantChunkOverlapBytes, backed off at most
		// utf8.UTFMax-1 bytes to land on a character boundary.
		if ov < maxOv-(utf8.UTFMax-1) {
			t.Fatalf("chunk %d overlaps the previous tail by %d bytes, want about %d", i+1, ov, maxOv)
		}
		rebuilt += c[ov:]
	}
	return rebuilt
}

// Defends the chunk budget against drift from the real prompt: the header
// helper must be byte-for-byte the prefix extractRelevantContent builds, for
// both the plain context and the directory File: context. If the prompt
// format changes without the helper, chunks sized by the helper would overrun
// the cap and be cut again.
func TestRelevantPromptHeaderMatchesTheRealPrompt(t *testing.T) {
	for _, contextText := range chunkTestContexts {
		srv, prompts := promptRecorder(t, "a passage")
		pointExtractRelevantAt(t, srv)
		resetModelUse()
		t.Cleanup(resetModelUse)

		const content = "sentinel content bytes"
		client := newLLMClient(llmapi.GetAPIConfig())
		if _, err := extractRelevantContent(context.Background(), client, content, contextText, 30); err != nil {
			t.Fatalf("extractRelevantContent: %v", err)
		}
		sent := prompts()
		if len(sent) != 1 {
			t.Fatalf("%d prompts sent, want 1", len(sent))
		}
		want := relevantPromptHeader(contextText) + content
		if sent[0] != want {
			t.Errorf("header helper does not match the real prompt.\nreal: %q\nwant: %q", sent[0], want)
		}
	}
}

// Defends the cap: no chunk plus the header extractRelevantContent builds may
// exceed maxRelevantPromptBytes, so no chunked prompt is ever cut. Checked
// both by byte count and by sending every chunk through the real prompt
// builder against a fake model: the prompt must arrive whole, with no
// truncation note and no cut counted.
func TestChunkContentNoPromptOverTheCap(t *testing.T) {
	for _, contextText := range chunkTestContexts {
		content := paragraphContent(120, 1000) // ~120 KB, several chunks
		header := relevantPromptHeader(contextText)
		chunks, err := chunkContent(content, len(header))
		if err != nil {
			t.Fatalf("chunkContent: %v", err)
		}
		if len(chunks) < 2 {
			t.Fatalf("%d chunks for %d bytes, want several", len(chunks), len(content))
		}

		srv, prompts := promptRecorder(t, "a passage")
		pointExtractRelevantAt(t, srv)
		resetModelUse()
		t.Cleanup(resetModelUse)
		client := newLLMClient(llmapi.GetAPIConfig())

		for i, c := range chunks {
			if len(header)+len(c) > maxRelevantPromptBytes {
				t.Errorf("chunk %d: header %d + chunk %d = %d bytes, over the %d cap", i, len(header), len(c), len(header)+len(c), maxRelevantPromptBytes)
			}
			if _, err := extractRelevantContent(context.Background(), client, c, contextText, 30); err != nil {
				t.Fatalf("chunk %d: extractRelevantContent: %v", i, err)
			}
		}
		if cut := llmPromptsCut.Load(); cut != 0 {
			t.Errorf("%d chunked prompts were cut, want 0", cut)
		}
		sent := prompts()
		if len(sent) != len(chunks) {
			t.Fatalf("%d prompts sent, want %d", len(sent), len(chunks))
		}
		for i, p := range sent {
			if p != header+chunks[i] {
				t.Errorf("chunk %d: the prompt sent is not header+chunk; it was cut or rewritten", i)
			}
		}
	}
}

// Defends every byte being covered, in document order, with only the small
// overlap repeated: the chunks rejoined must equal the input exactly.
func TestChunkContentCoversEveryByte(t *testing.T) {
	for _, contextText := range chunkTestContexts {
		for _, content := range []string{
			paragraphContent(120, 1000),
			strings.Repeat("x", 3*maxRelevantPromptBytes),
			strings.Repeat("line of text\n", 5000),
			strings.Repeat("€", 40000),
			paragraphContent(3, 900) + "\n\n" + strings.Repeat("😀 tail ", 5000),
		} {
			chunks, err := chunkContent(content, len(relevantPromptHeader(contextText)))
			if err != nil {
				t.Fatalf("chunkContent: %v", err)
			}
			if got := rejoinChunks(t, chunks); got != content {
				t.Errorf("rejoined chunks are %d bytes, want the original %d; every byte must be covered in order", len(got), len(content))
			}
		}
	}
}

// Defends against a chunk boundary landing inside a multi-byte character.
// Every padding is tried so for both a 3- and a 4-byte character some chunk
// edge falls mid-character if the cut is by byte offset.
func TestChunkContentNeverSplitsACharacter(t *testing.T) {
	for _, ch := range []string{"€", "😀"} {
		for pad := 0; pad < len(ch); pad++ {
			content := strings.Repeat("x", pad) + strings.Repeat(ch, maxRelevantPromptBytes/len(ch)+10)
			chunks, err := chunkContent(content, len(relevantPromptHeader("c")))
			if err != nil {
				t.Fatalf("%q pad %d: chunkContent: %v", ch, pad, err)
			}
			for i, c := range chunks {
				if !utf8.ValidString(c) || strings.ContainsRune(c, utf8.RuneError) {
					t.Errorf("%q pad %d: chunk %d splits a character", ch, pad, i)
				}
			}
			if got := rejoinChunks(t, chunks); got != content {
				t.Errorf("%q pad %d: rejoined chunks differ from the input", ch, pad)
			}
		}
	}
}

// Defends determinism: the same input must give the same chunks, call after
// call, so a rerun sends the same prompts and costs the same.
func TestChunkContentIsDeterministic(t *testing.T) {
	content := paragraphContent(120, 1000) + "\n\n" + strings.Repeat("€😀", 5000)
	headerBytes := len(relevantPromptHeader(chunkTestContexts[1]))
	first, err := chunkContent(content, headerBytes)
	if err != nil {
		t.Fatalf("chunkContent: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := chunkContent(content, headerBytes)
		if err != nil {
			t.Fatalf("chunkContent: %v", err)
		}
		if len(again) != len(first) {
			t.Fatalf("run %d gave %d chunks, first gave %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("run %d chunk %d differs from the first run", i, j)
			}
		}
	}
}

// Defends the boundary preference: paragraphs first. With paragraphs well
// under the chunk budget, every chunk but the last must end on a paragraph
// boundary, and chunks must stay well filled (no tiny chunks).
func TestChunkContentPrefersParagraphBoundaries(t *testing.T) {
	content := paragraphContent(120, 1000)
	size := chunkSizeFor(t, "c")
	chunks, err := chunkContent(content, len(relevantPromptHeader("c")))
	if err != nil {
		t.Fatalf("chunkContent: %v", err)
	}
	for i, c := range chunks[:len(chunks)-1] {
		if !strings.HasSuffix(c, "\n\n") {
			t.Errorf("chunk %d does not end on a paragraph boundary; tail %q", i, c[max(0, len(c)-20):])
		}
		if len(c) < size/2 {
			t.Errorf("chunk %d is only %d bytes, want at least half the %d budget", i, len(c), size)
		}
	}
}

// Defends the second preference: with no blank lines, chunks end on line
// boundaries.
func TestChunkContentPrefersLineBoundaries(t *testing.T) {
	content := strings.Repeat("a line of text\n", 4000)
	chunks, err := chunkContent(content, len(relevantPromptHeader("c")))
	if err != nil {
		t.Fatalf("chunkContent: %v", err)
	}
	for i, c := range chunks[:len(chunks)-1] {
		if !strings.HasSuffix(c, "\n") || strings.HasSuffix(c, "\n\n") {
			t.Errorf("chunk %d does not end on a line boundary; tail %q", i, c[max(0, len(c)-20):])
		}
	}
}

// Defends the last resort: with no newlines at all, chunks fill the budget to
// the byte (ASCII content has a boundary at every byte).
func TestChunkContentFallsBackToRuneBoundaries(t *testing.T) {
	size := chunkSizeFor(t, "c")
	content := strings.Repeat("x", 3*size+123)
	chunks, err := chunkContent(content, len(relevantPromptHeader("c")))
	if err != nil {
		t.Fatalf("chunkContent: %v", err)
	}
	if len(chunks) != 4 {
		t.Fatalf("%d chunks, want 4", len(chunks))
	}
	for i, c := range chunks[:3] {
		if len(c) != size {
			t.Errorf("chunk %d is %d bytes, want the full %d budget", i, len(c), size)
		}
	}
}

// Defends the edges: content that fits is one untouched chunk, empty content
// gives no chunks and no error, and a header that leaves no room (or a budget
// too small for the next character) is an error, never a silent empty result
// or a split character.
func TestChunkContentEdges(t *testing.T) {
	size := chunkSizeFor(t, "c")
	header := len(relevantPromptHeader("c"))

	if chunks, err := chunkContent("short input", header); err != nil || len(chunks) != 1 || chunks[0] != "short input" {
		t.Errorf("small content: got %q, %v; want one untouched chunk", chunks, err)
	}
	if chunks, err := chunkContent(strings.Repeat("x", size), header); err != nil || len(chunks) != 1 {
		t.Errorf("exact-budget content: got %d chunks, %v; want one", len(chunks), err)
	}
	if chunks, err := chunkContent("", header); err != nil || len(chunks) != 0 {
		t.Errorf("empty content: got %q, %v; want no chunks and no error", chunks, err)
	}
	if _, err := chunkContent("content", maxRelevantPromptBytes); err == nil {
		t.Error("a header at the cap left no room, but chunkContent did not fail")
	}
	if _, err := chunkContent("content", maxRelevantPromptBytes+1); err == nil {
		t.Error("a header over the cap did not fail")
	}
	if _, err := chunkContent("content", -1); err == nil {
		t.Error("a negative header size did not fail")
	}
	// A budget of 2 bytes cannot hold a 3-byte character: an error, not a
	// split character and not an endless loop of empty chunks.
	if _, err := chunkContent("€€€", maxRelevantPromptBytes-2); err == nil {
		t.Error("a budget smaller than one character did not fail")
	}
}

// Defends progress at tiny budgets: with a 4-byte budget the overlap is 1
// byte and a rune cut can keep only 1 byte, so subtracting the overlap would
// leave the next chunk starting where this one did -- an endless loop. Every
// chunk must advance and every byte must still be covered.
func TestChunkContentAdvancesAtTinyBudgets(t *testing.T) {
	content := "a" + strings.Repeat("😀", 30)
	chunks, err := chunkContent(content, maxRelevantPromptBytes-4)
	if err != nil {
		t.Fatalf("chunkContent: %v", err)
	}
	covered := 0
	for i, c := range chunks {
		if len(c) == 0 || len(c) > 4 {
			t.Fatalf("chunk %d is %d bytes, want 1..4", i, len(c))
		}
		// The chunk starts at the covered edge, or at most 1 byte (the
		// whole overlap at this budget) before it.
		back := 0
		if !strings.HasPrefix(content[covered:], c) {
			back = 1
		}
		start := covered - back
		if start < 0 || start+len(c) > len(content) || content[start:start+len(c)] != c {
			t.Fatalf("chunk %d %q is not at the covered edge", i, c)
		}
		if start+len(c) <= covered {
			t.Fatalf("chunk %d does not advance past byte %d", i, covered)
		}
		covered = start + len(c)
	}
	if covered != len(content) {
		t.Errorf("tiny-budget chunks cover %d of %d bytes", covered, len(content))
	}
}

// Defends the overlap itself: consecutive chunks must repeat a small tail of
// the previous chunk so a fact straddling a boundary is still in one piece in
// some chunk.
func TestChunkContentHasSmallOverlap(t *testing.T) {
	content := paragraphContent(120, 1000)
	chunks, err := chunkContent(content, len(relevantPromptHeader("c")))
	if err != nil {
		t.Fatalf("chunkContent: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("%d chunks, want several", len(chunks))
	}
	// rejoinChunks fails unless every chunk overlaps the previous tail by
	// about relevantChunkOverlapBytes; a zero-overlap chunker fails there.
	if got := rejoinChunks(t, chunks); got != content {
		t.Errorf("rejoined chunks differ from the input")
	}
}