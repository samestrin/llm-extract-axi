package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	goaxi "github.com/samestrin/go-axi"

	"github.com/samestrin/llm-extract-axi/pkg/llmapi"
	"github.com/samestrin/llm-extract-axi/pkg/output"
)

// standInForDefaultURL sends every model call bound for llmapi.DefaultBaseURL
// to a recording server instead, so a test can see whether a run would have
// reached api.openai.com and with which key, without leaving the machine.
func standInForDefaultURL(t *testing.T) (last *atomic.Value, calls func() int32) {
	t.Helper()
	srv, last, n := recordingLLM(t)
	orig := newLLMClient
	t.Cleanup(func() { newLLMClient = orig })
	newLLMClient = func(c *llmapi.APIConfig) *llmapi.LLMClient {
		if c.BaseURL == llmapi.DefaultBaseURL {
			redirected := *c
			redirected.BaseURL = srv.URL
			c = &redirected
		}
		return orig(c)
	}
	return last, n.Load
}

// Defends against the Anthropic key going to OpenAI. With only
// ANTHROPIC_API_KEY set the base URL stayed at api.openai.com, so the key was
// sent to the wrong vendor. The run must stop before any model call, exit 2
// and name OPENAI_BASE_URL.
func TestAnthropicKeyWithoutBaseURLIsRefused(t *testing.T) {
	for _, axi := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "axi"}[axi], func(t *testing.T) {
			t.Cleanup(func() {
				GlobalAXIOutput = false
				output.SetDefaultAXI(false)
			})
			t.Setenv("OPENAI_API_KEY", "")
			t.Setenv("ANTHROPIC_API_KEY", "sk-ant-must-not-leave")
			t.Setenv("OPENAI_BASE_URL", "")
			_, calls := standInForDefaultURL(t)

			args := []string{"extract-relevant", "--path", writeDoc(t), "--context", "c"}
			if axi {
				args = append(args, "--axi")
			}
			code, stdout, stderr := runCLI(t, args...)
			if code != int(goaxi.ExitUsage) {
				t.Errorf("exited %d, want %d (usage)\nstdout=%s\nstderr=%s", code, goaxi.ExitUsage, stdout, stderr)
			}
			if !strings.Contains(stdout+stderr, "OPENAI_BASE_URL") {
				t.Errorf("the error does not name OPENAI_BASE_URL:\nstdout=%s\nstderr=%s", stdout, stderr)
			}
			if n := calls(); n != 0 {
				t.Errorf("the default endpoint received %d model calls, want 0", n)
			}
		})
	}
}

// Defends extract-links --context against the same leak: the page is fetched,
// but the ranking must not send the Anthropic key to the default endpoint.
func TestExtractLinksAnthropicKeyWithoutBaseURLMakesNoModelCall(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-must-not-leave")
	t.Setenv("OPENAI_BASE_URL", "")
	_, calls := standInForDefaultURL(t)

	code, out, _ := runCLI(t, "extract-links", "--url", linksPage(t), "--context", "start", "--json")
	if code == 0 {
		t.Fatalf("ranking with an unusable key exited 0:\n%s", out)
	}
	var got ExtractLinksResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !strings.Contains(got.Error, "OPENAI_BASE_URL") {
		t.Errorf("the JSON error does not name OPENAI_BASE_URL: %q", got.Error)
	}
	if n := calls(); n != 0 {
		t.Errorf("the default endpoint received %d model calls, want 0", n)
	}
}

// Defends the two pairings that say where a key belongs: an Anthropic key
// with OPENAI_BASE_URL goes to that URL, and an OpenAI key alone still goes
// to the default endpoint. The fix must refuse only the guess.
func TestKeyPairingsThatNameTheirEndpointStillWork(t *testing.T) {
	t.Run("ANTHROPIC_API_KEY with OPENAI_BASE_URL", func(t *testing.T) {
		srv, last, calls := recordingLLM(t)
		_, defaultCalls := standInForDefaultURL(t)
		t.Setenv("OPENAI_API_KEY", "")
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-probe")
		t.Setenv("OPENAI_BASE_URL", srv.URL)

		if code, _, stderr := extractOneFile(t); code != 0 {
			t.Fatalf("exited %d: %s", code, stderr)
		}
		if calls.Load() != 1 || defaultCalls() != 0 {
			t.Errorf("calls: OPENAI_BASE_URL got %d, default got %d; want 1 and 0", calls.Load(), defaultCalls())
		}
		if c, _ := last.Load().(modelCall); c.auth != "Bearer sk-ant-probe" {
			t.Errorf("the call carried %q, want the Anthropic key", c.auth)
		}
	})
	t.Run("OPENAI_API_KEY alone", func(t *testing.T) {
		last, defaultCalls := standInForDefaultURL(t)
		t.Setenv("OPENAI_API_KEY", "sk-openai-probe")
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("OPENAI_BASE_URL", "")

		if code, _, stderr := extractOneFile(t); code != 0 {
			t.Fatalf("exited %d: %s", code, stderr)
		}
		if defaultCalls() != 1 {
			t.Errorf("the default endpoint got %d calls, want 1", defaultCalls())
		}
		if c, _ := last.Load().(modelCall); c.auth != "Bearer sk-openai-probe" {
			t.Errorf("the call carried %q, want the OpenAI key", c.auth)
		}
	})
}

// Defends the home view's configuration report: with only ANTHROPIC_API_KEY
// set it must not read as ready, since extract-relevant would refuse to run.
// Both modes say why, and a usable configuration carries no such note.
func TestHomeViewReportsAnthropicKeyWithoutBaseURL(t *testing.T) {
	homeEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret-anthropic")

	out, m := runAXIHome(t)
	if msg, _ := m["api_config_error"].(string); !strings.Contains(msg, "OPENAI_BASE_URL") {
		t.Errorf("api_config_error = %v, want a message naming OPENAI_BASE_URL\n%s", m["api_config_error"], out)
	}
	if strings.Contains(out, "sk-secret") {
		t.Errorf("the home view printed the API key:\n%s", out)
	}

	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exited %d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "OPENAI_BASE_URL") {
		t.Errorf("the text home view does not say OPENAI_BASE_URL is needed:\n%s", stdout.String())
	}

	t.Setenv("OPENAI_BASE_URL", "https://proxy.example/v1")
	out, m = runAXIHome(t)
	if _, ok := m["api_config_error"]; ok {
		t.Errorf("a usable configuration still reports api_config_error:\n%s", out)
	}
}
