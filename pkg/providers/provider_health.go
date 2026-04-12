package providers

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

type ProviderHealth struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	Selected   bool   `json:"selected"`
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
}

type RuntimeSelectionStatus struct {
	Provider           string                    `json:"provider"`
	Model              string                    `json:"model"`
	Valid              bool                      `json:"valid"`
	Error              string                    `json:"error,omitempty"`
	BackgroundProvider string                    `json:"background_provider,omitempty"`
	BackgroundModel    string                    `json:"background_model,omitempty"`
	LocalEnabled       bool                      `json:"local_enabled"`
	LocalFallback      bool                      `json:"local_fallback"`
	PrivacyEnabled     bool                      `json:"privacy_enabled"`
	AvailableProviders []string                  `json:"available_providers"`
	Providers          map[string]ProviderHealth `json:"providers"`
}

func ProviderConfigPresent(p config.ProviderConfig) bool {
	return strings.TrimSpace(p.APIKey) != "" || strings.TrimSpace(p.AuthMethod) != ""
}

func ProviderHealthMap(cfg *config.Config) map[string]ProviderHealth {
	selected := NormalizeProviderName(cfg.Agents.Defaults.Provider)
	available := make(map[string]bool)
	for _, name := range AvailableProviders(cfg) {
		available[NormalizeProviderName(name)] = true
	}

	result := map[string]ProviderHealth{}
	add := func(name string, configured bool, reason string) {
		canonical := NormalizeProviderName(name)
		result[canonical] = ProviderHealth{
			Name:       canonical,
			Configured: configured,
			Selected:   canonical == selected,
			Available:  available[canonical],
			Reason:     reason,
		}
	}

	add("openai", ProviderConfigPresent(cfg.Providers.OpenAI), "needs api_key or auth_method")
	add("openrouter", ProviderConfigPresent(cfg.Providers.OpenRouter), "needs api_key")
	add("groq", ProviderConfigPresent(cfg.Providers.Groq), "needs api_key")
	add("anthropic", ProviderConfigPresent(cfg.Providers.Anthropic), "needs api_key or auth_method")
	add("gemini", ProviderConfigPresent(cfg.Providers.Gemini), "needs api_key")
	add("deepseek", ProviderConfigPresent(cfg.Providers.DeepSeek), "needs api_key")
	add("zhipu", ProviderConfigPresent(cfg.Providers.Zhipu), "needs api_key")
	add("vllm", strings.TrimSpace(cfg.Providers.VLLM.APIBase) != "", "needs api_base")
	add("nvidia", ProviderConfigPresent(cfg.Providers.Nvidia), "needs api_key")
	add("moonshot", ProviderConfigPresent(cfg.Providers.Moonshot), "needs api_key")
	add("shengsuanyun", ProviderConfigPresent(cfg.Providers.ShengSuanYun), "needs api_key")
	add("github_copilot", strings.TrimSpace(cfg.Providers.GitHubCopilot.APIBase) != "" || strings.TrimSpace(cfg.Providers.GitHubCopilot.ConnectMode) != "", "needs api_base or connect_mode")
	add("llamacpp", cfg.Providers.LlamaCpp.Enabled, "needs enabled=true and local model config")

	return result
}

func RuntimeSelection(cfg *config.Config) RuntimeSelectionStatus {
	status := RuntimeSelectionStatus{
		Provider:           NormalizeProviderName(cfg.Agents.Defaults.Provider),
		Model:              NormalizeModelName(cfg.Agents.Defaults.Model),
		BackgroundProvider: NormalizeProviderName(cfg.Background.Provider),
		BackgroundModel:    NormalizeModelName(cfg.Background.Model),
		LocalEnabled:       cfg.Providers.LlamaCpp.Enabled,
		LocalFallback:      cfg.Providers.LlamaCpp.Fallback,
		PrivacyEnabled:     cfg.Privacy.Enabled,
		AvailableProviders: AvailableProviders(cfg),
		Providers:          ProviderHealthMap(cfg),
	}

	if _, err := CreateProvider(cfg); err != nil {
		status.Valid = false
		status.Error = err.Error()
		return status
	}

	status.Valid = true
	return status
}
