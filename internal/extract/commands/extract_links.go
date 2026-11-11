package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"

	"github.com/samestrin/llm-extract-axi/pkg/llmapi"
	"github.com/samestrin/llm-extract-axi/pkg/output"
)

var (
	extractLinksURL     string
	extractLinksContext string
	extractLinksTimeout int
	extractLinksJSON    bool
	extractLinksMinimal bool
	extractLinksFields  string
)

// linkFields are the fields a link row can carry, in print order.
var linkFields = []string{"href", "text", "score", "section", "context"}

// defaultLinkFields is the row without --fields. context is left out because
// it is the least useful of the five per token; --fields context brings it back.
var defaultLinkFields = []string{"href", "text", "score", "section"}

// parseLinkFields turns the --fields value into a set. An unknown name is a
// usage error that lists the valid ones; the wording matches the marker
// output.ExitCodeFor reads, so the exit code is 2.
func parseLinkFields(spec string) (map[string]bool, error) {
	names := defaultLinkFields
	if spec != "" {
		names = strings.Split(spec, ",")
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		valid := false
		for _, f := range linkFields {
			if f == n {
				valid = true
			}
		}
		if !valid {
			return nil, fmt.Errorf("invalid argument %q for \"--fields\" flag: unknown field; valid fields: %s", n, strings.Join(linkFields, ", "))
		}
		set[n] = true
	}
	return set, nil
}

// selectLinkFields blanks every field the caller did not ask for. Each field
// is omitempty, so a blanked field is absent from text, --json and --axi alike.
func selectLinkFields(links []LinkInfo, set map[string]bool) []LinkInfo {
	out := make([]LinkInfo, len(links))
	for i, l := range links {
		if !set["href"] {
			l.Href = ""
		}
		if !set["text"] {
			l.Text = ""
		}
		if !set["score"] {
			l.Score = 0
		}
		if !set["section"] {
			l.Section = ""
		}
		if !set["context"] {
			l.Context = ""
		}
		out[i] = l
	}
	return out
}

// LinkInfo represents a single extracted link with context
type LinkInfo struct {
	Href    string `json:"href,omitempty"`
	H       string `json:"h,omitempty"`
	Text    string `json:"text,omitempty"`
	T       string `json:"t,omitempty"`
	Context string `json:"context,omitempty"`
	C       string `json:"c,omitempty"`
	Score   int    `json:"score,omitempty"`
	S       *int   `json:"s,omitempty"`
	Section string `json:"section,omitempty"`
	Sec     string `json:"sec,omitempty"`
}

// ExtractLinksResult holds the extraction result
type ExtractLinksResult struct {
	URL   string     `json:"url,omitempty"`
	U     string     `json:"u,omitempty"`
	Links []LinkInfo `json:"links,omitempty"`
	L     []LinkInfo `json:"l,omitempty"`
	// Total is a pointer so a successful run that found nothing prints
	// "total":0, while a failed run, which has no total, omits it.
	Total *int `json:"total,omitempty"`
	N     *int `json:"n,omitempty"`
	// Message says outright that a run succeeded and found no links, so an
	// empty result cannot be mistaken for a failed one.
	Message string `json:"message,omitempty"`
	Msg     string `json:"msg,omitempty"`
	// Model and BytesSent are set only when a model was called.
	Model     string `json:"model,omitempty"`
	M         string `json:"m,omitempty"`
	BytesSent *int64 `json:"bytes_sent,omitempty"`
	BS        *int64 `json:"bs,omitempty"`
	Error     string `json:"error,omitempty"`
	E         string `json:"e,omitempty"`
	// Help is next steps, set only under --axi; --json never carries it.
	Help []string `json:"help,omitempty"`
}

// noLinksMessage is the explicit zero-result message.
const noLinksMessage = "no links found"

// newExtractLinksCmd creates the extract-links command
func newExtractLinksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extract-links",
		Short: "Extract and rank links from a URL",
		Long: `Extract links from a URL with intelligent ranking.

RANKING MODES:

1. Heuristic Mode (default - no --context flag):
   Links are scored based on their position in the document hierarchy:
     - h1: 100, h2: 85, h3: 70, h4-h6: 55
     - main/article: 50, p: 40, li: 35
     - nav: 30, aside: 20, footer: 10
   
   Modifiers add bonus points:
     - bold/strong: +15, em/i: +10
     - button role or .btn class: +10
     - has title attribute: +5

2. LLM Mode (when --context is provided):
   Uses an LLM to rank links by relevance to the specified context.
   Scores range from 0-100 based on semantic relevance.
   Requires OpenAI-compatible API configuration.

Each link includes its parent section heading when available.

Examples:
  # Heuristic ranking (default)
  llm-extract extract-links --url https://example.com/docs
  llm-extract extract-links --url https://example.com --json

  # LLM-based ranking
  llm-extract extract-links --url https://example.com/docs --context "authentication"
  llm-extract extract-links --url https://api.example.com --context "rate limiting" --json`,
		Args: cobra.NoArgs,
		// Load-bearing, not redundant: cobra (command.go:1132) silences usage
		// only when cmd.SilenceUsage || c.SilenceUsage, where c is the command
		// Execute() was called on. The unit tests build this command fresh with
		// newExtractLinksCmd() and call cmd.Execute() directly, so there is no
		// parent root and c == cmd -- the field is the only thing standing
		// between a runtime failure and a Usage dump after the JSON. The root's
		// own flag silences usage for the shipped binary; it cannot reach a
		// parentless run. Do not remove.
		SilenceUsage: true,
		RunE:         runExtractLinks,
	}

	cmd.Flags().StringVar(&extractLinksURL, "url", "", "URL to extract links from (required)")
	cmd.Flags().StringVar(&extractLinksContext, "context", "", "Context for LLM-based ranking (enables LLM mode)")
	cmd.Flags().IntVar(&extractLinksTimeout, "timeout", 30, "HTTP timeout in seconds")
	cmd.Flags().BoolVar(&extractLinksJSON, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&extractLinksMinimal, "min", false, "Output in minimal/token-optimized format")
	cmd.Flags().StringVar(&extractLinksFields, "fields", "", "Comma-separated link fields to print: href,text,score,section,context (default href,text,score,section)")

	_ = cmd.MarkFlagRequired("url")

	return cmd
}

func runExtractLinks(cmd *cobra.Command, args []string) error {
	if extractLinksURL == "" {
		return fmt.Errorf("--url is required")
	}

	// Validate URL
	if !isURL(extractLinksURL) {
		return errInvalidURL
	}

	if _, err := parseLinkFields(extractLinksFields); err != nil {
		return err
	}

	result := ExtractLinksResult{
		URL: extractLinksURL,
	}

	llmBytesSent.Store(0)
	links, err := fetchAndExtractLinks(cmd.Context(), extractLinksURL, extractLinksTimeout)
	if err != nil {
		result.Error = err.Error()
	} else {
		// If context is provided, use LLM ranking instead of heuristic
		if extractLinksContext != "" {
			// On failure the heuristic links are kept: they are printed as a
			// partial result alongside the error, and the exit is non-zero.
			// All of them: rankLinksWithLLM may return only the first 100.
			ranked, err := rankLinksWithLLM(cmd.Context(), links, extractLinksContext, extractLinksTimeout)
			if err != nil {
				result.Error = fmt.Sprintf("LLM ranking failed: %v", err)
			} else {
				links = ranked
			}
		}
		result.Links = links
		total := len(links)
		result.Total = &total
		if total == 0 {
			result.Message = noLinksMessage
		}
	}
	result.Model, result.BytesSent = modelUse(llmapi.GetAPIConfig().Model)
	if GlobalAXIOutput {
		result.Help = extractLinksHelp(result)
	}

	if err := outputExtractLinksResult(cmd, result); err != nil {
		return err
	}
	// The output already carries the error; returning it as well is what makes
	// the exit code non-zero.
	if result.Error != "" {
		return reportedError{errors.New(result.Error)}
	}
	return nil
}

// extractLinksHelp is the help[] block under --axi: what to run next after an
// error, an empty result, or a list.
func extractLinksHelp(result ExtractLinksResult) []string {
	const usage = "Run 'llm-extract extract-links --help' for usage"
	switch {
	case result.Error != "" && strings.HasPrefix(result.Error, "LLM ranking failed"):
		return []string{"Check OPENAI_API_KEY or ANTHROPIC_API_KEY, or drop --context to rank by page position without a model", usage}
	case result.Error != "":
		return []string{"Check the URL is reachable, or raise --timeout, then run the command again", usage}
	case len(result.Links) == 0:
		return []string{"Read the page text instead: llm-extract extract-relevant --path '" + result.URL + "' --context '<what you need>'"}
	default:
		return []string{"Read the top link: llm-extract extract-relevant --path '" + result.Links[0].Href + "' --context '<what you need>'"}
	}
}

// fetchAndExtractLinks fetches a URL and extracts all links with ranking. The
// request runs under ctx, so a cancelled run stops it; the client timeout
// still bounds a single fetch.
func fetchAndExtractLinks(ctx context.Context, targetURL string, timeout int) ([]LinkInfo, error) {
	client := &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "llm-extract/1.0 (extract-links)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.7")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error: %s", resp.Status)
	}

	body, err := readCappedBody(resp.Body)
	if err != nil {
		return nil, err
	}

	return extractLinksFromHTML(bytes.NewReader(body), targetURL)
}

// extractLinksFromHTML parses HTML and extracts links with context and scoring
func extractLinksFromHTML(r io.Reader, baseURL string) ([]LinkInfo, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Parse base URL for resolving relative links
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	var links []LinkInfo
	seen := make(map[string]bool)

	// Track current section heading
	var currentSection string

	// Process the document in order to track sections
	doc.Find("h1, h2, h3, h4, h5, h6, a").Each(func(i int, s *goquery.Selection) {
		tagName := goquery.NodeName(s)

		// Update current section for headings
		if strings.HasPrefix(tagName, "h") {
			text := strings.TrimSpace(s.Text())
			if text != "" && (tagName == "h1" || tagName == "h2" || tagName == "h3") {
				currentSection = text
			}
			// Also check if heading contains a link
			s.Find("a").Each(func(j int, link *goquery.Selection) {
				if info := processLink(link, base, tagName, currentSection, seen); info != nil {
					links = append(links, *info)
				}
			})
			return
		}

		// Process standalone links
		if tagName == "a" {
			// Determine context from parent elements
			context := getParentContext(s)
			if info := processLink(s, base, context, currentSection, seen); info != nil {
				links = append(links, *info)
			}
		}
	})

	// Sort by score descending
	sortLinksByScore(links)

	return links, nil
}

// processLink extracts info from a link element
func processLink(s *goquery.Selection, base *url.URL, context, section string, seen map[string]bool) *LinkInfo {
	href, exists := s.Attr("href")
	if !exists || href == "" {
		return nil
	}

	// Skip anchors, javascript, and mailto
	if strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") {
		return nil
	}

	// Resolve relative URLs
	resolved := resolveURL(href, base)
	if resolved == "" {
		return nil
	}

	// Skip duplicates
	if seen[resolved] {
		return nil
	}
	seen[resolved] = true

	text := strings.TrimSpace(s.Text())
	if text == "" {
		// Try alt text from images
		if img := s.Find("img"); img.Length() > 0 {
			text, _ = img.Attr("alt")
		}
		// Try title attribute
		if text == "" {
			text, _ = s.Attr("title")
		}
	}

	// Calculate score
	score := calculateLinkScore(s, context)

	return &LinkInfo{
		Href:    resolved,
		Text:    text,
		Context: context,
		Score:   score,
		Section: section,
	}
}

// getParentContext determines the semantic context of a link
func getParentContext(s *goquery.Selection) string {
	// Check parents for context
	contexts := []string{"h1", "h2", "h3", "h4", "h5", "h6", "nav", "main", "article", "aside", "footer", "header", "li", "p"}

	for _, ctx := range contexts {
		if s.ParentsFiltered(ctx).Length() > 0 {
			return ctx
		}
	}

	return "body"
}

// calculateLinkScore calculates importance score for a link
func calculateLinkScore(s *goquery.Selection, context string) int {
	// Base scores by context
	baseScores := map[string]int{
		"h1":      100,
		"h2":      85,
		"h3":      70,
		"h4":      55,
		"h5":      55,
		"h6":      55,
		"main":    50,
		"article": 50,
		"p":       40,
		"li":      35,
		"nav":     30,
		"header":  25,
		"aside":   20,
		"footer":  10,
		"body":    30,
	}

	score := baseScores[context]
	if score == 0 {
		score = 30 // default
	}

	// Modifiers
	// Check if link or its parents have bold/strong
	if s.ParentsFiltered("strong, b").Length() > 0 || s.Find("strong, b").Length() > 0 {
		score += 15
	}

	// Check for emphasis
	if s.ParentsFiltered("em, i").Length() > 0 || s.Find("em, i").Length() > 0 {
		score += 10
	}

	// Check for button-like elements
	if role, _ := s.Attr("role"); role == "button" {
		score += 10
	}
	if class, _ := s.Attr("class"); strings.Contains(class, "btn") || strings.Contains(class, "button") {
		score += 10
	}

	// Has title attribute
	if title, _ := s.Attr("title"); title != "" {
		score += 5
	}

	return score
}

// resolveURL resolves a potentially relative URL against a base
func resolveURL(href string, base *url.URL) string {
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}

	resolved := base.ResolveReference(parsed)
	return resolved.String()
}

// sortLinksByScore sorts links by score in descending order
func sortLinksByScore(links []LinkInfo) {
	for i := 0; i < len(links)-1; i++ {
		for j := i + 1; j < len(links); j++ {
			if links[j].Score > links[i].Score {
				links[i], links[j] = links[j], links[i]
			}
		}
	}
}

// LLMRankRequest represents the input for LLM ranking
type LLMRankRequest struct {
	Context string         `json:"context"`
	Links   []LLMLinkInput `json:"links"`
}

// LLMLinkInput represents a link for LLM input
type LLMLinkInput struct {
	Index   int    `json:"index"`
	Href    string `json:"href"`
	Text    string `json:"text"`
	Section string `json:"section,omitempty"`
}

// LLMRankResponse represents the LLM's ranking response
type LLMRankResponse struct {
	Rankings []LLMRanking `json:"rankings"`
}

// LLMRanking represents a single link's relevance score
type LLMRanking struct {
	Index int `json:"index"`
	Score int `json:"score"`
}

// rankLinksWithLLM uses an LLM to rank links by relevance to the given context.
// The model call runs under ctx with the per-call timeout as a child deadline,
// so whichever fires first ends it.
func rankLinksWithLLM(ctx context.Context, links []LinkInfo, contextText string, timeout int) ([]LinkInfo, error) {
	if len(links) == 0 {
		return links, nil
	}

	// Load API configuration
	config := llmapi.GetAPIConfig()
	if err := config.Validate(); err != nil {
		return links, fmt.Errorf("API configuration error: %w", err)
	}

	// Create LLM client
	client := newLLMClient(config)

	// Prepare links for LLM input (limit to avoid token limits)
	maxLinks := 100
	if len(links) > maxLinks {
		links = links[:maxLinks]
	}

	llmLinks := make([]LLMLinkInput, len(links))
	for i, link := range links {
		llmLinks[i] = LLMLinkInput{
			Index:   i,
			Href:    link.Href,
			Text:    link.Text,
			Section: link.Section,
		}
	}

	// Build the prompt
	systemPrompt := `You are a link relevance analyzer. Given a context and a list of links, score each link's relevance to the context on a scale of 0-100.

Rules:
1. Score 90-100: Directly related, primary resource for the context
2. Score 70-89: Closely related, useful secondary resource
3. Score 50-69: Somewhat related, tangentially useful
4. Score 30-49: Loosely related, might be useful
5. Score 0-29: Not relevant to the context

Respond ONLY with valid JSON in this exact format:
{"rankings":[{"index":0,"score":85},{"index":1,"score":45},...]}

Do not include any explanation or text outside the JSON.`

	linksJSON, err := json.Marshal(llmLinks)
	if err != nil {
		return links, fmt.Errorf("failed to marshal links: %w", err)
	}

	userPrompt := fmt.Sprintf("Context: %s\n\nLinks to rank:\n%s", contextText, string(linksJSON))

	// Truncate if too long
	if len(userPrompt) > 25000 {
		userPrompt = userPrompt[:25000] + "\n...(truncated)"
	}

	// Call the LLM
	llmBytesSent.Add(int64(len(systemPrompt) + len(userPrompt)))
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	response, err := client.CompleteWithContext(callCtx, systemPrompt, userPrompt)
	if err != nil {
		return links, fmt.Errorf("LLM request failed: %w", err)
	}

	// Parse the response
	rankings, err := parseLLMRankings(response, len(links))
	if err != nil {
		return links, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// Apply scores to links
	for _, ranking := range rankings {
		if ranking.Index >= 0 && ranking.Index < len(links) {
			links[ranking.Index].Score = ranking.Score
		}
	}

	// Re-sort by new scores
	sortLinksByScore(links)

	return links, nil
}

// parseLLMRankings parses the LLM response into rankings
func parseLLMRankings(response string, linkCount int) ([]LLMRanking, error) {
	// Clean up response - find JSON object
	response = strings.TrimSpace(response)

	// Find the JSON object boundaries
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no valid JSON found in response")
	}
	response = response[start : end+1]

	var result LLMRankResponse
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Validate rankings
	for i, r := range result.Rankings {
		if r.Score < 0 {
			result.Rankings[i].Score = 0
		} else if r.Score > 100 {
			result.Rankings[i].Score = 100
		}
	}

	return result.Rankings, nil
}

func outputExtractLinksResult(cmd *cobra.Command, result ExtractLinksResult) error {
	var finalResult ExtractLinksResult
	// Selection happens here, after help[] was built from the full links.
	fields, _ := parseLinkFields(extractLinksFields)
	result.Links = selectLinkFields(result.Links, fields)

	if extractLinksMinimal {
		// Convert to minimal format
		minLinks := make([]LinkInfo, len(result.Links))
		for i, link := range result.Links {
			minLinks[i] = LinkInfo{
				H:   link.Href,
				T:   link.Text,
				C:   link.Context,
				Sec: link.Section,
			}
			if fields["score"] {
				score := link.Score
				minLinks[i].S = &score
			}
		}
		finalResult = ExtractLinksResult{
			U:    result.URL,
			L:    minLinks,
			N:    result.Total,
			Msg:  result.Message,
			M:    result.Model,
			BS:   result.BytesSent,
			E:    result.Error,
			Help: result.Help,
		}
	} else {
		finalResult = result
	}

	formatter := output.New(extractLinksJSON, extractLinksMinimal, cmd.OutOrStdout())
	return formatter.Print(finalResult, func(w io.Writer, data interface{}) {
		r := data.(ExtractLinksResult)
		if r.Error != "" {
			_, _ = fmt.Fprintf(w, "ERROR: %s\n", r.Error)
			if len(result.Links) == 0 {
				return
			}
			_, _ = fmt.Fprintln(w)
		}

		_, _ = fmt.Fprintf(w, "URL: %s\n", result.URL)
		if result.Total != nil {
			_, _ = fmt.Fprintf(w, "Total Links: %d\n", *result.Total)
		}
		if result.Message != "" {
			_, _ = fmt.Fprintf(w, "%s\n", result.Message)
		}
		if result.Model != "" {
			_, _ = fmt.Fprintf(w, "Model: %s | Bytes sent: %d\n", result.Model, *result.BytesSent)
		}
		_, _ = fmt.Fprintln(w)

		for _, link := range result.Links {
			var head []string
			if fields["score"] {
				head = append(head, fmt.Sprintf("[%d]", link.Score))
			}
			if fields["text"] {
				head = append(head, link.Text)
			}
			if len(head) > 0 {
				_, _ = fmt.Fprintln(w, strings.Join(head, " "))
			}
			if fields["href"] {
				_, _ = fmt.Fprintf(w, "    %s\n", link.Href)
			}
			var meta []string
			if fields["section"] && link.Section != "" {
				meta = append(meta, "Section: "+link.Section)
			}
			if fields["context"] {
				meta = append(meta, "Context: "+link.Context)
			}
			if len(meta) > 0 {
				_, _ = fmt.Fprintf(w, "    %s\n", strings.Join(meta, " | "))
			}
			_, _ = fmt.Fprintln(w)
		}
	})
}

func init() {
	RootCmd.AddCommand(newExtractLinksCmd())
}
