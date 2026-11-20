package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	cmd := exec.Command(binary(t, "llm-extract-mcp"))
	cmd.Env = append(os.Environ(), "LLM_EXTRACT_BIN="+binary(t, "llm-extract"), "FETCH_MAX_RETRIES=0")
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "1"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func resultText(r *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// Defends that the built server lists its tools over real stdio.
func TestMCPListsTools(t *testing.T) {
	res, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = true
	}
	for _, n := range []string{"fetch", "extract_links", "extract_relevant"} {
		if !got[n] {
			t.Errorf("tool %s not listed: %v", n, got)
		}
	}
}

// Defends the payload: fetch through MCP returns the CLI's TOON envelope with
// the page body in body, the whole-body size in bytes and truncated false for
// a page under the cut. A server that injects a flag or rewrites the output
// changes these strings.
func TestMCPFetchReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello from the fake page"))
	}))
	defer srv.Close()
	r, err := connect(t).CallTool(context.Background(), &mcp.CallToolParams{
		Name: "fetch", Arguments: map[string]any{"url": srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	got := resultText(r)
	for _, want := range []string{"body: hello from the fake page\n", "bytes: 24\n", "truncated: false\n"} {
		if r.IsError || !strings.Contains(got, want) {
			t.Errorf("want %q in the envelope: err=%v %q", want, r.IsError, got)
		}
	}
}

// Defends epic 10's head/full pass-through end to end: a page over the
// 4096-byte default cut comes back truncated with help[] naming --full, full
// returns the whole body, head sets the cut, and head with full surfaces the
// CLI's usage error as an MCP error. Without the pass-through an MCP caller
// would be stuck with the cut and no way to read the rest.
func TestMCPFetchHeadAndFull(t *testing.T) {
	body := strings.Repeat("A", 6000) + "TAIL-MARKER"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	call := func(t *testing.T, extra map[string]any) *mcp.CallToolResult {
		t.Helper()
		args := map[string]any{"url": srv.URL}
		for k, v := range extra {
			args[k] = v
		}
		r, err := connect(t).CallTool(context.Background(), &mcp.CallToolParams{Name: "fetch", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	t.Run("default cuts at 4096", func(t *testing.T) {
		r := call(t, nil)
		got := resultText(r)
		if r.IsError {
			t.Fatalf("default fetch was an error: %s", got)
		}
		for _, want := range []string{"body: " + strings.Repeat("A", 4096) + "\n", "bytes: 6011\n", "truncated: true\n", "--full"} {
			if !strings.Contains(got, want) {
				t.Errorf("want %q in the envelope, got:\n%s", want, got)
			}
		}
		if strings.Contains(got, "TAIL-MARKER") {
			t.Error("the default cut leaked the tail of the body")
		}
	})

	t.Run("full returns the whole body", func(t *testing.T) {
		r := call(t, map[string]any{"full": true})
		got := resultText(r)
		if r.IsError {
			t.Fatalf("full fetch was an error: %s", got)
		}
		for _, want := range []string{"body: " + body + "\n", "truncated: false\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("want %q in the envelope, got:\n%s", want, got)
			}
		}
	})

	t.Run("head sets the cut", func(t *testing.T) {
		r := call(t, map[string]any{"head": 10})
		got := resultText(r)
		if r.IsError {
			t.Fatalf("head fetch was an error: %s", got)
		}
		for _, want := range []string{"body: AAAAAAAAAA\n", "truncated: true\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("want %q in the envelope, got:\n%s", want, got)
			}
		}
	})

	t.Run("head with full is the CLI's usage error", func(t *testing.T) {
		r := call(t, map[string]any{"head": 10, "full": true})
		if !r.IsError || !strings.Contains(resultText(r), "--head and --full cannot be used together") {
			t.Errorf("want the CLI usage error, got err=%v %q", r.IsError, resultText(r))
		}
	})
}

// Defends the AGENTS.md rule: a failed fetch is an error result through MCP,
// never an empty success.
func TestMCPFetchFailureIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<title>Just a moment...</title>"))
	}))
	defer srv.Close()
	r, err := connect(t).CallTool(context.Background(), &mcp.CallToolParams{
		Name: "fetch", Arguments: map[string]any{"url": srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError || !strings.Contains(resultText(r), "needs a browser") {
		t.Errorf("want error mentioning browser, got isError=%v %q", r.IsError, resultText(r))
	}
}
