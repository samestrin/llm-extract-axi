// Package mcpserver exposes the llm-extract CLI as MCP tools. It maps tool
// arguments to CLI arguments and returns what the CLI printed; every
// behaviour lives in the CLI.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// BinaryEnv overrides where the llm-extract binary is looked up.
const BinaryEnv = "LLM_EXTRACT_BIN"

const instructions = "llm-extract MCP fetches web pages and reduces files or pages to what an agent needs. It is a fallback for environments that cannot run the llm-extract CLI directly; the CLI is the primary interface. A failed fetch or model call is returned as an error, never as an empty result."

// FindBinary locates llm-extract: $LLM_EXTRACT_BIN, then next to this
// executable, then on PATH.
func FindBinary() (string, error) {
	if p := os.Getenv(BinaryEnv); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s=%s: %w", BinaryEnv, p, err)
		}
		return p, nil
	}
	name := "llm-extract"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(sibling); err == nil {
			return sibling, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", errors.New("llm-extract binary not found: install it next to llm-extract-mcp, put it on PATH, or set " + BinaryEnv)
}

type tool struct {
	name, description string
	schema            map[string]any
	// build maps tool arguments to CLI arguments, or reports what is missing.
	build func(a args) ([]string, error)
}

type args map[string]any

func (a args) str(k string) string {
	s, _ := a[k].(string)
	return s
}

// num returns the integer value of k and whether it was supplied.
func (a args) num(k string) (int, bool) {
	f, ok := a[k].(float64)
	return int(f), ok
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func schema(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func (a args) flagInt(out []string, k, flag string) []string {
	if n, ok := a.num(k); ok {
		out = append(out, flag, strconv.Itoa(n))
	}
	return out
}

func (a args) flagStr(out []string, k, flag string) []string {
	if s := a.str(k); s != "" {
		out = append(out, flag, s)
	}
	return out
}

func need(a args, keys ...string) error {
	var missing []string
	for _, k := range keys {
		if strings.TrimSpace(a.str(k)) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required argument(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

var tools = []tool{
	{
		name:        "fetch",
		description: "Fetch a URL and return the body. A challenge page or failed fetch is an error, not an empty result.",
		schema: schema([]string{"url"}, map[string]any{
			"url":        prop("string", "URL to fetch"),
			"timeout":    prop("integer", "Per-attempt timeout in seconds"),
			"retries":    prop("integer", "Retries on connection errors and 5xx"),
			"user_agent": prop("string", "Override the request User-Agent"),
		}),
		build: func(a args) ([]string, error) {
			if err := need(a, "url"); err != nil {
				return nil, err
			}
			out := []string{"fetch", a.str("url")}
			out = a.flagInt(out, "timeout", "--timeout")
			out = a.flagInt(out, "retries", "--retries")
			return a.flagStr(out, "user_agent", "--user-agent"), nil
		},
	},
	{
		name:        "extract_links",
		description: "Extract links from a URL; with context, rank them by relevance using a model.",
		schema: schema([]string{"url"}, map[string]any{
			"url":     prop("string", "URL to extract links from"),
			"context": prop("string", "What the links are wanted for (enables model ranking)"),
			"timeout": prop("integer", "HTTP timeout in seconds"),
		}),
		build: func(a args) ([]string, error) {
			if err := need(a, "url"); err != nil {
				return nil, err
			}
			out := []string{"extract-links", "--url", a.str("url")}
			out = a.flagStr(out, "context", "--context")
			return a.flagInt(out, "timeout", "--timeout"), nil
		},
	},
	{
		name:        "extract_relevant",
		description: "Reduce a file or directory to the content relevant to a context, using a model.",
		schema: schema([]string{"path", "context"}, map[string]any{
			"path":        prop("string", "File or directory path to process"),
			"context":     prop("string", "What content to extract"),
			"concurrency": prop("integer", "Concurrent API calls for directory processing"),
			"timeout":     prop("integer", "API call timeout in seconds"),
		}),
		build: func(a args) ([]string, error) {
			if err := need(a, "path", "context"); err != nil {
				return nil, err
			}
			out := []string{"extract-relevant", "--path", a.str("path"), "--context", a.str("context")}
			out = a.flagInt(out, "concurrency", "--concurrency")
			return a.flagInt(out, "timeout", "--timeout"), nil
		},
	},
}

// ToolNames lists the registered tools.
func ToolNames() []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.name
	}
	return names
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// Run executes the CLI at bin for the named tool. A non-zero exit is an error
// result carrying stderr, so a failure is never returned as content.
func Run(ctx context.Context, bin, name string, raw json.RawMessage) *mcp.CallToolResult {
	var t *tool
	for i := range tools {
		if tools[i].name == name {
			t = &tools[i]
		}
	}
	if t == nil {
		return errResult("unknown tool: " + name)
	}
	var a args
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return errResult("Error parsing arguments: " + err.Error())
		}
	}
	cliArgs, err := t.build(a)
	if err != nil {
		return errResult("Error: " + err.Error())
	}
	cmd := exec.CommandContext(ctx, bin, cliArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errResult(fmt.Sprintf("llm-extract %s failed: %s", cliArgs[0], msg))
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: stdout.String()}}}
}

// New builds the server with every tool registered against the CLI at bin.
func New(bin, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "llm-extract-mcp", Version: version},
		&mcp.ServerOptions{Instructions: instructions})
	for _, t := range tools {
		name := t.name
		s.AddTool(&mcp.Tool{Name: name, Description: t.description, InputSchema: t.schema},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return Run(ctx, bin, name, req.Params.Arguments), nil
			})
	}
	return s
}
