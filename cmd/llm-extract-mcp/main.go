// Command llm-extract-mcp is a fallback MCP server for environments that
// cannot run the llm-extract CLI directly. It holds no logic of its own: each
// tool call is one invocation of the CLI.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/samestrin/llm-extract-axi/internal/extract/mcpserver"
)

// serverVersion is stamped by the Makefile with -X main.serverVersion.
var serverVersion = "dev"

func main() {
	bin, err := mcpserver.FindBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	server := mcpserver.New(bin, serverVersion)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
