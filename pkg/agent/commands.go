package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// defaultProviderModels maps provider names to their default model.
var defaultProviderModels = map[string]string{
	"openai":     "gpt-5",
	"openrouter": "anthropic/claude-sonnet-4",
	"groq":       "llama-3.3-70b-versatile",
	"anthropic":  "claude-sonnet-4-20250514",
	"deepseek":   "deepseek-chat",
	"gemini":     "gemini-2.5-flash",
}

// handleModelCommand handles the /model command to view or change the current model at runtime.
// Returns the response string and true if the command was handled.
func (al *AgentLoop) handleModelCommand(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)

	if trimmed == "/model" {
		return fmt.Sprintf("Current model: %s", al.model), true
	}

	if strings.HasPrefix(trimmed, "/model ") {
		newModel := strings.TrimSpace(strings.TrimPrefix(trimmed, "/model "))
		if newModel == "" {
			return fmt.Sprintf("Current model: %s", al.model), true
		}

		oldModel := al.model
		al.model = newModel
		al.contextBuilder.SetModel(newModel)

		// Update config and persist
		al.cfg.Agents.Defaults.Model = newModel
		if al.configPath != "" {
			if err := config.SaveConfig(al.configPath, al.cfg); err != nil {
				logger.WarnCF("agent", "Failed to persist model change",
					map[string]interface{}{"error": err.Error()})
				return fmt.Sprintf("Model changed: %s → %s (warning: failed to save config: %v)", oldModel, newModel, err), true
			}
		}

		logger.InfoCF("agent", "Model changed via /model command",
			map[string]interface{}{
				"old_model": oldModel,
				"new_model": newModel,
			})

		return fmt.Sprintf("Model changed: %s → %s", oldModel, newModel), true
	}

	return "", false
}

// handleProviderCommand handles the /provider command to view or change the current provider at runtime.
// Returns the response string and true if the command was handled.
func (al *AgentLoop) handleProviderCommand(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)

	if trimmed == "/provider" {
		return fmt.Sprintf("Current provider: %s (model: %s)", al.cfg.Agents.Defaults.Provider, al.model), true
	}

	if !strings.HasPrefix(trimmed, "/provider ") {
		return "", false
	}

	newProvider := strings.TrimSpace(strings.TrimPrefix(trimmed, "/provider "))
	if newProvider == "" {
		return fmt.Sprintf("Current provider: %s (model: %s)", al.cfg.Agents.Defaults.Provider, al.model), true
	}
	newProvider = strings.ToLower(newProvider)

	oldProvider := al.cfg.Agents.Defaults.Provider
	oldModel := al.model

	// Save old values for rollback
	savedProvider := al.cfg.Agents.Defaults.Provider
	savedModel := al.cfg.Agents.Defaults.Model

	// Update config for CreateProvider
	al.cfg.Agents.Defaults.Provider = newProvider

	// Set default model for the new provider
	newModel := oldModel
	if dm, ok := defaultProviderModels[newProvider]; ok {
		newModel = dm
	}
	al.cfg.Agents.Defaults.Model = newModel

	// Try creating the new provider
	newProv, err := providers.CreateProvider(al.cfg)
	if err != nil {
		// Rollback
		al.cfg.Agents.Defaults.Provider = savedProvider
		al.cfg.Agents.Defaults.Model = savedModel
		return fmt.Sprintf("Error al cambiar a %s: %v", newProvider, err), true
	}

	// Swap provider and model
	al.provider = newProv
	al.model = newModel
	al.contextBuilder.SetModel(newModel)

	// Propagate to subagent manager
	if al.subagentMgr != nil {
		al.subagentMgr.SetProvider(newProv)
		al.subagentMgr.SetDefaultModel(newModel)
	}

	// Persist config
	if al.configPath != "" {
		if err := config.SaveConfig(al.configPath, al.cfg); err != nil {
			logger.WarnCF("agent", "Failed to persist provider change",
				map[string]interface{}{"error": err.Error()})
			return fmt.Sprintf("Provider: %s → %s, Model: %s → %s (warning: no se pudo guardar config)", oldProvider, newProvider, oldModel, newModel), true
		}
	}

	logger.InfoCF("agent", "Provider changed via /provider command",
		map[string]interface{}{
			"old_provider": oldProvider,
			"new_provider": newProvider,
			"old_model":    oldModel,
			"new_model":    newModel,
		})

	return fmt.Sprintf("Provider: %s → %s\nModel: %s → %s", oldProvider, newProvider, oldModel, newModel), true
}

// handleStatusCommand handles the /status command to show system status.
func (al *AgentLoop) handleStatusCommand(content string) (string, bool) {
	if strings.TrimSpace(content) != "/status" {
		return "", false
	}

	provider := al.cfg.Agents.Defaults.Provider
	model := al.model
	toolCount := al.tools.Count()

	uptime := time.Since(al.startedAt)
	var uptimeStr string
	if h := int(uptime.Hours()); h > 24 {
		uptimeStr = fmt.Sprintf("%dd %dh", h/24, h%24)
	} else if h > 0 {
		uptimeStr = fmt.Sprintf("%dh %dm", h, int(uptime.Minutes())%60)
	} else {
		uptimeStr = fmt.Sprintf("%dm", int(uptime.Minutes()))
	}

	status := fmt.Sprintf("📊 Estado del sistema\n\nProvider: %s\nModelo: %s\nTools: %d activos\nUptime: %s",
		provider, model, toolCount, uptimeStr)

	return status, true
}
