package providers

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCreateProvider_ExplicitProviderDoesNotFallback(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Agents.Defaults.Model = "gpt-5"
	cfg.Providers.OpenRouter.APIKey = "or-key"

	_, err := CreateProvider(cfg)
	if err == nil {
		t.Fatal("CreateProvider() error = nil, want explicit openai configuration failure")
	}
	if !strings.Contains(err.Error(), `provider "openai"`) {
		t.Fatalf("CreateProvider() error = %q, want explicit openai error", err.Error())
	}
}

func TestCreateProvider_ExplicitProviderIsTrimmed(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Provider = " OpenAI "
	cfg.Agents.Defaults.Model = " gpt-5 "
	cfg.Providers.OpenAI.APIKey = "oa-key"

	provider, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if _, ok := provider.(*HTTPProvider); !ok {
		t.Fatalf("CreateProvider() returned %T, want *HTTPProvider", provider)
	}
}

func TestNormalizeProviderName_Aliases(t *testing.T) {
	tests := map[string]string{
		" gpt ":       "openai",
		"CLAUDE":      "anthropic",
		" local ":     "llamacpp",
		"claude-code": "claude-cli",
		"copilot":     "github_copilot",
		"openrouter":  "openrouter",
	}

	for input, want := range tests {
		if got := NormalizeProviderName(input); got != want {
			t.Fatalf("NormalizeProviderName(%q) = %q, want %q", input, got, want)
		}
	}
}
