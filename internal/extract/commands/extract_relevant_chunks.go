package commands

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/samestrin/llm-extract-axi/v2/pkg/llmapi"
)

// relevantChunkOverlapBytes is how much of the previous chunk's tail each
// chunk repeats, so a fact straddling a boundary is still whole in one chunk.
// The overlap is backed off to a character boundary, so it repeats up to this
// many bytes, never a split character.
const relevantChunkOverlapBytes = 200

// relevantPromptHeader is the exact prefix extractRelevantContent builds
// before the content: "Context: <contextText>\n\n---\n\nContent to extract
// from:\n\n". A directory run's contextText already carries the "File: ..."
// line. TestRelevantPromptHeaderMatchesTheRealPrompt pins this against the
// real prompt, so a change to the prompt format cannot silently leave chunks
// sized for the wrong header.
func relevantPromptHeader(contextText string) string {
	return fmt.Sprintf("Context: %s\n\n---\n\nContent to extract from:\n\n", contextText)
}

// relevantReduceHeader is the prefix of the reduce prompt, before the parts.
func relevantReduceHeader(contextText string) string {
	return fmt.Sprintf("Context: %s\n\n---\n\nExtracted parts to merge, in document order:\n\n", contextText)
}

// relevantReduceSeparator divides the parts in the reduce prompt.
const relevantReduceSeparator = "\n\n---\n\n"

// chunkContent splits content into deterministic chunks of at most
// maxRelevantPromptBytes-headerBytes bytes each, so every chunk plus the
// header extractRelevantContent builds fits the whole-prompt cap and no
// prompt is cut. Boundaries are preferred paragraph, then line, then rune;
// a boundary in the first half of the budget is skipped so chunks stay well
// filled and every step advances. Consecutive chunks overlap by about
// relevantChunkOverlapBytes. Empty content gives no chunks. A header that
// leaves no room, or a budget too small for the next character, is an error:
// a chunk that cannot be sent whole is never returned as if it could.
func chunkContent(content string, headerBytes int) ([]string, error) {
	if headerBytes < 0 || headerBytes >= maxRelevantPromptBytes {
		return nil, fmt.Errorf("prompt header of %d bytes leaves no room for content under the %d-byte prompt cap", headerBytes, maxRelevantPromptBytes)
	}
	if content == "" {
		return nil, nil
	}
	size := maxRelevantPromptBytes - headerBytes
	overlap := min(relevantChunkOverlapBytes, size/4)
	minFill := size / 2

	var chunks []string
	for pos := 0; pos < len(content); {
		if len(content)-pos <= size {
			chunks = append(chunks, content[pos:])
			break
		}
		window := content[pos : pos+size]
		var end int
		if i := strings.LastIndex(window, "\n\n"); i >= minFill {
			end = i + 2
		} else if i := strings.LastIndex(window, "\n"); i >= minFill {
			end = i + 1
		} else {
			// One byte past the budget, so cutAtRuneBoundary inspects the
			// byte at the cut instead of returning the window untouched.
			end = len(cutAtRuneBoundary(content[pos:pos+size+1], size))
			if end == 0 {
				return nil, fmt.Errorf("chunk budget of %d bytes cannot hold the next character; the prompt header leaves too little room under the %d-byte prompt cap", size, maxRelevantPromptBytes)
			}
		}
		chunks = append(chunks, content[pos:pos+end])
		// The next chunk repeats the tail of this one, starting on a
		// character boundary. At a tiny budget a rune cut can be shorter
		// than the overlap; dropping the overlap for that step keeps every
		// chunk advancing instead of looping on the same byte.
		next := pos + end - overlap
		if next <= pos {
			next = pos + end
		}
		for next < len(content) && !utf8.RuneStart(content[next]) {
			next++
		}
		pos = next
	}
	return chunks, nil
}

// relevantCallSlots bounds the model calls in flight in one run to
// --concurrency. A directory run's files share it, so their chunks together
// never exceed the bound. runExtractRelevant sets it before any call.
var relevantCallSlots chan struct{}

// chunkedRead is what the map pass made of one input.
type chunkedRead struct {
	// parts are the answers that were not NONE, in document order.
	parts []string
	// read counts the chunks sent and total the chunks the input makes; when
	// atLeast is set the input was read only to its bound, so total counts
	// the chunks of what was read and the input makes at least that many.
	read, total int
	atLeast     bool
	// err names every failed chunk; the parts of the chunks that answered
	// are kept beside it.
	err error
}

// truncation is the note for an input whose chunks past --max-chunks were
// not read, or "" when every chunk was.
func (r chunkedRead) truncation() string {
	if r.read >= r.total {
		return ""
	}
	of := ""
	if r.atLeast {
		of = "at least "
	}
	return fmt.Sprintf("read %d of %s%d chunks", r.read, of, r.total)
}

// mapChunks is the map pass over one input: content is cut into chunks under
// the header contextText builds, the first maxChunks are each extracted with
// the same prompt, at most --concurrency at a time across the run, and the
// answers are kept in document order. A failed call is never read as NONE:
// it is named in err, and the parts of the chunks that answered are kept.
// atLeast says content stops at the read bound, not at the input's end.
func mapChunks(ctx context.Context, client *llmapi.LLMClient, content, contextText string, maxChunks int, atLeast bool) chunkedRead {
	chunks, err := chunkContent(content, len(relevantPromptHeader(contextText)))
	if err != nil {
		return chunkedRead{err: err}
	}
	r := chunkedRead{read: min(len(chunks), maxChunks), total: len(chunks), atLeast: atLeast}
	answers := make([]string, r.read)
	errs := make([]error, r.read)
	var wg sync.WaitGroup
	for i := range r.read {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case relevantCallSlots <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-relevantCallSlots }()
			answers[i], errs[i] = extractRelevantContent(ctx, client, chunks[i], contextText, extractRelevantTimeout)
		}()
	}
	wg.Wait()

	var failed []string
	var first error
	for i, answer := range answers {
		if errs[i] != nil {
			failed = append(failed, strconv.Itoa(i+1))
			if first == nil {
				first = errs[i]
			}
			continue
		}
		if !isNone(answer) {
			r.parts = append(r.parts, answer)
		}
	}
	switch {
	case len(failed) == 0:
	case r.read == 1:
		// One chunk is the whole input: its error needs no chunk number.
		r.err = first
	case len(failed) == 1:
		r.err = fmt.Errorf("chunk %s of %d failed: %w", failed[0], r.read, first)
	default:
		r.err = fmt.Errorf("chunks %s of %d failed (first: %w)", strings.Join(failed, ", "), r.read, first)
	}
	return r
}

// chunkReadLimit is how many bytes of a local file the map pass reads:
// --max-chunks chunks of maxRelevantPromptBytes. readCapped adds one byte, so
// a file past the limit is seen to be. Every chunk is shorter than
// maxRelevantPromptBytes, so a file read to the limit makes more chunks than
// --max-chunks, and the extra byte, which may end mid-character, is never
// sent. Saturates instead of overflowing on a huge --max-chunks.
func chunkReadLimit(maxChunks int) int64 {
	if int64(maxChunks) >= (math.MaxInt64-1)/maxRelevantPromptBytes {
		return math.MaxInt64 - 1
	}
	return int64(maxChunks) * maxRelevantPromptBytes
}

// readChunked reads a local file to chunkReadLimit and runs the map pass on it.
func readChunked(ctx context.Context, client *llmapi.LLMClient, path, contextText string) (chunkedRead, error) {
	limit := chunkReadLimit(extractRelevantMaxChunks)
	content, err := readCapped(path, limit)
	if err != nil {
		return chunkedRead{}, err
	}
	return mapChunks(ctx, client, string(content), contextText, extractRelevantMaxChunks, int64(len(content)) > limit), nil
}
