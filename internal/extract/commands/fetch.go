package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/samestrin/llm-extract-axi/v2/internal/support/fetch"
	"github.com/samestrin/llm-extract-axi/v2/pkg/output"
)

// formatRaw is the only output format ported so far: the body as received.
// llm-tools also offers text and markdown for HTML; those are rejected
// rather than ignored, so a caller never gets raw HTML when it asked for text.
const formatRaw = "raw"

// defaultHead is how many body bytes fetch prints unless --head or --full
// says otherwise: enough to see what a page is without filling a context.
const defaultHead = 4096

var (
	fetchOutput    string
	fetchFormat    string
	fetchTimeout   int
	fetchRetries   int
	fetchUserAgent string
	fetchMaxBytes  int
	fetchJSON      bool
	fetchHead      int
	fetchFull      bool
)

// FetchResult is the envelope of a successful fetch. A failed fetch is an
// error and has no result.
//
// Bytes is the size of the whole body, and Truncated says Body holds less
// than that: a cut at --head, or no text at all for a body that is not text.
// Body is a pointer so --output can leave it out while an empty body is still
// printed as empty. Help is set under TOON only, as on every other command.
type FetchResult struct {
	URL         string   `json:"url"`
	FinalURL    string   `json:"final_url,omitempty"`
	Status      int      `json:"status"`
	Tier        string   `json:"tier"`
	Format      string   `json:"format"`
	ContentType string   `json:"content_type"`
	Bytes       int      `json:"bytes"`
	Truncated   bool     `json:"truncated"`
	Output      string   `json:"output,omitempty"`
	Body        *string  `json:"body,omitempty"`
	Help        []string `json:"help,omitempty"`
}

// The help[] lines name flags only, never the URL or anything else the page
// controls (#55).
const (
	fetchHelpFull   = "Run again with --full for the whole body"
	fetchHelpOutput = "Run again with --output <file> to save the whole body to a file"
	fetchHelpBinary = "The body is not UTF-8 text; run again with --output <file> to save its bytes"
)

func newFetchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fetch <url>",
		Short: "Download a URL with a direct HTTP GET",
		Long: `Download a URL with a direct HTTP GET and print what came back.

The default output is a TOON document: the URL, status, content type, the
size of the whole body in bytes, and the first 4096 bytes of the body, cut on
a character boundary. truncated says whether the body was cut, and help[]
says how to get the rest. A body that is not UTF-8 text (an image, a PDF) is
never printed: body is empty and truncated is true; save it with --output.

  --head N   print the first N bytes of the body instead of 4096
  --full     print the whole body
  --plain    print the body bytes alone, still cut at --head unless --full;
             a one-line size hint goes to stderr when the body was cut
  --json     the same document as JSON
  --output   save the whole body to a file; the document leaves body out

A failed fetch exits non-zero and says why. So does a page that needs a
browser (a Cloudflare or JavaScript challenge page): this command cannot pass
one, and says so instead of printing the challenge page.

Configuration is read from environment variables (flags override them):

  FETCH_TIMEOUT       Per-attempt timeout in seconds (default 30)
  FETCH_MAX_RETRIES   Retries on connection errors and 5xx (default 2)
  FETCH_USER_AGENT    Override the request User-Agent
  FETCH_MAX_BYTES     Maximum response body size in bytes (default 16777216)

A response body larger than the cap is an error, never a truncated page.`,
		// Marked as a usage error: cobra's own wording carries no usage
		// marker, so a missing or extra URL exited 1, as if a retry could help.
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return output.UsageError(err)
			}
			return nil
		},
		RunE: runFetch,
	}
	cmd.Flags().StringVar(&fetchOutput, "output", "", "Write the whole body to this file; stdout gets the document without body")
	cmd.Flags().StringVar(&fetchFormat, "format", formatRaw, "Output format: raw (the only one supported)")
	cmd.Flags().IntVar(&fetchTimeout, "timeout", fetch.DefaultTimeout, "Per-attempt timeout in seconds")
	cmd.Flags().IntVar(&fetchRetries, "retries", fetch.DefaultMaxRetries, "Retries on connection errors and 5xx")
	cmd.Flags().StringVar(&fetchUserAgent, "user-agent", "", "Override the request User-Agent")
	cmd.Flags().IntVar(&fetchMaxBytes, "max-bytes", fetch.DefaultMaxBodyBytes, "Maximum response body size in bytes; a larger body is an error")
	cmd.Flags().BoolVar(&fetchJSON, "json", false, "Output as JSON")
	cmd.Flags().IntVar(&fetchHead, "head", defaultHead, "Print the first N bytes of the body, cut on a character boundary")
	cmd.Flags().BoolVar(&fetchFull, "full", false, "Print the whole body; cannot be used with --head")
	return cmd
}

func runFetch(cmd *cobra.Command, args []string) error {
	// Checked before the URL so a bad cut never costs a request. The wording
	// is cobra's own for a bad flag value, which ExitCodeFor reads as exit 2.
	if fetchHead < 1 {
		return fmt.Errorf("invalid argument %q for \"--head\" flag: must be 1 or more", fmt.Sprint(fetchHead))
	}
	if fetchFull && cmd.Flags().Changed("head") {
		return fmt.Errorf("invalid argument %q for \"--head\" flag: --head and --full cannot be used together", fmt.Sprint(fetchHead))
	}
	rawURL := args[0]
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return errInvalidURL
	}
	if fetchFormat != formatRaw {
		return output.UsageError(fmt.Errorf("unsupported --format %q: only %q is available", fetchFormat, formatRaw))
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
	if cmd.Flags().Changed("max-bytes") {
		cfg.MaxBodyBytes = fetchMaxBytes
	}

	res := fetch.New(cfg).Fetch(rawURL)
	if res.Err != nil {
		return fmt.Errorf("fetch %s: %w", rawURL, res.Err)
	}

	out := FetchResult{
		URL:         res.URL,
		FinalURL:    res.FinalURL,
		Status:      res.Status,
		Tier:        string(res.Tier),
		Format:      fetchFormat,
		ContentType: res.ContentType,
		Bytes:       res.Bytes,
	}
	w := cmd.OutOrStdout()
	if fetchOutput != "" {
		if err := os.WriteFile(fetchOutput, res.Body, 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		out.Output = fetchOutput
		if !fetchJSON && !GlobalAXIOutput {
			_, err := fmt.Fprintf(w, "Saved %d bytes [HTTP %d via %s, %s] -> %s\n", out.Bytes, res.Status, res.Tier, fetchFormat, fetchOutput)
			return err
		}
		return printFetchResult(w, out)
	}

	limit := fetchHead
	if fetchFull {
		limit = len(res.Body)
	}
	shown := headCut(res.Body, limit)

	if !fetchJSON && !GlobalAXIOutput {
		// --plain is a byte pipe: stdout holds body bytes only, and the size
		// hint goes to stderr so a redirect never saves it into the page.
		if len(shown) < len(res.Body) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "llm-extract: printed %d of %d bytes; run again with --full for the whole body or --output <file> to save it\n", len(shown), len(res.Body))
		}
		_, err := w.Write(shown)
		return err
	}

	body := ""
	if isTextBody(res.ContentType, res.Body) {
		body = string(shown)
		out.Truncated = len(shown) < len(res.Body)
		if out.Truncated {
			out.Help = []string{fetchHelpFull, fetchHelpOutput}
		}
	} else {
		out.Truncated = true
		out.Help = []string{fetchHelpBinary}
	}
	out.Body = &body
	return printFetchResult(w, out)
}

// printFetchResult writes the envelope as JSON under --json and as TOON
// otherwise. help[] belongs to TOON only.
func printFetchResult(w io.Writer, out FetchResult) error {
	if fetchJSON {
		out.Help = nil
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(out)
	}
	// Assembled first and written once, so a failed encode never leaves half
	// a document on stdout.
	var doc bytes.Buffer
	if err := output.EncodeAXI(&doc, out); err != nil {
		return err
	}
	_, err := w.Write(doc.Bytes())
	return err
}

// headCut returns the first n bytes of body. A UTF-8 body is cut on a rune
// boundary, so the cut never splits a character; any other body is cut at n.
func headCut(body []byte, n int) []byte {
	if n >= len(body) {
		return body
	}
	if !utf8.Valid(body) {
		return body[:n]
	}
	for n > 0 && !utf8.RuneStart(body[n]) {
		n--
	}
	return body[:n]
}

// isTextBody reports whether body can go in a text field: valid UTF-8 under a
// text content type. A missing content type is decided by the bytes alone.
func isTextBody(contentType string, body []byte) bool {
	return isTextType(contentType) && utf8.Valid(body)
}

// isTextType reports whether contentType names text: text/*, the JSON, XML
// and JavaScript application types, and any +json or +xml suffix type.
func isTextType(contentType string) bool {
	if strings.TrimSpace(contentType) == "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	if strings.HasPrefix(mt, "text/") || strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml") {
		return true
	}
	switch mt {
	case "application/json", "application/xml", "application/javascript", "application/ecmascript",
		"application/x-javascript", "application/x-ndjson", "application/yaml", "application/x-yaml",
		"application/toml", "application/x-www-form-urlencoded":
		return true
	}
	return false
}

func init() {
	RootCmd.AddCommand(newFetchCmd())
}
