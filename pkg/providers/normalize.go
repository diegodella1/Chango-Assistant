package providers

import "strings"

// NormalizeProviderName trims user input and maps aliases to canonical provider ids.
func NormalizeProviderName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "gpt":
		return "openai"
	case "claude":
		return "anthropic"
	case "google":
		return "gemini"
	case "glm":
		return "zhipu"
	case "llama", "local", "qwen":
		return "llamacpp"
	case "claudecode", "claude-code":
		return "claude-cli"
	case "copilot":
		return "github_copilot"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

// NormalizeModelName trims surrounding whitespace from model names.
func NormalizeModelName(model string) string {
	return strings.TrimSpace(model)
}
