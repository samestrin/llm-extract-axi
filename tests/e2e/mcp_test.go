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

// Defends the payload: fetch through MCP returns the page body the CLI fetched.
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
	if r.IsError || !strings.Contains(resultText(r), "hello from the fake page") {
		t.Errorf("unexpected result: err=%v %q", r.IsError, resultText(r))
	}
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
