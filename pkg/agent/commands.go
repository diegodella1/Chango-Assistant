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
	"llamacpp":   "gemma-4-E2B-it",
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
		if strings.EqualFold(al.cfg.Agents.Defaults.Provider, "llamacpp") {
			al.cfg.Providers.LlamaCpp.DefaultModel = newModel
		}
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
	newProvider = providers.NormalizeProviderName(newProvider)

	oldProvider := al.cfg.Agents.Defaults.Provider
	oldModel := al.model

	// Save old values for rollback
	savedProvider := al.cfg.Agents.Defaults.Provider
	savedModel := al.cfg.Agents.Defaults.Model

	// Update config for CreateProvider
	al.cfg.Agents.Defaults.Provider = newProvider

	// Set default model for the new provider
	newModel := oldModel
	if newProvider == "llamacpp" && strings.TrimSpace(al.cfg.Providers.LlamaCpp.DefaultModel) != "" {
		newModel = strings.TrimSpace(al.cfg.Providers.LlamaCpp.DefaultModel)
	} else if dm, ok := defaultProviderModels[newProvider]; ok {
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

// tryAutoRecovery attempts to switch to a working provider after consecutive failures.
// Cycles through known providers until one works.
func (al *AgentLoop) tryAutoRecovery() {
	currentProvider := al.cfg.Agents.Defaults.Provider
	fallbackOrder := []string{"openrouter", "groq", "deepseek", "openai", "anthropic", "gemini"}

	for _, candidate := range fallbackOrder {
		if candidate == currentProvider {
			continue
		}

		// Save for rollback
		savedProvider := al.cfg.Agents.Defaults.Provider
		savedModel := al.cfg.Agents.Defaults.Model

		al.cfg.Agents.Defaults.Provider = providers.NormalizeProviderName(candidate)
		if dm, ok := defaultProviderModels[candidate]; ok {
			al.cfg.Agents.Defaults.Model = dm
		}

		newProv, err := providers.CreateProvider(al.cfg)
		if err != nil {
			// Rollback and try next
			al.cfg.Agents.Defaults.Provider = savedProvider
			al.cfg.Agents.Defaults.Model = savedModel
			continue
		}

		// Success — switch in-memory only. Do NOT persist to disk:
		// the user's chosen provider/model in config.json should be respected.
		// Auto-recovery is temporary until the primary provider recovers.
		al.provider = newProv
		al.model = al.cfg.Agents.Defaults.Model
		al.contextBuilder.SetModel(al.model)
		al.consecutiveFails = 0

		logger.WarnCF("agent", "Auto-recovery: switched provider (in-memory only, config.json unchanged)", map[string]interface{}{
			"from_provider": currentProvider,
			"to_provider":   candidate,
			"to_model":      al.model,
		})
		return
	}

	logger.ErrorCF("agent", "Auto-recovery failed: no working provider found", nil)
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

	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 Estado del sistema\n\n")
	fmt.Fprintf(&sb, "Provider: %s\nModelo: %s\nTools: %d activos\nUptime: %s\n", provider, model, toolCount, uptimeStr)

	// Token usage today
	if al.tracker != nil {
		if bucket := al.tracker.GetToday(); bucket != nil {
			fmt.Fprintf(&sb, "\n📈 Tokens hoy: %d en %d llamadas", bucket.Totals.TotalTokens, bucket.Totals.Calls)
		} else {
			fmt.Fprintf(&sb, "\n📈 Tokens hoy: 0")
		}
	}

	// Token budget
	if al.tokenBudget != nil {
		used, bgUsed, limit, bgMax := al.tokenBudget.GetStatus()
		if limit > 0 {
			pct := float64(used) * 100 / float64(limit)
			fmt.Fprintf(&sb, "\n💰 Budget: %d/%d (%.0f%%)", used, limit, pct)
		}
		if bgMax > 0 {
			fmt.Fprintf(&sb, " | BG: %d/%d", bgUsed, bgMax)
		}
	}

	return sb.String(), true
}
