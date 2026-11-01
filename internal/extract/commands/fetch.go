package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/samestrin/llm-extract-axi/internal/support/fetch"
)

// formatRaw is the only output format ported so far: the body as received.
// llm-tools also offers text and markdown for HTML; those are rejected
// rather than ignored, so a caller never gets raw HTML when it asked for text.
const formatRaw = "raw"

var (
	fetchOutput    string
	fetchFormat    string
	fetchTimeout   int
	fetchRetries   int
	fetchUserAgent string
	fetchJSON      bool
)

// FetchResult is the serializable outcome of a successful fetch. A failed
// fetch is an error and has no result.
type FetchResult struct {
	URL      string `json:"url"`
	FinalURL string `json:"final_url,omitempty"`
	Status   int    `json:"status"`
	Tier     string `json:"tier"`
	Format   string `json:"format"`
	Bytes    int    `json:"bytes"`
	Output   string `json:"output,omitempty"`
	Body     string `json:"body,omitempty"`
}

func newFetchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fetch <url>",
		Short: "Download a URL with a direct HTTP GET",
		Long: `Download a URL with a direct HTTP GET and write the body to stdout.

A failed fetch exits non-zero and says why. So does a page that needs a
browser (a Cloudflare or JavaScript challenge page): this command cannot pass
one, and says so instead of printing the challenge page.

Configuration is read from environment variables (flags override them):

  FETCH_TIMEOUT       Per-attempt timeout in seconds (default 30)
  FETCH_MAX_RETRIES   Retries on connection errors and 5xx (default 2)
  FETCH_USER_AGENT    Override the request User-Agent

Use --output to save the body to a file instead of stdout.`,
		Args: cobra.ExactArgs(1),
		RunE: runFetch,
	}
	cmd.Flags().StringVar(&fetchOutput, "output", "", "Write the body to this file instead of stdout")
	cmd.Flags().StringVar(&fetchFormat, "format", formatRaw, "Output format: raw (the only one supported)")
	cmd.Flags().IntVar(&fetchTimeout, "timeout", fetch.DefaultTimeout, "Per-attempt timeout in seconds")
	cmd.Flags().IntVar(&fetchRetries, "retries", fetch.DefaultMaxRetries, "Retries on connection errors and 5xx")
	cmd.Flags().StringVar(&fetchUserAgent, "user-agent", "", "Override the request User-Agent")
	cmd.Flags().BoolVar(&fetchJSON, "json", false, "Output as JSON")
	return cmd
}

func runFetch(cmd *cobra.Command, args []string) error {
	rawURL := args[0]
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return fmt.Errorf("invalid URL: must start with http:// or https://")
	}
	if fetchFormat != formatRaw {
		return fmt.Errorf("unsupported --format %q: only %q is available", fetchFormat, formatRaw)
	}

	cfg := fetch.LoadConfig()
	if cmd.Flags().Changed("timeout") {
		cfg.Timeout = fetchTimeout
	}
	if cmd.Flags().Changed("retries") {
		cfg.MaxRetries = fetchRetries
	}
	if fetchUserAgent != "" {
		cfg.UserAgent = fetchUserAgent
	}

	res := fetch.New(cfg).Fetch(rawURL)
	if res.Err != nil {
		return fmt.Errorf("fetch %s: %w", rawURL, res.Err)
	}

	out := FetchResult{
		URL:      res.URL,
		FinalURL: res.FinalURL,
		Status:   res.Status,
		Tier:     string(res.Tier),
		Format:   fetchFormat,
		Bytes:    res.Bytes,
	}
	if fetchOutput != "" {
		if err := os.WriteFile(fetchOutput, res.Body, 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		out.Output = fetchOutput
	}

	w := cmd.OutOrStdout()
	if fetchJSON {
		if fetchOutput == "" {
			out.Body = string(res.Body)
		}
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(out)
	}
	if fetchOutput != "" {
		_, err := fmt.Fprintf(w, "Saved %d bytes [HTTP %d via %s, %s] -> %s\n", out.Bytes, res.Status, res.Tier, fetchFormat, fetchOutput)
		return err
	}
	_, err := w.Write(res.Body)
	return err
}

func init() {
	RootCmd.AddCommand(newFetchCmd())
}
