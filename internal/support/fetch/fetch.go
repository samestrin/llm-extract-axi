// Package fetch downloads a URL with a plain HTTP GET (the direct tier).
//
// It ports the direct tier of llm-tools' fetch. The proxy and FlareSolverr
// tiers are not ported, so a page that needs a browser cannot be fetched here:
// it is reported as ErrChallenge, never returned as an empty or partial body.
package fetch

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults for Config.
const (
	DefaultTimeout    = 30 // seconds, per attempt
	DefaultMaxRetries = 2
	DefaultUserAgent  = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

// Tier identifies which path produced a result. Only the direct tier exists.
type Tier string

// TierDirect is a plain HTTP GET.
const TierDirect Tier = "direct"

// ErrChallenge marks a Cloudflare or JavaScript interstitial that only a
// browser can pass.
var ErrChallenge = errors.New("page needs a browser (challenge page)")

// Config controls a Fetcher.
type Config struct {
	Timeout    int // seconds per attempt; <= 0 means DefaultTimeout
	MaxRetries int // retries on transport errors and 5xx
	UserAgent  string
}

// LoadConfig reads FETCH_TIMEOUT, FETCH_MAX_RETRIES and FETCH_USER_AGENT.
// A value that is missing or not usable keeps its default.
func LoadConfig() *Config {
	c := &Config{Timeout: DefaultTimeout, MaxRetries: DefaultMaxRetries, UserAgent: DefaultUserAgent}
	if n, err := strconv.Atoi(os.Getenv("FETCH_TIMEOUT")); err == nil && n > 0 {
		c.Timeout = n
	}
	if n, err := strconv.Atoi(os.Getenv("FETCH_MAX_RETRIES")); err == nil && n >= 0 {
		c.MaxRetries = n
	}
	if ua := os.Getenv("FETCH_USER_AGENT"); ua != "" {
		c.UserAgent = ua
	}
	return c
}

// Result is the outcome of a fetch. When Err is non-nil the fetch failed and
// Body must not be used.
type Result struct {
	URL         string
	FinalURL    string
	Status      int
	Tier        Tier
	Bytes       int
	Body        []byte
	ContentType string
	Err         error

	// cfMitigated records the cf-mitigated response header.
	cfMitigated bool
}

// challengeMarkers are case-insensitive substrings of a challenge page, applied
// only when the status already signals a block (403/429/503).
var challengeMarkers = []string{
	"just a moment",
	"cf-browser-verification",
	"challenge-platform",
	"_cf_chl_opt",
	"cf_chl_",
	"attention required",
}

// strongChallengeMarkers are high-confidence fingerprints, safe to act on even
// for a 200 response. Kept narrower than challengeMarkers so a legitimate page
// that merely contains "attention required" is not rejected.
var strongChallengeMarkers = []string{
	"cf-browser-verification",
	"challenge-platform",
	"_cf_chl_opt",
	"cf_chl_",
	"please wait for verification",
}

// Fetcher performs direct fetches for a Config.
type Fetcher struct {
	cfg *Config
	// sleep waits before retry number attempt+1; replaced in tests.
	sleep func(attempt int)
}

// New builds a Fetcher. A nil cfg loads configuration from the environment.
func New(cfg *Config) *Fetcher {
	if cfg == nil {
		cfg = LoadConfig()
	}
	return &Fetcher{cfg: cfg, sleep: func(attempt int) {
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}}
}

func (f *Fetcher) timeout() time.Duration {
	t := f.cfg.Timeout
	if t <= 0 {
		t = DefaultTimeout
	}
	return time.Duration(t) * time.Second
}

// Fetch retrieves rawURL with a direct GET. Transport errors and 5xx are
// retried up to MaxRetries; a challenge page and 4xx are final. Any result that
// is not a 2xx page carries a non-nil Err.
func (f *Fetcher) Fetch(rawURL string) *Result {
	client := &http.Client{Timeout: f.timeout()}
	var res *Result
	for attempt := 0; attempt <= f.cfg.MaxRetries; attempt++ {
		res = f.once(client, rawURL)
		if res.Err == nil || errors.Is(res.Err, ErrChallenge) || (res.Status > 0 && res.Status < 500) {
			return res
		}
		if attempt < f.cfg.MaxRetries {
			f.sleep(attempt)
		}
	}
	return res
}

func (f *Fetcher) once(client *http.Client, rawURL string) *Result {
	res := &Result{URL: rawURL, Tier: TierDirect}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		res.Err = fmt.Errorf("failed to create request: %w", err)
		return res
	}
	ua := f.cfg.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		res.Err = fmt.Errorf("fetch failed: %w", err)
		return res
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)
	res.Status = resp.StatusCode
	res.FinalURL = resp.Request.URL.String()
	res.ContentType = resp.Header.Get("Content-Type")
	res.cfMitigated = resp.Header.Get("cf-mitigated") != ""
	if readErr != nil {
		res.Err = fmt.Errorf("failed to read body: %w", readErr)
		return res
	}
	res.Body = body
	res.Bytes = len(body)
	res.Err = statusError(res)
	if res.Err != nil {
		// A failed fetch carries no body: callers must not mistake it for content.
		res.Body, res.Bytes = nil, 0
	}
	return res
}

// statusError returns nil for a good page and otherwise the error to report.
func statusError(r *Result) error {
	if r.Status >= 200 && r.Status < 300 {
		if r.cfMitigated || isChallengeBody(r.Body, strongChallengeMarkers) {
			return fmt.Errorf("HTTP %d: %w", r.Status, ErrChallenge)
		}
		return nil
	}
	if r.cfMitigated || isChallengeBody(r.Body, challengeMarkers) {
		switch r.Status {
		case http.StatusForbidden, http.StatusServiceUnavailable, http.StatusTooManyRequests:
			return fmt.Errorf("HTTP %d: %w", r.Status, ErrChallenge)
		}
	}
	return fmt.Errorf("HTTP %d %s", r.Status, http.StatusText(r.Status))
}

func isChallengeBody(body []byte, markers []string) bool {
	if len(body) == 0 {
		return false
	}
	n := len(body)
	if n > 4096 {
		n = 4096
	}
	lower := strings.ToLower(string(body[:n]))
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}
