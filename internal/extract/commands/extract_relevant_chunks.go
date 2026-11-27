package commands

import (
	"fmt"
	"strings"
	"unicode/utf8"
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
