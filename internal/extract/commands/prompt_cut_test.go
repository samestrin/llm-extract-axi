package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// promptRecorder stands in for the model endpoint and keeps the user message
// of every call, decoded the way the endpoint reads it.
func promptRecorder(t *testing.T, reply string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		for _, m := range req.Messages {
			if m.Role == "user" {
				prompts = append(prompts, m.Content)
			}
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}}}})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), prompts...)
	}
}

// checkCutPrompt fails when a cut prompt is not whole characters, overruns
// the cap plus its note, or lost more than one character's worth of bytes.
// json.Marshal turns a split character into U+FFFD on the wire, so the
// endpoint sees a replacement character rather than invalid bytes; both count.
func checkCutPrompt(t *testing.T, prompt string, limit int, note string) {
	t.Helper()
	if !utf8.ValidString(prompt) || strings.ContainsRune(prompt, utf8.RuneError) {
		t.Errorf("the prompt sent to the model splits a character; its tail is %q", prompt[max(0, len(prompt)-len(note)-8):])
	}
	body, ok := strings.CutSuffix(prompt, note)
	if !ok {
		t.Fatalf("the cut prompt does not end with the truncation note %q", note)
	}
	if len(body) > limit || len(body) < limit-utf8.UTFMax {
		t.Errorf("the cut prompt is %d bytes before its note, want %d or up to %d fewer", len(body), limit, utf8.UTFMax-1)
	}
}

// Defends against extract-relevant cutting its prompt inside a multi-byte
// character: the byte-offset cut sent the model a broken character. Every
// padding is tried, so for both a 3- and a 4-byte character some run puts
// the 30000th byte mid-character. Since epic 12 the input is chunked rather
// than cut, so this checks the CLI end of that: every prompt sent is whole
// characters and within the cap, none carries the cut note, nothing reports
// truncated, and every chunk was sent, then
// one reduce call.
func TestExtractRelevantCutsPromptOnARuneBoundary(t *testing.T) {
	for _, ch := range []string{"€", "😀"} {
		for pad := 0; pad < len(ch); pad++ {
			srv, prompts := promptRecorder(t, "a passage")
			pointExtractRelevantAt(t, srv)
			src := filepath.Join(t.TempDir(), "doc.md")
			content := strings.Repeat("x", pad) + strings.Repeat(ch, maxRelevantPromptBytes/len(ch)+10)
			if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}

			out, err := runExtractRelevantCmd(t, "--path", src, "--context", "c", "--json")
			if err != nil {
				t.Fatalf("%q pad %d: extract-relevant: %v", ch, pad, err)
			}
			var got ExtractRelevantResult
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("output is not JSON: %v\n%s", err, out)
			}
			if got.Truncated != "" {
				t.Errorf("%q pad %d: a chunked input reports truncated: %q", ch, pad, got.Truncated)
			}
			chunks, err := chunkContent(content, len(relevantPromptHeader("c")))
			if err != nil {
				t.Fatal(err)
			}
			// One call per chunk, and one reduce merging their parts; the
			// reduce prompt is held to the same checks.
			sent := prompts()
			if len(sent) != len(chunks)+1 {
				t.Fatalf("%q pad %d: %d model calls, want one per chunk (%d) and the reduce", ch, pad, len(sent), len(chunks))
			}
			for i, p := range sent {
				if !utf8.ValidString(p) || strings.ContainsRune(p, utf8.RuneError) {
					t.Errorf("%q pad %d: prompt %d splits a character", ch, pad, i)
				}
				if len(p) > maxRelevantPromptBytes || strings.Contains(p, "[Content truncated due to length]") {
					t.Errorf("%q pad %d: prompt %d is %d bytes or was cut", ch, pad, i, len(p))
				}
			}
		}
	}
}

// Defends extract-links --context ranking against the same split: link text
// is page-controlled and may be any script, and its prompt is cut at
// maxLinksPromptBytes by byte offset too.
func TestRankLinksCutsPromptOnARuneBoundary(t *testing.T) {
	for _, ch := range []string{"€", "😀"} {
		for pad := 0; pad < len(ch); pad++ {
			srv, prompts := promptRecorder(t, `{"rankings":[{"index":0,"score":50}]}`)
			pointExtractRelevantAt(t, srv)
			resetModelUse()
			links := []LinkInfo{{Href: "https://example.com/" + strings.Repeat("a", pad), Text: strings.Repeat(ch, maxLinksPromptBytes/len(ch)+10)}}

			if _, err := rankLinksWithLLM(context.Background(), links, "c", 30); err != nil {
				t.Fatalf("%q pad %d: rankLinksWithLLM: %v", ch, pad, err)
			}
			if llmPromptsCut.Load() != 1 {
				t.Errorf("%q pad %d: the cut is not counted, so truncated would not report it", ch, pad)
			}
			sent := prompts()
			if len(sent) != 1 {
				t.Fatalf("%q pad %d: %d model calls, want 1", ch, pad, len(sent))
			}
			checkCutPrompt(t, sent[0], maxLinksPromptBytes, "\n...(truncated)")
		}
	}
}

// Defends the cut itself at its edges: shorter input is untouched, a cut at
// a boundary keeps every byte up to the limit, and a cut inside a character
// drops the whole character.
func TestCutAtRuneBoundary(t *testing.T) {
	for _, tc := range []struct {
		in    string
		limit int
		want  string
	}{
		{"abc", 5, "abc"},
		{"abc", 3, "abc"},
		{"ab€", 3, "ab"},
		{"ab€", 4, "ab"},
		{"ab€", 5, "ab€"},
		{"a😀b", 4, "a"},
		{"a😀b", 5, "a😀"},
		{"€", 0, ""},
	} {
		if got := cutAtRuneBoundary(tc.in, tc.limit); got != tc.want {
			t.Errorf("cutAtRuneBoundary(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
	}
}
