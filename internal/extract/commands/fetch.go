package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/samestrin/llm-extract-axi/v2/internal/extract/readable"
	"github.com/samestrin/llm-extract-axi/v2/internal/support/fetch"
	"github.com/samestrin/llm-extract-axi/v2/pkg/output"
)

// formatRaw is the body as received; formatMarkdown is the main content of
// an HTML page as Markdown. llm-tools also offers text for HTML; that is
// rejected rather than ignored, so a caller never gets raw HTML when it asked
// for text.
const (
	formatRaw      = "raw"
	formatMarkdown = "markdown"
)

// errNotHTML is a --format markdown fetch whose page is not HTML. The page
// was fetched, so it is a run error (exit 1), not a usage error.
var errNotHTML = errors.New("not an HTML page")

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
	fetchFields    string
)

// fetchFieldNames are the envelope fields --fields selects from, in envelope
// order. Every field FetchResult can carry is selectable, including the
// conditional output and the TOON-only help.
var fetchFieldNames = []string{"url", "final_url", "status", "tier", "format", "content_type", "bytes", "source_bytes", "truncated", "title", "byline", "output", "body", "help"}

// parseFetchFields turns the --fields value into a set, or nil for an empty
// spec (the whole envelope). An unknown name is a usage error that lists the
// valid ones, in the same wording extract-links --fields uses; the wording
// matches the marker output.ExitCodeFor reads, so the exit code is 2.
func parseFetchFields(spec string) (map[string]bool, error) {
	if spec == "" {
		return nil, nil
	}
	set := make(map[string]bool)
	for _, n := range strings.Split(spec, ",") {
		n = strings.ToLower(strings.TrimSpace(n))
		valid := false
		for _, f := range fetchFieldNames {
			if f == n {
				valid = true
			}
		}
		if !valid {
			return nil, fmt.Errorf("invalid argument %q for \"--fields\" flag: unknown field; valid fields: %s", n, strings.Join(fetchFieldNames, ", "))
		}
		set[n] = true
	}
	return set, nil
}

// selectFetchFields keeps only the fields the caller asked for. It selects
// from the envelope's JSON form, so a selected zero value (bytes 0,
// truncated false) is still printed, while a field the run does not carry
// (output without --output, body under --output) stays absent. UseNumber
// keeps large byte counts exact, as projectJSON does for the whole envelope.
func selectFetchFields(out FetchResult, set map[string]bool) (map[string]any, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	for k := range doc {
		if !set[k] {
			delete(doc, k)
		}
	}
	return doc, nil
}

// FetchResult is the envelope of a successful fetch. A failed fetch is an
// error and has no result.
//
// Bytes is the size of the whole body, and Truncated says Body holds less
// than that: a cut at --head, or no text at all for a body that is not text.
// Body is a pointer so --output can leave it out while an empty body is still
// printed as empty. Help is set under TOON only, as on every other command.
//
// Under --format markdown the body is the Markdown, so Bytes and Truncated
// are relative to the Markdown; SourceBytes is the size of the fetched HTML,
// and Title and Byline come from the page. The three are pointers so a raw
// result leaves them out while a markdown result prints an empty title.
type FetchResult struct {
	URL         string   `json:"url"`
	FinalURL    string   `json:"final_url,omitempty"`
	Status      int      `json:"status"`
	Tier        string   `json:"tier"`
	Format      string   `json:"format"`
	ContentType string   `json:"content_type"`
	Bytes       int      `json:"bytes"`
	SourceBytes *int     `json:"source_bytes,omitempty"`
	Truncated   bool     `json:"truncated"`
	Title       *string  `json:"title,omitempty"`
	Byline      *string  `json:"byline,omitempty"`
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
  --output   save the whole body to a file; the document leaves body out,
             and --plain prints one line instead of the body:
             Saved <n> bytes [HTTP <status> via <tier>, <format>] -> <file>
             --head and --full cannot be used with --output
  --format   raw (the default, the body as received) or markdown (the main
             content of an HTML page as Markdown, made without a model)
  --fields   print only these envelope fields, comma-separated: url,
             final_url, status, tier, format, content_type, bytes,
             source_bytes, truncated, title, byline, output, body, help.
             Composes with --json and --output; --plain prints no envelope,
             so --fields with --plain is a usage error. A selection where no
             named field is present in this run is an error naming the
             fields, never an empty document

--format markdown makes the body the page's main content as Markdown. The
cut, bytes, truncated, --output and --plain then all describe the Markdown;
source_bytes is the fetched HTML's size, and title and byline come from the
page. A page that is not HTML, or has no main content, exits non-zero with
the reason and a help[] line naming --format markdown; it is never an empty
body.

A failed fetch exits non-zero and says why. So does a page that needs a
browser (a Cloudflare or JavaScript challenge page): this command cannot pass
one, and says so instead of printing the challenge page.

Configuration is read from environment variables (flags override them):

  FETCH_TIMEOUT       Per-attempt timeout in seconds (default 30)
  FETCH_MAX_RETRIES   Retries on connection errors and 5xx (default 2)
  FETCH_USER_AGENT    Override the request User-Agent
  FETCH_MAX_BYTES     Maximum response body size in bytes (default 16777216)

A response body larger than the cap is an error, never a truncated page.

Examples:
  # The envelope, with the first 4096 bytes of the body
  llm-extract fetch https://go.dev/doc/

  # Only the size and whether the body was cut, as JSON
  llm-extract fetch https://go.dev/doc/ --fields status,bytes,truncated --json

  # Save the whole body (text or not) to a file
  llm-extract fetch https://go.dev/doc/ --output page.html`,
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
	cmd.Flags().StringVar(&fetchFormat, "format", formatRaw, "Output format: raw (the body as received) or markdown (the main content of an HTML page)")
	cmd.Flags().IntVar(&fetchTimeout, "timeout", fetch.DefaultTimeout, "Per-attempt timeout in seconds")
	cmd.Flags().IntVar(&fetchRetries, "retries", fetch.DefaultMaxRetries, "Retries on connection errors and 5xx")
	cmd.Flags().StringVar(&fetchUserAgent, "user-agent", "", "Override the request User-Agent")
	cmd.Flags().IntVar(&fetchMaxBytes, "max-bytes", fetch.DefaultMaxBodyBytes, "Maximum response body size in bytes; a larger body is an error")
	cmd.Flags().BoolVar(&fetchJSON, "json", false, "Output as JSON")
	cmd.Flags().IntVar(&fetchHead, "head", defaultHead, "Print the first N bytes of the body, cut on a character boundary")
	cmd.Flags().BoolVar(&fetchFull, "full", false, "Print the whole body; cannot be used with --head or --output")
	cmd.Flags().StringVar(&fetchFields, "fields", "", "Comma-separated envelope fields to print: url,final_url,status,tier,format,content_type,bytes,source_bytes,truncated,title,byline,output,body,help (default all)")
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
	// --output saves the whole body, so a cut there would be silently ignored.
	if fetchOutput != "" && cmd.Flags().Changed("head") {
		return fmt.Errorf("invalid argument %q for \"--head\" flag: --head and --output cannot be used together: --output saves the whole body", fmt.Sprint(fetchHead))
	}
	// The envelope leaves body out under --output, so --full printed nothing
	// more and was silently ignored.
	if fetchOutput != "" && fetchFull {
		return fmt.Errorf("invalid argument \"true\" for \"--full\" flag: --full and --output cannot be used together: --output saves the whole body")
	}
	// A bad number is refused, never swapped for the default: --max-bytes 0
	// used to mean 16 MiB and --timeout -1 thirty seconds.
	if err := atLeast("max-bytes", fetchMaxBytes, 1); err != nil {
		return err
	}
	if err := atLeast("timeout", fetchTimeout, 1); err != nil {
		return err
	}
	if err := atLeast("retries", fetchRetries, 0); err != nil {
		return err
	}
	// Also before the URL: a bad field list never costs a request, and under
	// --plain it never writes the --output file. --plain prints no envelope,
	// so a selection there would be silently ignored -- a usage error instead.
	fields, err := parseFetchFields(fetchFields)
	if err != nil {
		return err
	}
	if fields != nil && !fetchJSON && !GlobalAXIOutput {
		return fmt.Errorf("invalid argument %q for \"--fields\" flag: --fields and --plain cannot be used together: --plain prints no envelope", fetchFields)
	}
	rawURL := args[0]
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return errInvalidURL
	}
	if fetchFormat != formatRaw && fetchFormat != formatMarkdown {
		return output.UsageError(fmt.Errorf("unsupported --format %q: use %q or %q", fetchFormat, formatRaw, formatMarkdown))
	}
	// A directory or a path with no parent is refused before the request,
	// not after it with exit 1.
	if fetchOutput != "" {
		if err := checkWritable("--output", fetchOutput); err != nil {
			return err
		}
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
		return &fetchFailure{err: fmt.Errorf("fetch %s: %w", rawURL, res.Err), status: res.Status}
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
	// From here on body is what the caller asked for: the page as received,
	// or its Markdown, which every cut, size and --output below is about.
	body := res.Body
	if fetchFormat == formatMarkdown {
		art, md, err := pageMarkdown(res)
		if err != nil {
			return err
		}
		body = []byte(md)
		out.Bytes = len(body)
		out.SourceBytes = &res.Bytes
		out.Title, out.Byline = &art.Title, &art.Byline
	}
	w := cmd.OutOrStdout()
	if fetchOutput != "" {
		if err := os.WriteFile(fetchOutput, body, 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		out.Output = fetchOutput
		if !fetchJSON && !GlobalAXIOutput {
			_, err := fmt.Fprintf(w, "Saved %d bytes [HTTP %d via %s, %s] -> %s\n", out.Bytes, res.Status, res.Tier, fetchFormat, fetchOutput)
			return err
		}
		return printFetchResult(w, out, fields)
	}

	limit := fetchHead
	if fetchFull {
		limit = len(body)
	}
	shown := headCut(body, limit)

	if !fetchJSON && !GlobalAXIOutput {
		// --plain is a byte pipe: stdout holds body bytes only, and the size
		// hint goes to stderr so a redirect never saves it into the page.
		if len(shown) < len(body) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "llm-extract: printed %d of %d bytes; run again with --full for the whole body or --output <file> to save it\n", len(shown), len(body))
		}
		_, err := w.Write(shown)
		return err
	}

	text := ""
	if fetchFormat == formatMarkdown || isTextBody(res.ContentType, body) {
		text = string(shown)
		out.Truncated = len(shown) < len(body)
		if out.Truncated {
			out.Help = []string{fetchHelpFull, fetchHelpOutput}
		}
	} else {
		out.Truncated = true
		out.Help = []string{fetchHelpBinary}
	}
	out.Body = &text
	return printFetchResult(w, out, fields)
}

// pageMarkdown turns a fetched HTML page into the Markdown of its main
// content. A page that is not HTML, or has no main content, is a
// fetchFailure, so its error names the next step; neither is ever an empty
// body.
func pageMarkdown(res *fetch.Result) (readable.Article, string, error) {
	ct := res.ContentType
	if strings.TrimSpace(ct) == "" {
		// No type sent: the bytes decide, as a browser would.
		ct = http.DetectContentType(res.Body)
	}
	if !isHTMLType(ct) {
		named := res.ContentType
		if strings.TrimSpace(named) == "" {
			named = "none sent; the body reads as " + ct
		}
		return readable.Article{}, "", &fetchFailure{err: fmt.Errorf("fetch %s: --format markdown needs an HTML page, but the content type is %q: %w", res.URL, named, errNotHTML), status: res.Status}
	}
	pageURL := res.FinalURL
	if pageURL == "" {
		pageURL = res.URL
	}
	base, _ := url.Parse(pageURL)
	art, err := readable.Extract(bytes.NewReader(res.Body), base)
	if errors.Is(err, readable.ErrNoMainContent) {
		return readable.Article{}, "", &fetchFailure{err: fmt.Errorf("fetch %s: %w", res.URL, err), status: res.Status}
	}
	if err != nil {
		return readable.Article{}, "", fmt.Errorf("fetch %s: %w", res.URL, err)
	}
	md, err := art.Markdown()
	if err != nil {
		return readable.Article{}, "", fmt.Errorf("fetch %s: %w", res.URL, err)
	}
	// The converter can drop everything Extract kept: a video embed's
	// fallback text reads as main content, but the iframe is removed whole.
	// An empty Markdown is no main content, never an empty success.
	if strings.TrimSpace(md) == "" {
		return readable.Article{}, "", &fetchFailure{err: fmt.Errorf("fetch %s: %w", res.URL, readable.ErrNoMainContent), status: res.Status}
	}
	return art, md, nil
}

// isHTMLType reports whether contentType names an HTML page.
func isHTMLType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && (mt == "text/html" || mt == "application/xhtml+xml")
}

// atLeast refuses a numeric flag below min, in cobra's wording for a bad flag
// value, which output.ExitCodeFor reads as exit 2.
func atLeast(flag string, v, min int) error {
	if v < min {
		return fmt.Errorf("invalid argument %q for \"--%s\" flag: must be %d or more", fmt.Sprint(v), flag, min)
	}
	return nil
}

// printFetchResult writes the envelope as JSON under --json and as TOON
// otherwise, narrowed to fields when --fields named a set. help[] belongs to
// TOON only, so it is dropped before selection, never selectable under --json.
// A selection where no named field is present is an error, not an empty
// document.
func printFetchResult(w io.Writer, out FetchResult, fields map[string]bool) error {
	if fetchJSON {
		out.Help = nil
	}
	var v any = out
	if fields != nil {
		doc, err := selectFetchFields(out, fields)
		if err != nil {
			return err
		}
		if len(doc) == 0 {
			return errEmptyFetchSelection(fields)
		}
		v = doc
	}
	if fetchJSON {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(v)
	}
	// Assembled first and written once, so a failed encode never leaves half
	// a document on stdout.
	var doc bytes.Buffer
	if err := output.EncodeAXI(&doc, v); err != nil {
		return err
	}
	_, err := w.Write(doc.Bytes())
	return err
}

// fetchAbsentWhy explains why a selectable field can be absent from a run.
// The other envelope fields are always present on a successful fetch.
var fetchAbsentWhy = map[string]string{
	"output": "output is set only with --output <file>",
	"body":   "body is left out when --output <file> saves it",
	"help":   "help appears only when the body is cut or not text, and never under --json",
	// One line for the three; errEmptyFetchSelection says it once.
	"source_bytes": fetchMarkdownOnly,
	"title":        fetchMarkdownOnly,
	"byline":       fetchMarkdownOnly,
}

const fetchMarkdownOnly = "source_bytes, title and byline appear only with --format markdown"

// errEmptyFetchSelection reports a --fields selection where every named
// field is absent from this run. An empty document would exit 0 with zero
// bytes on stdout under TOON and {} under --json: success with no result,
// and the two formats disagreeing about whether output exists. The flag
// values are valid, so this is a run error (exit 1), not a usage error; the
// wording deliberately carries none of cobra's usage markers.
func errEmptyFetchSelection(set map[string]bool) error {
	var names, whys []string
	for _, n := range fetchFieldNames {
		if !set[n] {
			continue
		}
		names = append(names, n)
		if why := fetchAbsentWhy[n]; why != "" && !slices.Contains(whys, why) {
			whys = append(whys, why)
		}
	}
	msg := fmt.Sprintf("--fields %s: none of the selected fields are present in this result", strings.Join(names, ","))
	if len(whys) > 0 {
		msg += ": " + strings.Join(whys, "; ")
	}
	return errors.New(msg)
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

// fetchFailure is a fetch that reached no usable page. It carries the HTTP
// status (0 when no response came back), so the error's help[] can name the
// fix for this failure instead of only pointing at --help (AXI principle 9).
type fetchFailure struct {
	err    error
	status int
}

func (f *fetchFailure) Error() string { return f.err.Error() }
func (f *fetchFailure) Unwrap() error { return f.err }

// nextStep is the one thing most likely to get a page after this failure.
// Like every help[] line it names flags only, never the URL (#55).
func (f *fetchFailure) nextStep() string {
	switch {
	case errors.Is(f.err, errNotHTML):
		return "Run again without --format markdown to get the body as received"
	case errors.Is(f.err, readable.ErrNoMainContent):
		return "Run again without --format markdown for the whole page; a page that builds its content with JavaScript needs a browser tool"
	case errors.Is(f.err, fetch.ErrChallenge):
		return "This page needs a real browser; llm-extract cannot pass a challenge page, so open it with a browser tool"
	case errors.Is(f.err, fetch.ErrTooLarge):
		return "Run again with a larger --max-bytes if the whole body is wanted"
	case f.status >= 500:
		return "The server failed; run again later, or with more --retries"
	case f.status >= 400:
		return fmt.Sprintf("Check the URL: the server answered HTTP %d, so the same request will fail again", f.status)
	}
	return "Check the URL and the network, then run again; raise --timeout or --retries for a slow host"
}

func init() {
	RootCmd.AddCommand(newFetchCmd())
}
