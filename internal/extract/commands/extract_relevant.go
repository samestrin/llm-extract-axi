package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"

	"github.com/samestrin/llm-extract-axi/v2/internal/support/fetch"
	"github.com/samestrin/llm-extract-axi/v2/pkg/llmapi"
	"github.com/samestrin/llm-extract-axi/v2/pkg/output"
)

var (
	extractRelevantContext     string
	extractRelevantConcurrency int
	extractRelevantOutput      string
	extractRelevantTimeout     int
	extractRelevantJSON        bool
	extractRelevantMinimal     bool
	extractRelevantPath        string
)

// newLLMClient builds the model client for both commands. It is a variable so
// a test can drop the retry backoff, which costs 14s against a fake 500.
var newLLMClient = llmapi.NewLLMClientFromConfig

// ExtractRelevantResult holds the extraction result
type ExtractRelevantResult struct {
	Path            string   `json:"path,omitempty"`
	P               string   `json:"p,omitempty"`
	URL             string   `json:"url,omitempty"`
	U               string   `json:"u,omitempty"`
	Context         string   `json:"context,omitempty"`
	Ctx             string   `json:"ctx,omitempty"`
	ExtractedParts  []string `json:"extracted_parts,omitempty"`
	EP              []string `json:"ep,omitempty"`
	TotalFiles      int      `json:"total_files,omitempty"`
	TF              *int     `json:"tf,omitempty"`
	ProcessedFiles  int      `json:"processed_files,omitempty"`
	PF              *int     `json:"pf,omitempty"`
	Error           string   `json:"error,omitempty"`
	E               string   `json:"e,omitempty"`
	ProcessingTimeS float64  `json:"processing_time_s,omitempty"`
	PTS             *float64 `json:"pts,omitempty"`
	// Total counts the extracted parts. It is a pointer so a successful run
	// that found nothing prints "total":0; a failed run omits it.
	Total *int `json:"total,omitempty"`
	N     *int `json:"n,omitempty"`
	// Message says outright that the run succeeded and found nothing.
	Message string `json:"message,omitempty"`
	Msg     string `json:"msg,omitempty"`
	// Model and BytesSent describe the model calls made.
	Model     string `json:"model,omitempty"`
	M         string `json:"m,omitempty"`
	BytesSent *int64 `json:"bytes_sent,omitempty"`
	BS        *int64 `json:"bs,omitempty"`
	// ModelCalls counts the model calls that answered, one per chunk read;
	// BytesSent is the sum of their prompts.
	ModelCalls *int64 `json:"model_calls,omitempty"`
	MC         *int64 `json:"mc,omitempty"`
	// Truncated says the model read a cut-down input: a prompt longer than
	// maxRelevantPromptBytes is cut, and the content past the cut is unread.
	Truncated string `json:"truncated,omitempty"`
	Tr        string `json:"tr,omitempty"`
	// Skipped counts, by reason, the files a directory run did not read, so a
	// file left out is never left out silently.
	Skipped map[string]int `json:"skipped,omitempty"`
	SK      map[string]int `json:"sk,omitempty"`
	// Help is next steps, set only under --axi; --json never carries it.
	Help []string `json:"help,omitempty"`
}

// noRelevantMessage is the explicit zero-result message.
const noRelevantMessage = "no relevant content found"

// llmBytesSent totals the prompt bytes sent to the model in one run. Both
// commands share it; each resets it at the start of a run.
var llmBytesSent atomic.Int64

// llmPrompts and llmPromptsCut count the prompts sent in one run and how many
// of them were cut to the size limit, so a result built from a cut input says
// so. Reset with llmBytesSent.
var llmPrompts, llmPromptsCut atomic.Int64

// maxRelevantPromptBytes is where extract-relevant cuts the prompt it sends
// the model. Content past the cut is never read.
const maxRelevantPromptBytes = 30000

// resetModelUse clears the per-run model counters.
func resetModelUse() {
	llmBytesSent.Store(0)
	llmPrompts.Store(0)
	llmPromptsCut.Store(0)
}

// modelUse returns the model and byte count to report, or nothing when no
// prompt was sent, so a run that never reached a model does not claim one.
func modelUse(model string) (string, *int64) {
	sent := llmBytesSent.Load()
	if sent <= 0 {
		return "", nil
	}
	return model, &sent
}

// isNone reports whether the model answered that nothing was relevant.
func isNone(s string) bool {
	t := strings.TrimSpace(s)
	return t == "" || t == "NONE"
}

// newExtractRelevantCmd creates the extract-relevant command
func newExtractRelevantCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extract-relevant",
		Short: "Extract relevant content using LLM API",
		Long: `Extract relevant content from files, directories, or URLs using an LLM API.

The command sends content to the LLM with a context describing what
to extract, and returns only the relevant portions.

Supports:
  - Local files and directories
  - URLs (HTTP/HTTPS) - HTML is automatically converted to clean text

Examples:
  # Local files and directories
  llm-extract extract-relevant --path ./src --context "API endpoint definitions"
  llm-extract extract-relevant --path ./docs --context "Configuration options" --concurrency 4
  llm-extract extract-relevant --path ./file.md --context "Code examples" -o output.md

  # URLs (HTML automatically stripped)
  llm-extract extract-relevant --path https://example.com/docs --context "Installation steps"
  llm-extract extract-relevant --path https://api.example.com/openapi.json --context "Authentication endpoints"

API Configuration:
  Set the OPENAI_API_KEY or ANTHROPIC_API_KEY environment variable.
  Optionally set OPENAI_BASE_URL and OPENAI_MODEL for custom endpoints.
  ANTHROPIC_API_KEY needs OPENAI_BASE_URL: the client speaks the
  OpenAI-compatible API and never sends that key to the OpenAI default.`,
		Args: noArgs,
		// Load-bearing, not redundant: cobra (command.go:1132) silences usage
		// only when cmd.SilenceUsage || c.SilenceUsage, where c is the command
		// Execute() was called on. The unit tests build this command fresh with
		// newExtractRelevantCmd() and call cmd.Execute() directly, so there is
		// no parent root and c == cmd -- the field is the only thing standing
		// between a runtime failure and a Usage dump after the JSON. The root's
		// own flag silences usage for the shipped binary; it cannot reach a
		// parentless run. Do not remove.
		SilenceUsage: true,
		RunE:         runExtractRelevant,
	}

	// No default: "." sent whatever directory the caller stood in to a model.
	cmd.Flags().StringVar(&extractRelevantPath, "path", "", "File, directory or URL to process (required)")
	cmd.Flags().StringVar(&extractRelevantContext, "context", "", "Context describing what content to extract (required)")
	cmd.Flags().IntVar(&extractRelevantConcurrency, "concurrency", 2, "Number of concurrent API calls for directory processing")
	cmd.Flags().StringVarP(&extractRelevantOutput, "output", "o", "", "Output file (default: stdout)")
	cmd.Flags().IntVar(&extractRelevantTimeout, "timeout", 60, "API call timeout in seconds")
	cmd.Flags().BoolVar(&extractRelevantJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&extractRelevantMinimal, "min", false, "Output in minimal/token-optimized format")

	_ = cmd.MarkFlagRequired("path")
	_ = cmd.MarkFlagRequired("context")

	return cmd
}

func runExtractRelevant(cmd *cobra.Command, args []string) error {
	startTime := time.Now()
	targetPath := extractRelevantPath

	// An empty --path is as missing as an absent one: refused, never ".".
	if targetPath == "" {
		return output.UsageError(fmt.Errorf("--path is required"))
	}
	// Validate context is provided
	if extractRelevantContext == "" {
		return output.UsageError(fmt.Errorf("--context is required"))
	}
	// Bad numbers and an unwritable -o are refused before any model call:
	// --timeout 0 used to fail every call, and a bad -o failed after them all.
	if err := atLeast("timeout", extractRelevantTimeout, 1); err != nil {
		return err
	}
	if err := atLeast("concurrency", extractRelevantConcurrency, 1); err != nil {
		return err
	}
	if extractRelevantOutput != "" {
		if err := checkWritable("-o", extractRelevantOutput); err != nil {
			return err
		}
	}

	// Load API configuration
	config := llmapi.GetAPIConfig()
	if err := config.Validate(); err != nil {
		return err
	}

	// Create LLM client
	client := newLLMClient(config)

	result := ExtractRelevantResult{
		Context: extractRelevantContext,
	}

	resetModelUse()
	var err error

	// Check if target is a URL
	if isURL(targetPath) {
		result.URL = targetPath
		err = processURL(cmd, client, targetPath, &result)
	} else {
		result.Path = targetPath

		// Check if target exists
		info, statErr := os.Stat(targetPath)
		if os.IsNotExist(statErr) {
			return fmt.Errorf("path not found: %s", targetPath)
		}
		if statErr != nil {
			return fmt.Errorf("cannot access path: %w", statErr)
		}

		switch {
		case info.IsDir():
			err = processDirectory(cmd, client, targetPath, &result)
		case info.Mode().IsRegular():
			err = processSingleFile(cmd, client, targetPath, &result)
		default:
			// Opening a FIFO blocks until a writer appears, and /dev/zero
			// never ends.
			return fmt.Errorf("cannot read --path %s: not a regular file or directory", targetPath)
		}
	}

	if err != nil {
		result.Error = err.Error()
	}

	result.ProcessingTimeS = time.Since(startTime).Seconds()
	result.Model, result.BytesSent = modelUse(config.Model)
	if cut := llmPromptsCut.Load(); cut > 0 {
		result.Truncated = fmt.Sprintf("model input cut at %d bytes for %d of %d inputs; the content past the cut was not read", maxRelevantPromptBytes, cut, llmPrompts.Load())
	}
	if result.Error == "" {
		total := len(result.ExtractedParts)
		result.Total = &total
		if total == 0 {
			result.Message = noRelevantMessage
		}
	}

	if GlobalAXIOutput {
		result.Help = extractRelevantHelp(result)
	}

	if err := outputExtractResult(cmd, result); err != nil {
		return err
	}
	// The output already carries the error; returning it as well is what makes
	// the exit code non-zero. Only a result printed on stdout counts as
	// reported: --json prints it even with -o, but otherwise -o sends it to
	// the file, so run() prints the error (under --axi the -o branch is silent).
	if result.Error != "" {
		err := errors.New(result.Error)
		if extractRelevantOutput == "" || extractRelevantJSON {
			return reportedError{err}
		}
		return err
	}
	return nil
}

// checkWritable reports whether path can be written as the file that flag
// (-o, --output) names, without writing it: an existing file is opened for
// writing and left as it was, and for a new file its directory is tested with
// a temporary file that is removed at once. Failure is a usage error: the
// same path never works.
func checkWritable(flag, path string) error {
	refuse := func(err error) error {
		return output.UsageError(fmt.Errorf("cannot write %s %s: %w", flag, path, err))
	}
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		return refuse(errors.New("it is a directory"))
	case err == nil && !info.Mode().IsRegular():
		// Opening a FIFO for writing blocks until a reader appears.
		return refuse(errors.New("not a regular file"))
	case err == nil:
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return refuse(err)
		}
		return f.Close()
	case !errors.Is(err, os.ErrNotExist):
		return refuse(err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".llm-extract-check-*")
	if err != nil {
		return refuse(err)
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// readCapped reads at most limit+1 bytes of the file at path: one past the
// limit, so a caller can tell the file ran over it.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit+1))
}

func processSingleFile(cmd *cobra.Command, client *llmapi.LLMClient, filePath string, result *ExtractRelevantResult) error {
	// The model reads no more than maxRelevantPromptBytes, so neither does
	// this; the byte past it makes the prompt cut, and so its truncated note.
	content, err := readCapped(filePath, maxRelevantPromptBytes)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	extracted, err := extractRelevantContent(cmd.Context(), client, string(content), extractRelevantContext, extractRelevantTimeout)
	if err != nil {
		return err
	}

	if !isNone(extracted) {
		result.ExtractedParts = append(result.ExtractedParts, extracted)
	}
	result.TotalFiles = 1
	result.ProcessedFiles = 1

	return nil
}

// isURL checks if the given path is an HTTP/HTTPS URL
func isURL(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
}

// processURL fetches and processes content from a URL
func processURL(cmd *cobra.Command, client *llmapi.LLMClient, url string, result *ExtractRelevantResult) error {
	content, err := fetchURL(cmd.Context(), url, extractRelevantTimeout)
	if err != nil {
		return fmt.Errorf("failed to fetch URL: %w", err)
	}

	extracted, err := extractRelevantContent(cmd.Context(), client, content, extractRelevantContext, extractRelevantTimeout)
	if err != nil {
		return err
	}

	if !isNone(extracted) {
		result.ExtractedParts = append(result.ExtractedParts, extracted)
	}
	result.TotalFiles = 1
	result.ProcessedFiles = 1

	return nil
}

// maxRemoteBodyBytes caps the page body extract-relevant and extract-links
// read into memory. It is the fetch tier's cap, so FETCH_MAX_BYTES moves all
// three commands together. A variable so a test can lower it.
var maxRemoteBodyBytes = loadRemoteBodyCap()

// loadRemoteBodyCap reads the configured cap: 16 MiB unless FETCH_MAX_BYTES
// sets another positive value.
func loadRemoteBodyCap() int {
	return fetch.LoadConfig().MaxBodyBytes
}

// fetchURL fetches content from a URL and converts HTML to plain text. The
// request runs under ctx, so a cancelled run stops it; the client timeout
// still bounds a single fetch.
func fetchURL(ctx context.Context, url string, timeout int) (string, error) {
	client := &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set a reasonable User-Agent
	req.Header.Set("User-Agent", "llm-extract/1.0 (extract-relevant)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.7")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP error: %s", resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")

	body, err := readCappedBody(resp.Body)
	if err != nil {
		return "", err
	}

	// Check if it's HTML content
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml") {
		return htmlToText(bytes.NewReader(body))
	}

	// For non-HTML content, read as-is
	return string(body), nil
}

// readCappedBody reads a response body of at most maxRemoteBodyBytes. It reads
// one byte past the cap, as the fetch tier does: if that byte arrives the body
// is larger than the cap and the read is an error, never a truncated page
// handed on as if whole. LimitReader bounds memory against an endless body.
func readCappedBody(r io.Reader) ([]byte, error) {
	limit := maxRemoteBodyBytes
	readN := int64(limit)
	// Guard the +1 against overflow at the maximum cap, which would wrap
	// negative and make LimitReader return an empty success.
	if readN < math.MaxInt64 {
		readN++
	}
	body, err := io.ReadAll(io.LimitReader(r, readN))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if len(body) > limit {
		return nil, fmt.Errorf("%w: body exceeds %d-byte cap (raise it with FETCH_MAX_BYTES)", fetch.ErrTooLarge, limit)
	}
	return body, nil
}

// htmlToText converts HTML content to clean plain text
func htmlToText(r io.Reader) (string, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Remove script, style, and other non-content elements
	doc.Find("script, style, noscript, iframe, svg, nav, footer, header").Remove()

	// Extract text content with structure preservation
	var buf strings.Builder

	// Try to get the page title
	if title := doc.Find("title").First().Text(); title != "" {
		buf.WriteString("# ")
		buf.WriteString(strings.TrimSpace(title))
		buf.WriteString("\n\n")
	}

	// Process main content (prefer article/main, fall back to body)
	var mainContent *goquery.Selection
	if article := doc.Find("article, main, [role='main']").First(); article.Length() > 0 {
		mainContent = article
	} else {
		mainContent = doc.Find("body")
	}

	// Extract text with basic structure
	mainContent.Find("h1, h2, h3, h4, h5, h6, p, li, pre, code, blockquote, td, th").Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if text == "" {
			return
		}

		tagName := goquery.NodeName(s)

		switch tagName {
		case "h1":
			buf.WriteString("\n# ")
			buf.WriteString(text)
			buf.WriteString("\n\n")
		case "h2":
			buf.WriteString("\n## ")
			buf.WriteString(text)
			buf.WriteString("\n\n")
		case "h3":
			buf.WriteString("\n### ")
			buf.WriteString(text)
			buf.WriteString("\n\n")
		case "h4", "h5", "h6":
			buf.WriteString("\n#### ")
			buf.WriteString(text)
			buf.WriteString("\n\n")
		case "li":
			buf.WriteString("- ")
			buf.WriteString(text)
			buf.WriteString("\n")
		case "pre", "code":
			buf.WriteString("\n```\n")
			buf.WriteString(text)
			buf.WriteString("\n```\n\n")
		case "blockquote":
			buf.WriteString("> ")
			buf.WriteString(text)
			buf.WriteString("\n\n")
		default:
			buf.WriteString(text)
			buf.WriteString("\n\n")
		}
	})

	result := buf.String()

	// Clean up excessive whitespace
	result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	result = strings.TrimSpace(result)

	if result == "" {
		// Fallback: just get all text from body
		result = strings.TrimSpace(doc.Find("body").Text())
	}

	return result, nil
}

// maxDirFileBytes is the largest file a directory run reads; a larger one is
// skipped and counted as too_large.
const maxDirFileBytes = 100 * 1024

// Reasons a directory run skips a file, as counted in the result's skipped.
const (
	skipDotfile     = "dotfile"
	skipExcludedDir = "excluded_dir"
	skipCredential  = "credential_name"
	skipNotText     = "not_text"
	skipTooLarge    = "too_large"
	skipOutsidePath = "outside_path"
	skipNotRegular  = "not_regular"
	skipUnreadable  = "unreadable"
)

// collectDirFiles walks root and returns the files a directory run reads,
// with a count of the rest by reason. root has been resolved through
// symlinks. A file is read only if it is a regular file whose real path is
// under root: a link out of the tree would send a file the caller never
// named, and a link to /dev/zero or a FIFO would never finish reading.
func collectDirFiles(root string) ([]string, map[string]int, error) {
	var files []string
	skipped := map[string]int{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			skipped[skipUnreadable]++
			return nil
		}

		// A dotfile or dot directory is skipped unless it is the path the
		// caller named: .env, .npmrc and .aws hold secrets, and a directory
		// run must not send them to a model unasked.
		if path != root && strings.HasPrefix(info.Name(), ".") {
			skipped[skipDotfile]++
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if shouldExcludeDir(info.Name()) {
				skipped[skipExcludedDir]++
				return filepath.SkipDir
			}
			return nil
		}
		// Named before the extension test, so a skipped server.key counts as
		// a credential rather than as not text.
		if isCredentialName(info.Name()) {
			skipped[skipCredential]++
			return nil
		}
		// A *.env file is never collected: hasTextExtension leaves the
		// extension out, because those files hold secrets.
		if !hasTextExtension(path) {
			skipped[skipNotText]++
			return nil
		}

		// Walk reports a symlink itself, so its size is the link's length.
		// Resolve it, and judge the target it would read.
		if info.Mode()&os.ModeSymlink != 0 {
			real, err := filepath.EvalSymlinks(path)
			if err != nil {
				skipped[skipUnreadable]++
				return nil
			}
			if !within(root, real) {
				skipped[skipOutsidePath]++
				return nil
			}
			if info, err = os.Stat(real); err != nil {
				skipped[skipUnreadable]++
				return nil
			}
			// The name rules hold for the file the link leads to, so
			// notes.txt -> .env does not send .env under another name.
			rel, _ := filepath.Rel(root, real)
			if reason := targetNameSkip(rel); reason != "" && info.Mode().IsRegular() {
				skipped[reason]++
				return nil
			}
		}
		if !info.Mode().IsRegular() {
			skipped[skipNotRegular]++
			return nil
		}
		if info.Size() > maxDirFileBytes {
			skipped[skipTooLarge]++
			return nil
		}

		files = append(files, path)
		return nil
	})
	return files, skipped, err
}

// targetNameSkip applies the walk's name rules to a link's target, given by
// its path under the root: a dot or excluded directory on the way, then the
// file's own name. It returns the skip reason, or "" to read the file.
func targetNameSkip(rel string) string {
	parts := strings.Split(rel, string(filepath.Separator))
	for _, dir := range parts[:len(parts)-1] {
		if strings.HasPrefix(dir, ".") {
			return skipDotfile
		}
		if shouldExcludeDir(dir) {
			return skipExcludedDir
		}
	}
	name := parts[len(parts)-1]
	switch {
	case strings.HasPrefix(name, "."):
		return skipDotfile
	case isCredentialName(name):
		return skipCredential
	case !hasTextExtension(name):
		return skipNotText
	}
	return ""
}

// within reports whether path is root or lies under it. Both are clean,
// absolute and resolved through symlinks.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// isCredentialName reports whether a file's name marks it as holding a
// credential: credentials*, secret*, *token*, *.key, *.pem, and service
// account files (*service-account*, *service_account*, *serviceaccount*).
// A directory run skips these; README "Limits" states the same list.
func isCredentialName(name string) bool {
	n := strings.ToLower(name)
	switch strings.ToLower(filepath.Ext(n)) {
	case ".key", ".pem":
		return true
	}
	for _, p := range []string{"credentials", "secret"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, s := range []string{"token", "service-account", "service_account", "serviceaccount"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

func processDirectory(cmd *cobra.Command, client *llmapi.LLMClient, dirPath string, result *ExtractRelevantResult) error {
	// Walk the real directory, so a --path that is itself a link to a
	// directory is read and the files under it are judged against it.
	root, err := filepath.Abs(dirPath)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return fmt.Errorf("cannot resolve path: %w", err)
	}
	files, skipped, err := collectDirFiles(root)
	if err != nil {
		return fmt.Errorf("failed to walk directory: %w", err)
	}
	if len(skipped) > 0 {
		result.Skipped = skipped
	}
	dirPath = root

	result.TotalFiles = len(files)

	if len(files) == 0 {
		return fmt.Errorf("no suitable files found in directory")
	}

	// Process files concurrently using batch processor
	processor := llmapi.NewBatchProcessor(client, extractRelevantConcurrency)

	items := make([]interface{}, len(files))
	for i, f := range files {
		items[i] = f
	}

	// The whole-run context is derived from the command's own context, so when
	// the caller supplies a cancellable or deadline-bound context (an embedding
	// caller or a test; the shipped CLI does not wire a signal handler today) a
	// cancel at the top aborts every per-file model call too. The per-file
	// timeout is layered on within extractRelevantContent. cobra guarantees
	// cmd.Context() is non-nil.
	ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(extractRelevantTimeout*len(files))*time.Second)
	defer cancel()

	results := processor.ProcessItems(ctx, items, func(ctx context.Context, c *llmapi.LLMClient, item interface{}) (interface{}, error) {
		filePath := item.(string)
		content, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", filePath, err)
		}

		// Create context-aware prompt for this file
		relPath, _ := filepath.Rel(dirPath, filePath)
		fileContext := fmt.Sprintf("File: %s\n\nContext: %s", relPath, extractRelevantContext)

		extracted, err := extractRelevantContent(ctx, c, string(content), fileContext, extractRelevantTimeout)
		if err != nil {
			return nil, err
		}

		if extracted == "" || strings.TrimSpace(extracted) == "NONE" {
			return nil, nil // No relevant content
		}

		return fmt.Sprintf("## %s\n\n%s", relPath, extracted), nil
	})

	// Collect successful results. A failed file is not dropped silently: the
	// others are kept as a partial result and the run fails, naming the first.
	var failed int
	var firstErr string
	for _, r := range results {
		if r.Error != nil {
			if failed == 0 {
				relPath, _ := filepath.Rel(dirPath, files[r.Index])
				firstErr = fmt.Sprintf("%s: %v", relPath, r.Error)
			}
			failed++
			continue
		}
		result.ProcessedFiles++
		if r.Output != nil {
			if output := r.Output.(string); output != "" {
				result.ExtractedParts = append(result.ExtractedParts, output)
			}
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d files failed (first: %s)", failed, len(files), firstErr)
	}
	return nil
}

// extractRelevantContent sends one extraction prompt and returns the model's
// answer. It takes the caller's run context so a whole-run deadline or
// cancellation reaches the model request; the per-call timeout is layered on
// top with WithTimeout, so the effective bound is whichever fires first. Every
// caller passes a non-nil context: cobra defaults cmd.Context() to
// context.Background() on Execute, and processDirectory derives a child.
func extractRelevantContent(ctx context.Context, client *llmapi.LLMClient, content, contextText string, timeout int) (string, error) {
	systemPrompt := `You are a content extraction assistant. Your task is to extract only the parts of the provided content that are relevant to the given context.

Rules:
1. Only extract content that directly relates to the specified context
2. Preserve the original formatting (code blocks, headers, lists, etc.)
3. If no relevant content is found, respond with "NONE"
4. Do not add explanations or summaries - just extract the relevant portions
5. Keep extracted sections in their original order
6. Include enough surrounding context to make the extracted content understandable`

	userPrompt := fmt.Sprintf("Context: %s\n\n---\n\nContent to extract from:\n\n%s", contextText, content)

	// Truncate content if too long (API limit considerations). The run
	// reports the cut in its result's truncated field.
	llmPrompts.Add(1)
	if len(userPrompt) > maxRelevantPromptBytes {
		userPrompt = cutAtRuneBoundary(userPrompt, maxRelevantPromptBytes) + "\n\n[Content truncated due to length]"
		llmPromptsCut.Add(1)
	}

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	answer, err := client.CompleteWithContext(callCtx, systemPrompt, userPrompt)
	if err != nil {
		return "", err
	}
	// Counted once the call has answered, so a model that was never reached
	// is not reported as used.
	llmBytesSent.Add(int64(len(systemPrompt) + len(userPrompt)))
	return answer, nil
}

func shouldExcludeDir(name string) bool {
	excludedDirs := map[string]bool{
		".git":          true,
		"node_modules":  true,
		"vendor":        true,
		"dist":          true,
		"build":         true,
		"__pycache__":   true,
		".pytest_cache": true,
		"target":        true,
		"coverage":      true,
		".stryker-tmp":  true,
		".next":         true,
		".nuxt":         true,
		"out":           true,
		".vscode":       true,
		".idea":         true,
	}
	return excludedDirs[name]
}

func hasTextExtension(path string) bool {
	textExts := map[string]bool{
		".md": true, ".txt": true, ".go": true, ".py": true, ".js": true,
		".ts": true, ".jsx": true, ".tsx": true, ".html": true, ".css": true,
		".json": true, ".yaml": true, ".yml": true, ".toml": true, ".xml": true,
		".sh": true, ".bash": true, ".zsh": true, ".sql": true, ".rs": true,
		".java": true, ".kt": true, ".swift": true, ".c": true, ".cpp": true,
		".h": true, ".hpp": true, ".rb": true, ".php": true, ".vue": true,
		".svelte": true, ".astro": true, ".prisma": true, ".graphql": true,
		".proto": true, ".dockerfile": true,
	}

	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		// Check for known extensionless files
		base := strings.ToLower(filepath.Base(path))
		return base == "dockerfile" || base == "makefile" || base == "gemfile" ||
			base == "rakefile" || base == "readme" || base == "license"
	}

	return textExts[ext]
}

func outputExtractResult(cmd *cobra.Command, result ExtractRelevantResult) error {
	// Build the appropriate result struct
	var finalResult ExtractRelevantResult
	if extractRelevantMinimal {
		tf := result.TotalFiles
		pf := result.ProcessedFiles
		pts := result.ProcessingTimeS
		finalResult = ExtractRelevantResult{
			P:    result.Path,
			U:    result.URL,
			Ctx:  result.Context,
			EP:   result.ExtractedParts,
			TF:   &tf,
			PF:   &pf,
			E:    result.Error,
			PTS:  &pts,
			N:    result.Total,
			Msg:  result.Message,
			M:    result.Model,
			BS:   result.BytesSent,
			Tr:   result.Truncated,
			SK:   result.Skipped,
			Help: result.Help,
		}
	} else {
		finalResult = result
	}

	// Write to file if output is specified
	if extractRelevantOutput != "" {
		var outputContent string

		if extractRelevantJSON || extractRelevantMinimal {
			// Use formatter to get JSON output. A Print failure must stop the
			// run before the file is touched: writing an empty file and exiting
			// 0 is reporting success for a render that did not succeed.
			var buf bytes.Buffer
			formatter := output.New(true, extractRelevantMinimal, &buf)
			if printErr := formatter.Print(finalResult, nil); printErr != nil {
				return fmt.Errorf("failed to format output: %w", printErr)
			}
			outputContent = buf.String()
		} else {
			outputContent = plainExtractResult(result)
		}

		if err := os.WriteFile(extractRelevantOutput, []byte(outputContent), 0644); err != nil {
			return fmt.Errorf("failed to write output: %w", err)
		}
		// Under --axi or --json stdout is one structured document and nothing
		// else. The prose summary below would sit in front of the TOON error
		// run() appends on a failure, leaving a stream that decodes as neither.
		// The document printed instead is the result without the passages: those
		// are in the file, which is what -o is for, while the total, the empty
		// state and help[] still reach the caller.
		if GlobalAXIOutput || extractRelevantJSON {
			// A failed run under --axi is reported by run()'s error document;
			// printing the result as well would put two documents on stdout.
			if GlobalAXIOutput && result.Error != "" {
				return nil
			}
			ack := finalResult
			ack.ExtractedParts, ack.EP = nil, nil
			formatter := output.New(extractRelevantJSON, extractRelevantMinimal, cmd.OutOrStdout())
			return formatter.Print(ack, nil)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Extracted content written to: %s\n", extractRelevantOutput)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Files processed: %d/%d\n", result.ProcessedFiles, result.TotalFiles)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Processing time: %.2fs\n", result.ProcessingTimeS)
		if result.Truncated != "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Truncated: %s\n", result.Truncated)
		}
		return nil
	}

	// Output to stdout
	formatter := output.New(extractRelevantJSON, extractRelevantMinimal, cmd.OutOrStdout())
	return formatter.Print(finalResult, func(w io.Writer, data interface{}) {
		_, _ = fmt.Fprint(w, plainExtractResult(result))
	})
}

// extractRelevantHelp is the help[] block under --axi: what to run next after
// an error, an empty result, or an extraction.
func extractRelevantHelp(result ExtractRelevantResult) []string {
	const usage = "Run 'llm-extract extract-relevant --help' for usage"
	field := "path"
	if result.URL != "" {
		field = "url"
	}
	switch {
	case result.Error != "":
		return []string{"Fix the cause named in the error, then run the command again", usage}
	case len(result.ExtractedParts) == 0:
		return []string{"Broaden the context: llm-extract extract-relevant --path " + pathArg(field) + " --context '<broader topic>'"}
	default:
		return []string{"Narrow and save it: llm-extract extract-relevant --path " + pathArg(field) + " --context '<narrower topic>' -o <file>"}
	}
}

// plainExtractResult renders the text output for stdout and -o alike. An error
// and a truncation note lead, and any extraction kept from a partial directory
// run follows them, so text mode loses nothing --json carries.
func plainExtractResult(result ExtractRelevantResult) string {
	parts := strings.Join(result.ExtractedParts, "\n\n---\n\n")
	var lead string
	if result.Error != "" {
		lead = fmt.Sprintf("ERROR: %s\n", result.Error)
	}
	if result.Truncated != "" {
		lead += fmt.Sprintf("TRUNCATED: %s\n", result.Truncated)
	}
	if len(result.Skipped) > 0 {
		lead += "SKIPPED: " + skippedSummary(result.Skipped) + "\n"
	}
	switch {
	case lead != "" && parts != "":
		return lead + "\n" + parts
	case result.Error != "":
		return lead
	case parts == "":
		return lead + noRelevantMessage + "\n"
	default:
		return parts
	}
}

// skippedSummary renders the skip counts as "2 dotfile, 1 too_large", in
// reason order.
func skippedSummary(skipped map[string]int) string {
	reasons := make([]string, 0, len(skipped))
	for r := range skipped {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	for i, r := range reasons {
		reasons[i] = fmt.Sprintf("%d %s", skipped[r], r)
	}
	return strings.Join(reasons, ", ")
}

func init() {
	RootCmd.AddCommand(newExtractRelevantCmd())
}

// cutAtRuneBoundary returns at most limit bytes of s, cut on the last
// character boundary at or below limit, so a cut never sends the model half
// of a multi-byte character.
func cutAtRuneBoundary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}
