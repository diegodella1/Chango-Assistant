package providers

import "strings"

type CapabilitySupport string

const (
	CapabilitySupported   CapabilitySupport = "supported"
	CapabilityUnsupported CapabilitySupport = "unsupported"
	CapabilityUnknown     CapabilitySupport = "unknown"
)

type ModelCapabilities struct {
	Provider   string
	Model      string
	Vision     CapabilitySupport
	Tools      CapabilitySupport
	AudioInput CapabilitySupport
}

type capabilityReporter interface {
	capabilities(model string) ModelCapabilities
}

func ResolveCapabilities(provider LLMProvider, model string) ModelCapabilities {
	if provider == nil {
		return ModelCapabilities{
			Provider: "unknown",
			Model:    model,
			Vision:   CapabilityUnknown,
			Tools:    CapabilityUnknown,
		}
	}
	if reporter, ok := provider.(capabilityReporter); ok {
		return reporter.capabilities(model)
	}
	return inferCapabilities(providerName(provider), normalizeCapabilityModel(provider, model))
}

func HasVisionInput(messages []Message) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Type == "image_url" && part.ImageURL != nil && part.ImageURL.URL != "" {
				return true
			}
		}
	}
	return false
}

func VisionUnsupportedNotice(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "Recibi una imagen, pero el modelo actual no soporta vision. Cambia a un modelo con vision o desactiva la ruta local para media."
	}
	return "Recibi una imagen, pero el modelo actual (" + model + ") no soporta vision. Cambia a un modelo con vision o desactiva la ruta local para media."
}

func (p *HTTPProvider) capabilities(model string) ModelCapabilities {
	return inferCapabilities("http", normalizeCapabilityModel(p, model))
}

func (p *CodexProvider) capabilities(model string) ModelCapabilities {
	return inferCapabilities("codex", normalizeCapabilityModel(p, model))
}

func (p *ClaudeProvider) capabilities(model string) ModelCapabilities {
	return inferCapabilities("anthropic", normalizeCapabilityModel(p, model))
}

func (p *ClaudeCliProvider) capabilities(model string) ModelCapabilities {
	return inferCapabilities("claude-cli", normalizeCapabilityModel(p, model))
}

func (p *GitHubCopilotProvider) capabilities(model string) ModelCapabilities {
	return inferCapabilities("github_copilot", normalizeCapabilityModel(p, model))
}

func (p *LlamaCppProvider) capabilities(model string) ModelCapabilities {
	return ModelCapabilities{
		Provider:   "llamacpp",
		Model:      normalizeCapabilityModel(p, model),
		Vision:     CapabilityUnsupported,
		Tools:      CapabilityUnsupported,
		AudioInput: CapabilityUnsupported,
	}
}

func (f *FallbackProvider) capabilities(model string) ModelCapabilities {
	caps := ResolveCapabilities(f.Primary, model)
	caps.Provider = "fallback:" + caps.Provider
	return caps
}

func (pr *PrivacyRouter) capabilities(model string) ModelCapabilities {
	return ModelCapabilities{
		Provider: "privacy_router",
		Model:    normalizeCapabilityModel(pr.cloud, model),
		Vision:   CapabilityUnknown,
		Tools:    CapabilityUnknown,
	}
}

func providerName(provider LLMProvider) string {
	switch provider.(type) {
	case *CodexProvider:
		return "codex"
	case *ClaudeProvider:
		return "anthropic"
	case *ClaudeCliProvider:
		return "claude-cli"
	case *GitHubCopilotProvider:
		return "github_copilot"
	case *HTTPProvider:
		return "http"
	case *LlamaCppProvider:
		return "llamacpp"
	case *FallbackProvider:
		return "fallback"
	case *PrivacyRouter:
		return "privacy_router"
	default:
		return "unknown"
	}
}

func normalizeCapabilityModel(provider LLMProvider, model string) string {
	if strings.TrimSpace(model) != "" {
		return model
	}
	if provider == nil {
		return ""
	}
	return provider.GetDefaultModel()
}

func inferCapabilities(provider, model string) ModelCapabilities {
	caps := ModelCapabilities{
		Provider:   provider,
		Model:      model,
		Vision:     CapabilityUnknown,
		Tools:      CapabilitySupported,
		AudioInput: CapabilityUnknown,
	}

	lower := strings.ToLower(strings.TrimSpace(model))
	if lower == "" {
		return caps
	}

	switch {
	case strings.Contains(lower, "gpt-4o"),
		strings.Contains(lower, "gpt-4.1"),
		strings.Contains(lower, "o4-mini"),
		strings.Contains(lower, "gemini"),
		strings.Contains(lower, "claude-3"),
		strings.Contains(lower, "claude-4"),
		strings.Contains(lower, "claude-sonnet"),
		strings.Contains(lower, "claude-opus"),
		strings.Contains(lower, "claude-haiku"),
		strings.Contains(lower, "pixtral"),
		strings.Contains(lower, "llava"),
		strings.Contains(lower, "qwen-vl"),
		strings.Contains(lower, "qwen2-vl"),
		strings.Contains(lower, "internvl"),
		strings.Contains(lower, "minicpm-v"),
		strings.Contains(lower, "kimi-vl"):
		caps.Vision = CapabilitySupported
	case strings.Contains(lower, "gpt-3.5"),
		strings.Contains(lower, "claude-2"),
		strings.Contains(lower, "gemma-4-e2b"),
		strings.Contains(lower, "gemma-4-e4b"),
		strings.Contains(lower, "llama-2"):
		caps.Vision = CapabilityUnsupported
	}

	return caps
}
