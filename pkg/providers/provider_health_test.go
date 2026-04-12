package providers

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestProviderHealthMapMarksConfiguredProviders(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Providers.OpenAI.APIKey = "sk-test"
	cfg.Providers.OpenRouter.APIKey = "or-test"

	health := ProviderHealthMap(cfg)

	if !health["openai"].Configured || !health["openai"].Selected || !health["openai"].Available {
		t.Fatalf("openai health = %+v, want configured+selected+available", health["openai"])
	}
	if !health["openrouter"].Configured || !health["openrouter"].Available {
		t.Fatalf("openrouter health = %+v, want configured+available", health["openrouter"])
	}
	if health["anthropic"].Configured {
		t.Fatalf("anthropic health = %+v, want unconfigured", health["anthropic"])
	}
}

func TestRuntimeSelectionReportsInvalidExplicitProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Agents.Defaults.Model = "gpt-5"
	cfg.Providers.OpenRouter.APIKey = "or-test"

	status := RuntimeSelection(cfg)
	if status.Valid {
		t.Fatalf("RuntimeSelection() = %+v, want invalid", status)
	}
	if status.Error == "" {
		t.Fatal("RuntimeSelection() error empty, want explicit provider error")
	}
}
