package llmapi

import (
	"errors"
	"os"
)

// DefaultBaseURL is the default OpenAI API endpoint.
const DefaultBaseURL = "https://api.openai.com/v1"

// DefaultModel is the default model to use.
const DefaultModel = "gpt-4o-mini"

// APIConfig holds API configuration settings.
type APIConfig struct {
	APIKey  string
	BaseURL string
	Model   string

	// keyFromAnthropic and baseURLSet record where GetAPIConfig found the key
	// and whether the endpoint was chosen, so Validate can refuse to send an
	// Anthropic key to the OpenAI default.
	keyFromAnthropic bool
	baseURLSet       bool
}

// GetAPIConfig loads API configuration from environment variables with defaults.
// Supported environment variables:
//   - OPENAI_API_KEY or ANTHROPIC_API_KEY for API key (required)
//   - OPENAI_BASE_URL for custom endpoint (default: https://api.openai.com/v1,
//     and required when the key comes from ANTHROPIC_API_KEY)
//   - OPENAI_MODEL for model selection (default: gpt-4o-mini)
func GetAPIConfig() *APIConfig {
	config := &APIConfig{
		APIKey:     getEnvWithFallbacks("OPENAI_API_KEY", "ANTHROPIC_API_KEY"),
		BaseURL:    getEnvOrDefault("OPENAI_BASE_URL", DefaultBaseURL),
		Model:      getEnvOrDefault("OPENAI_MODEL", DefaultModel),
		baseURLSet: os.Getenv("OPENAI_BASE_URL") != "",
	}
	config.keyFromAnthropic = os.Getenv("OPENAI_API_KEY") == "" && config.APIKey != ""
	return config
}

// ErrAPIKeyRequired is what Validate returns when no key is set, so a caller
// can tell a missing key from any other failure without matching its text.
// Like ErrBaseURLRequired it is a usage error (exit 2): the caller must fix
// the configuration before a retry can succeed, so retrying unchanged never
// helps and exit 1 would wrongly suggest it might.
var ErrAPIKeyRequired error = usageError{errors.New("API key is required: set OPENAI_API_KEY, or ANTHROPIC_API_KEY with OPENAI_BASE_URL")}

// usageError marks a configuration error the caller must fix before a retry
// can succeed. output.ExitCodeFor reads UsageError and exits 2.
type usageError struct{ error }

func (usageError) UsageError() bool { return true }

func (e usageError) Unwrap() error { return e.error }

// ErrBaseURLRequired is what Validate returns when the key came from
// ANTHROPIC_API_KEY and OPENAI_BASE_URL is not set. The client speaks the
// OpenAI-compatible API and its default endpoint is OpenAI's, so going ahead
// would send the Anthropic key to the wrong vendor. It never guesses an
// endpoint; the caller names the one the key belongs to.
var ErrBaseURLRequired error = usageError{errors.New("ANTHROPIC_API_KEY is set but OPENAI_BASE_URL is not: " +
	"llm-extract speaks the OpenAI-compatible API and will not send this key to the default " + DefaultBaseURL +
	"; set OPENAI_BASE_URL to the OpenAI-compatible endpoint the key belongs to, or set OPENAI_API_KEY instead")}

// Validate checks that required configuration values are present, and that
// the key is not about to go to an endpoint it does not belong to.
func (c *APIConfig) Validate() error {
	if c.APIKey == "" {
		return ErrAPIKeyRequired
	}
	if c.keyFromAnthropic && !c.baseURLSet {
		return ErrBaseURLRequired
	}
	return nil
}

// getEnvOrDefault returns the environment variable value or a default.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvWithFallbacks returns the first non-empty environment variable.
func getEnvWithFallbacks(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}
