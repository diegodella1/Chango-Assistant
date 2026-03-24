package agent

import (
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// createToolRegistryResult holds the registry and direct tool references needed by AgentLoop.
type createToolRegistryResult struct {
	registry   *tools.ToolRegistry
	memoryTool *tools.MemoryTool
}

// createToolRegistry creates a tool registry with common tools.
// This is shared between main agent and subagents.
func createToolRegistry(workspace string, restrict bool, cfg *config.Config, msgBus *bus.MessageBus) createToolRegistryResult {
	registry := tools.NewToolRegistry()

	// File system tools
	registry.Register(tools.NewReadFileTool(workspace, restrict))
	registry.Register(tools.NewWriteFileTool(workspace, restrict))
	registry.Register(tools.NewListDirTool(workspace, restrict))
	registry.Register(tools.NewEditFileTool(workspace, restrict))
	registry.Register(tools.NewAppendFileTool(workspace, restrict))

	// Shell execution
	registry.Register(tools.NewExecTool(workspace, restrict))

	// Host execution via nsenter (requires --privileged --pid=host)
	registry.Register(tools.NewHostExecTool())

	if searchTool := tools.NewWebSearchTool(tools.WebSearchToolOptions{
		SerperAPIKey:         cfg.Tools.Web.Serper.APIKey,
		SerperMaxResults:     cfg.Tools.Web.Serper.MaxResults,
		SerperEnabled:        cfg.Tools.Web.Serper.Enabled,
		BraveAPIKey:          cfg.Tools.Web.Brave.APIKey,
		BraveMaxResults:      cfg.Tools.Web.Brave.MaxResults,
		BraveEnabled:         cfg.Tools.Web.Brave.Enabled,
		DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
		DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
	}); searchTool != nil {
		registry.Register(searchTool)
	}
	registry.Register(tools.NewWebFetchTool(50000))

	// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
	registry.Register(tools.NewI2CTool())
	registry.Register(tools.NewSPITool())

	// Memory - persistent notes (keep reference for programmatic access)
	memoryTool := tools.NewMemoryTool(workspace)
	registry.Register(memoryTool)

	// Image generation
	registry.Register(tools.NewImageGenTool())

	// YouTube transcript
	registry.Register(tools.NewYouTubeTool())

	// Weather
	registry.Register(tools.NewWeatherTool())

	// Reminder (needs message bus for notifications)
	reminderTool := tools.NewReminderTool(workspace, msgBus)
	reminderTool.StartPendingReminders()
	registry.Register(reminderTool)

	// Tasks - persistent task/goal tracking
	registry.Register(tools.NewTasksTool(workspace))

	// Snippets
	registry.Register(tools.NewSnippetTool(workspace))

	// Credentials vault (encrypted)
	registry.Register(tools.NewCredentialsTool(workspace, cfg.Tools.Credentials.MasterKey))

	// Smart lights (Magic Home WiFi)
	registry.Register(tools.NewLightsTool(workspace))

	// Self-modification (AGENTS.md editing)
	registry.Register(tools.NewSelfTool(workspace))

	// Translator
	registry.Register(tools.NewTranslateTool())

	// GitHub (gh CLI)
	registry.Register(tools.NewGithubTool())

	// HTTP request
	registry.Register(tools.NewHTTPRequestTool())

	// Structured web browsing (session-persistent, form extraction/submit)
	registry.Register(tools.NewBrowseTool())

	// Google Workspace tools (Gmail, Calendar, Drive)
	if cfg.Tools.Google.ServiceAccountFile != "" && cfg.Tools.Google.ImpersonateEmail != "" {
		saFile := cfg.Tools.Google.ServiceAccountFile
		email := cfg.Tools.Google.ImpersonateEmail
		if t := tools.NewGmailTool(saFile, email); t != nil {
			registry.Register(t)
		}
		if t := tools.NewCalendarTool(saFile, email); t != nil {
			registry.Register(t)
		}
		if t := tools.NewGDriveTool(saFile, email); t != nil {
			registry.Register(t)
		}
	}

	// Message tool - available to both agent and subagent
	// Subagent uses it to communicate directly with user
	messageTool := tools.NewMessageTool()
	messageTool.SetSendCallback(func(channel, chatID, content string) error {
		msgBus.PublishOutbound(bus.OutboundMessage{
			Channel: channel,
			ChatID:  chatID,
			Content: content,
		})
		return nil
	})
	registry.Register(messageTool)

	return createToolRegistryResult{registry: registry, memoryTool: memoryTool}
}
