package llmapi

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Defends EST-8274 item 5: a failed request printed the base URL whole, so a
// key in its query (OPENAI_BASE_URL=...?api-key=QTOKEN) or the password in an
// unparsable URL reached the error. Both the plain and the streaming call
// redact it; the host stays, so the error still says where it went.
func TestRequestErrorsRedactBaseURL(t *testing.T) {
	for _, base := range []string{
		"http://127.0.0.1:1/v1?api-key=QTOKEN&model=m",
		"http://127.0.0.1:1/v1?Token=QTOKEN",
		"http://127.0.0.1:1/v1?client_secret=QTOKEN",
		"http://user:BASEPASS@127.0.0.1:1/v1",
		"ht!tp://user:BASEPASS@x",
		"http://user:BASEPASS@x/%zz",
	} {
		c := NewLLMClient("k", base, "m")
		c.MaxRetries = 0
		c.HTTPClient.Timeout = 2 * time.Second
		_, err := c.CompleteWithContext(context.Background(), "", "hi")
		_, serr := c.CompleteMessagesStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, time.Second)
		for _, e := range []error{err, serr} {
			if e == nil {
				t.Fatalf("%s: no error", base)
			}
			if msg := e.Error(); strings.Contains(msg, "QTOKEN") || strings.Contains(msg, "BASEPASS") {
				t.Errorf("%s: secret in error: %s", base, msg)
			}
		}
		if strings.Contains(base, "127.0.0.1") && !strings.Contains(err.Error(), "127.0.0.1:1") {
			t.Errorf("%s: host dropped from error: %s", base, err)
		}
	}
}

// Defends the redaction itself: only values of secret-named parameters are
// replaced, so an ordinary parameter stays readable.
func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"http://h/v1?api-key=Q&model=m": "http://h/v1?api-key=REDACTED&model=m",
		"http://u:p@h/v1":               "http://u:REDACTED@h/v1",
		"http://h/v1?sig=Q":             "http://h/v1?sig=REDACTED",
		"ht!tp://u:p@x":                 "(base URL not shown: it does not parse)",
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
