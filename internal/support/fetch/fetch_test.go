package fetch

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestFetcher() *Fetcher {
	return New(&Config{Timeout: 5, MaxRetries: 0, UserAgent: "test-agent"})
}

// Defends the happy path: a 2xx body comes back whole, with its status.
func TestFetchSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	res := newTestFetcher().Fetch(srv.URL)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if string(res.Body) != "hello" || res.Status != 200 || res.Bytes != 5 || res.Tier != TierDirect {
		t.Errorf("bad result: %+v", res)
	}
}

// Defends the User-Agent override reaching the server.
func TestFetchSendsUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.UserAgent()
	}))
	defer srv.Close()
	newTestFetcher().Fetch(srv.URL)
	if got != "test-agent" {
		t.Errorf("User-Agent = %q", got)
	}
}

// Defends "never an empty result": every non-2xx status is an error that
// names the status, including a 404 with a body.
func TestFetchNon2xxIsError(t *testing.T) {
	for _, code := range []int{401, 404, 410, 403, 429, 500, 503} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte("body"))
		}))
		res := newTestFetcher().Fetch(srv.URL)
		srv.Close()
		if res.Err == nil {
			t.Errorf("HTTP %d: no error", code)
			continue
		}
		if !strings.Contains(res.Err.Error(), http.StatusText(code)) {
			t.Errorf("HTTP %d: error does not name the status: %v", code, res.Err)
		}
		if res.Status != code {
			t.Errorf("HTTP %d: Status = %d", code, res.Status)
		}
	}
}

// Defends the challenge case: a Cloudflare interstitial is an error that says
// a browser is needed, and is not retried (the direct tier cannot pass it).
func TestFetchChallengePageIsError(t *testing.T) {
	cases := map[string]struct {
		status int
		header string
		body   string
	}{
		"403 just a moment":   {403, "", "<title>Just a moment...</title>"},
		"503 cf chl":          {503, "", "<script>window._cf_chl_opt={}</script>"},
		"403 cf-mitigated":    {403, "challenge", "nope"},
		"200 strong marker":   {200, "", "<div id=\"challenge-platform\"></div>"},
		"200 cf-mitigated":    {200, "challenge", "ok"},
		"200 reddit verify":   {200, "", "Please wait for verification"},
		"429 attention req'd": {429, "", "Attention Required! | Cloudflare"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var hits int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&hits, 1)
				if c.header != "" {
					w.Header().Set("cf-mitigated", c.header)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			f := New(&Config{Timeout: 5, MaxRetries: 2, UserAgent: "t"})
			f.sleep = func(int) {}
			res := f.Fetch(srv.URL)
			if !errors.Is(res.Err, ErrChallenge) {
				t.Fatalf("want ErrChallenge, got %v", res.Err)
			}
			if !strings.Contains(res.Err.Error(), "browser") {
				t.Errorf("error does not say a browser is needed: %v", res.Err)
			}
			if hits != 1 {
				t.Errorf("challenge was retried: %d hits", hits)
			}
		})
	}
}

// Defends against over-triggering: a normal 200 page that merely says
// "attention required" or "just a moment" is content, not a challenge.
func TestFetchLooseMarkerOn200IsNotChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Attention required: just a moment of your time"))
	}))
	defer srv.Close()
	if res := newTestFetcher().Fetch(srv.URL); res.Err != nil {
		t.Fatalf("ordinary page flagged: %v", res.Err)
	}
}

// Defends the transport-failure case: a refused connection is an error.
func TestFetchConnectionRefusedIsError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	res := newTestFetcher().Fetch(url)
	if res.Err == nil {
		t.Fatal("refused connection reported success")
	}
}

// Defends retries: 5xx is retried up to MaxRetries and a later 200 wins;
// 4xx is not retried.
func TestFetchRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(502)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	f := New(&Config{Timeout: 5, MaxRetries: 2, UserAgent: "t"})
	f.sleep = func(int) {}
	if res := f.Fetch(srv.URL); res.Err != nil || string(res.Body) != "ok" {
		t.Fatalf("retry did not recover: %+v", res)
	}

	hits = 0
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(404)
	}))
	defer srv404.Close()
	f.Fetch(srv404.URL)
	if hits != 1 {
		t.Errorf("404 retried: %d hits", hits)
	}
}

// Defends the exhausted-retries case: the last 5xx is still an error.
func TestFetchRetriesExhaustedIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	f := New(&Config{Timeout: 5, MaxRetries: 1, UserAgent: "t"})
	f.sleep = func(int) {}
	if res := f.Fetch(srv.URL); res.Err == nil {
		t.Fatal("persistent 500 reported success")
	}
}

// Defends redirects: FinalURL records where the body came from.
func TestFetchFinalURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/b", http.StatusFound) })
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("b")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res := newTestFetcher().Fetch(srv.URL + "/a")
	if res.Err != nil || !strings.HasSuffix(res.FinalURL, "/b") {
		t.Errorf("bad result: %+v", res)
	}
}

// Defends an invalid URL: error, not a panic or empty result.
func TestFetchBadURL(t *testing.T) {
	if res := newTestFetcher().Fetch("http://[::1"); res.Err == nil {
		t.Fatal("bad URL reported success")
	}
}

// Defends LoadConfig: env values apply, junk falls back to defaults.
func TestLoadConfig(t *testing.T) {
	t.Setenv("FETCH_TIMEOUT", "7")
	t.Setenv("FETCH_MAX_RETRIES", "0")
	t.Setenv("FETCH_USER_AGENT", "ua")
	c := LoadConfig()
	if c.Timeout != 7 || c.MaxRetries != 0 || c.UserAgent != "ua" {
		t.Errorf("env not applied: %+v", c)
	}
	t.Setenv("FETCH_TIMEOUT", "junk")
	t.Setenv("FETCH_MAX_RETRIES", "-3")
	t.Setenv("FETCH_USER_AGENT", "")
	c = LoadConfig()
	if c.Timeout != DefaultTimeout || c.MaxRetries != DefaultMaxRetries || c.UserAgent == "" {
		t.Errorf("defaults not used: %+v", c)
	}
}

// Defends New(nil) loading config rather than panicking, and a zero Timeout
// falling back to the default.
func TestNewNilConfig(t *testing.T) {
	f := New(nil)
	if f.cfg == nil || f.timeout() <= 0 {
		t.Fatal("nil config not handled")
	}
	if g := New(&Config{}); g.timeout() <= 0 {
		t.Fatal("zero timeout not defaulted")
	}
}

// Defends against a nil Result: a negative MaxRetries must still make one
// attempt, not skip the loop and return nothing.
func TestFetchNegativeRetriesStillAttempts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	res := New(&Config{Timeout: 5, MaxRetries: -1, UserAgent: "t"}).Fetch(srv.URL)
	if res == nil || res.Err != nil || string(res.Body) != "ok" {
		t.Fatalf("bad result: %+v", res)
	}
}

// Defends "never report success when it did not": a body cut short mid-read
// is an error with no body, even though the status was 200.
func TestFetchTruncatedBodyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()
	res := newTestFetcher().Fetch(srv.URL)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "read body") {
		t.Fatalf("truncated body not an error: %+v", res)
	}
	if res.Body != nil || res.Bytes != 0 || res.Status != 200 {
		t.Errorf("failed result carries data: %+v", res)
	}
}
