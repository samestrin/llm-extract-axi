package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Defends AXI principle 9 on a failed fetch (EST-8204 item 2): the first help[]
// line names the fix for this failure, not only "run --help", which says
// nothing about a 404, a dead host or a body over the cap. The usage line
// stays last, so a caller that wants the flag list still has it.
func TestFetchFailureHelpNamesTheFix(t *testing.T) {
	t.Setenv("FETCH_MAX_RETRIES", "0")
	gone := pageServer(t, http.StatusNotFound, "gone")
	broken := pageServer(t, http.StatusInternalServerError, "oops")
	challenge := pageServer(t, http.StatusForbidden, "<title>Just a moment...</title>")
	big := pageServer(t, http.StatusOK, strings.Repeat("x", 64))
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"HTTP 404", []string{"fetch", gone.URL}, "Check the URL"},
		{"HTTP 500", []string{"fetch", broken.URL}, "--retries"},
		{"challenge page", []string{"fetch", challenge.URL}, "browser"},
		{"over the cap", []string{"fetch", big.URL, "--max-bytes", "8"}, "--max-bytes"},
		{"connection refused", []string{"fetch", deadURL}, "--timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, _ := runCLI(t, tc.args...)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			help := strs(t, axiError(t, stdout), "help")
			if len(help) < 2 {
				t.Fatalf("help %q: want a specific step and the usage line", help)
			}
			if !strings.Contains(help[0], tc.want) {
				t.Errorf("help[0] %q does not mention %q", help[0], tc.want)
			}
			if strings.Contains(help[0], "--help") {
				t.Errorf("help[0] %q is the generic usage line, not a fix", help[0])
			}
			if !strings.Contains(help[len(help)-1], "fetch --help") {
				t.Errorf("last help line %q is not the usage line", help[len(help)-1])
			}
		})
	}
}
