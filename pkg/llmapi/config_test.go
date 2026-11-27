package llmapi

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/samestrin/llm-extract-axi/v2/pkg/output"
)

func TestGetAPIConfig_EnvVars(t *testing.T) {
	// Save original env vars
	origKey := os.Getenv("OPENAI_API_KEY")
	origURL := os.Getenv("OPENAI_BASE_URL")
	origModel := os.Getenv("OPENAI_MODEL")
	defer func() {
		_ = os.Setenv("OPENAI_API_KEY", origKey)
		_ = os.Setenv("OPENAI_BASE_URL", origURL)
		_ = os.Setenv("OPENAI_MODEL", origModel)
	}()

	// Set test env vars
	_ = os.Setenv("OPENAI_API_KEY", "test-api-key")
	_ = os.Setenv("OPENAI_BASE_URL", "https://test.api.com/v1")
	_ = os.Setenv("OPENAI_MODEL", "test-model")

	config := GetAPIConfig()

	if config.APIKey != "test-api-key" {
		t.Errorf("expected APIKey 'test-api-key', got %s", config.APIKey)
	}
	if config.BaseURL != "https://test.api.com/v1" {
		t.Errorf("expected BaseURL 'https://test.api.com/v1', got %s", config.BaseURL)
	}
	if config.Model != "test-model" {
		t.Errorf("expected Model 'test-model', got %s", config.Model)
	}
}

func TestGetAPIConfig_Defaults(t *testing.T) {
	// Save and clear env vars
	origKey := os.Getenv("OPENAI_API_KEY")
	origURL := os.Getenv("OPENAI_BASE_URL")
	origModel := os.Getenv("OPENAI_MODEL")
	defer func() {
		_ = os.Setenv("OPENAI_API_KEY", origKey)
		_ = os.Setenv("OPENAI_BASE_URL", origURL)
		_ = os.Setenv("OPENAI_MODEL", origModel)
	}()

	_ = os.Unsetenv("OPENAI_API_KEY")
	_ = os.Unsetenv("OPENAI_BASE_URL")
	_ = os.Unsetenv("OPENAI_MODEL")

	config := GetAPIConfig()

	// Default values
	if config.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("expected default BaseURL 'https://api.openai.com/v1', got %s", config.BaseURL)
	}
	if config.Model != "gpt-4o-mini" {
		t.Errorf("expected default Model 'gpt-4o-mini', got %s", config.Model)
	}
}

func TestAPIConfig_Validate_Missing(t *testing.T) {
	config := &APIConfig{
		APIKey:  "",
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4o-mini",
	}

	err := config.Validate()
	if err == nil {
		t.Error("expected error for missing API key")
	}
}

func TestAPIConfig_Validate_Valid(t *testing.T) {
	config := &APIConfig{
		APIKey:  "test-key",
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4o-mini",
	}

	err := config.Validate()
	if err != nil {
		t.Errorf("expected no error for valid config, got: %v", err)
	}
}

func TestGetAPIConfig_AnthropicEnvVars(t *testing.T) {
	// Save original env vars
	origKey := os.Getenv("OPENAI_API_KEY")
	origAnthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	defer func() {
		_ = os.Setenv("OPENAI_API_KEY", origKey)
		_ = os.Setenv("ANTHROPIC_API_KEY", origAnthropicKey)
	}()

	// Clear OpenAI key, set Anthropic key
	_ = os.Unsetenv("OPENAI_API_KEY")
	_ = os.Setenv("ANTHROPIC_API_KEY", "anthropic-test-key")

	config := GetAPIConfig()

	// Should fallback to Anthropic key
	if config.APIKey != "anthropic-test-key" {
		t.Errorf("expected APIKey to fallback to ANTHROPIC_API_KEY, got %s", config.APIKey)
	}
}

// Defends against sending an Anthropic key to OpenAI: ANTHROPIC_API_KEY used
// to be accepted with the base URL left at api.openai.com, so the key went to
// the wrong vendor. Validate must refuse that pairing, naming OPENAI_BASE_URL,
// and must still accept the two pairings that say where the key belongs.
func TestValidateAnthropicKeyNeedsBaseURL(t *testing.T) {
	cases := []struct {
		name, openai, anthropic, baseURL string
		wantBaseURLErr                   bool
	}{
		{"anthropic key alone", "", "sk-ant", "", true},
		{"anthropic key with base URL", "", "sk-ant", "https://proxy.example/v1", false},
		{"openai key alone", "sk-openai", "", "", false},
		{"both keys, no base URL: the openai key is the one used", "sk-openai", "sk-ant", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", tc.openai)
			t.Setenv("ANTHROPIC_API_KEY", tc.anthropic)
			t.Setenv("OPENAI_BASE_URL", tc.baseURL)

			err := GetAPIConfig().Validate()
			if !tc.wantBaseURLErr {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrBaseURLRequired) {
				t.Fatalf("Validate() = %v, want ErrBaseURLRequired", err)
			}
			for _, want := range []string{"OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "OpenAI-compatible"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not name %q: %v", want, err)
				}
			}
			var usage interface{ UsageError() bool }
			if !errors.As(err, &usage) || !usage.UsageError() {
				t.Errorf("the error is not marked as a usage error, so it would exit 1: %v", err)
			}
		})
	}
}

// Defends the single usage-error marker: llmapi once kept its own copy of
// output's usageError, so the two could drift and one sentinel could stop
// exiting 2. Both sentinels must carry output.UsageError's type and still
// classify as exit 2.
func TestConfigSentinelsUseOutputUsageError(t *testing.T) {
	want := fmt.Sprintf("%T", output.UsageError(errors.New("x")))
	for name, err := range map[string]error{
		"ErrAPIKeyRequired":  ErrAPIKeyRequired,
		"ErrBaseURLRequired": ErrBaseURLRequired,
	} {
		if got := fmt.Sprintf("%T", err); got != want {
			t.Errorf("%s is a %s, want %s", name, got, want)
		}
		if got := output.ExitCodeFor(err); got != 2 {
			t.Errorf("ExitCodeFor(%s) = %d, want 2", name, got)
		}
	}
}
