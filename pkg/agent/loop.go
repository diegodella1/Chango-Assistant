// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/experiments"
	"github.com/sipeed/picoclaw/pkg/knowledge"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/telemetry"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/utils"
)

type AgentLoop struct {
	bus            *bus.MessageBus
	provider       providers.LLMProvider
	workspace      string
	model          string
	contextWindow  int // Maximum context window size in tokens
	maxIterations  int
	sessions       *session.SessionManager
	state          *state.Manager
	contextBuilder *ContextBuilder
	tools          *tools.ToolRegistry
	memoryTool     *tools.MemoryTool          // Direct reference for programmatic access (distillation, relevance search)
	knowledgeGraph *tools.KnowledgeGraphTool // Direct reference for analogical reasoning (graph search)
	running        atomic.Bool
	summarizing    sync.Map // Tracks which sessions are currently being summarized
	cfg            *config.Config // Reference to config for runtime updates
	configPath     string         // Path to config.json for persistence
	tracker        *telemetry.Tracker
	subagentMgr    *tools.SubagentManager
	scoring        *ScoringEngine
	localProvider  providers.LLMProvider // local model for inner monologue (zero cost, private)
	onEvent        func(string) // callback for real-time activity events (SSE)
	tokenBudget    *telemetry.TokenBudget
	scratchpad     *Scratchpad // per-session working memory (active thoughts)
}

// SetLocalProvider sets a local LLM provider for inner monologue (zero cost, private).
func (al *AgentLoop) SetLocalProvider(p providers.LLMProvider) {
	al.localProvider = p
}

// SetEventCallback sets a function called on agent activity events (think, tool, memory, etc.)
func (al *AgentLoop) SetEventCallback(fn func(string)) {
	al.onEvent = fn
}

func (al *AgentLoop) emitEvent(eventType string) {
	if al.onEvent != nil {
		al.onEvent(eventType)
	}
}

// processOptions configures how a message is processed
type processOptions struct {
	SessionKey      string   // Session identifier for history/context
	Channel         string   // Target channel for tool execution
	ChatID          string   // Target chat ID for tool execution
	UserMessage     string   // User message content (may include prefix)
	Media           []string // Media data URIs (images as base64 data URIs)
	DefaultResponse string   // Response when LLM returns empty
	EnableSummary   bool     // Whether to trigger summarization
	SendResponse    bool     // Whether to send response via bus
	NoHistory       bool     // If true, don't load session history (for heartbeat)
	Feature         string   // Telemetry feature label (chat, heartbeat, cron, summarize)
}


func NewAgentLoop(cfg *config.Config, msgBus *bus.MessageBus, provider providers.LLMProvider, configPath string) *AgentLoop {
	workspace := cfg.WorkspacePath()
	os.MkdirAll(workspace, 0755)

	restrict := cfg.Agents.Defaults.RestrictToWorkspace

	// Create tool registry for main agent
	toolsResult := createToolRegistry(workspace, restrict, cfg, msgBus)
	toolsRegistry := toolsResult.registry

	// Create subagent manager with its own tool registry
	subagentManager := tools.NewSubagentManager(provider, cfg.Agents.Defaults.Model, workspace, msgBus)
	subagentToolsResult := createToolRegistry(workspace, restrict, cfg, msgBus)
	// Subagent doesn't need spawn/subagent tools to avoid recursion
	subagentManager.SetTools(subagentToolsResult.registry)

	// Register spawn tool (for main agent)
	spawnTool := tools.NewSpawnTool(subagentManager)
	toolsRegistry.Register(spawnTool)

	// Register subagent tool (synchronous execution)
	subagentTool := tools.NewSubagentTool(subagentManager)
	toolsRegistry.Register(subagentTool)

	sessionsManager := session.NewSessionManager(filepath.Join(workspace, "sessions"))

	// Create state manager for atomic state persistence
	stateManager := state.NewManager(workspace)

	// Create knowledge loader and experiments store
	knowledgeLoader := knowledge.NewLoader(workspace)
	experimentsStore := experiments.NewStore(workspace)

	// Register learn tool (needs knowledge loader and subagent manager)
	learnTool := tools.NewLearnTool(workspace, knowledgeLoader, subagentManager)
	toolsRegistry.Register(learnTool)

	// Create context builder and set tools registry
	contextBuilder := NewContextBuilder(workspace)
	contextBuilder.SetToolsRegistry(toolsRegistry)
	contextBuilder.SetModel(cfg.Agents.Defaults.Model)
	contextBuilder.SetKnowledgeLoader(knowledgeLoader)
	contextBuilder.SetExperiments(experimentsStore)
	contextBuilder.SetMemoryTool(toolsResult.memoryTool)

	// Wire system prompt builder so subagents inherit the main agent's personality
	subagentManager.SetSystemPromptBuilder(func() string { return contextBuilder.BuildSystemPrompt() })

	// Initialize token budget with config values or defaults
	dailyLimit := cfg.TokenBudget.DailyLimit
	if dailyLimit == 0 {
		dailyLimit = 200000
	}
	bgMax := cfg.TokenBudget.BackgroundMax
	if bgMax == 0 {
		bgMax = 50000
	}
	tokenBudget := telemetry.NewTokenBudget(workspace, dailyLimit, bgMax)

	return &AgentLoop{
		bus:            msgBus,
		provider:       provider,
		workspace:      workspace,
		model:          cfg.Agents.Defaults.Model,
		contextWindow:  cfg.Agents.Defaults.MaxTokens, // Restore context window for summarization
		maxIterations:  cfg.Agents.Defaults.MaxToolIterations,
		sessions:       sessionsManager,
		state:          stateManager,
		contextBuilder: contextBuilder,
		tools:          toolsRegistry,
		memoryTool:     toolsResult.memoryTool,
		knowledgeGraph: toolsResult.knowledgeGraph,
		summarizing:    sync.Map{},
		cfg:            cfg,
		configPath:     configPath,
		subagentMgr:    subagentManager,
		scoring:        NewScoringEngine(workspace),
		tokenBudget:    tokenBudget,
		scratchpad:     NewScratchpad(),
	}
}

func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)

	for al.running.Load() {
		select {
		case <-ctx.Done():
			return nil
		default:
			msg, ok := al.bus.ConsumeInbound(ctx)
			if !ok {
				continue
			}

			response, media, err := al.processMessage(ctx, msg)
			if err != nil {
				response = fmt.Sprintf("Error processing message: %v", err)
				media = nil
			}

			if response != "" {
				// Check if the message tool already sent a response during this round.
				// If so, skip publishing to avoid duplicate messages to the user.
				alreadySent := false
				if tool, ok := al.tools.Get("message"); ok {
					if mt, ok := tool.(*tools.MessageTool); ok {
						alreadySent = mt.HasSentInRound()
					}
				}

				if !alreadySent {
					al.bus.PublishOutbound(bus.OutboundMessage{
						Channel: msg.Channel,
						ChatID:  msg.ChatID,
						Content: response,
						Media:   media,
					})
				}
			}
		}
	}

	return nil
}

func (al *AgentLoop) Stop() {
	al.running.Store(false)
}

func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	al.tools.Register(tool)
}

// SetTracker sets the telemetry tracker for recording token usage.
func (al *AgentLoop) SetTracker(t *telemetry.Tracker) {
	al.tracker = t
}

// RecordLastChannel records the last active channel for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChannel(channel string) error {
	return al.state.SetLastChannel(channel)
}

// RecordLastChatID records the last active chat ID for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChatID(chatID string) error {
	return al.state.SetLastChatID(chatID)
}

func (al *AgentLoop) ProcessDirect(ctx context.Context, content, sessionKey string) (string, error) {
	return al.ProcessDirectWithChannel(ctx, content, sessionKey, "cli", "direct")
}

func (al *AgentLoop) ProcessDirectWithChannel(ctx context.Context, content, sessionKey, channel, chatID string) (string, error) {
	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   "cron",
		ChatID:     chatID,
		Content:    content,
		SessionKey: sessionKey,
	}

	response, _, err := al.processMessage(ctx, msg)
	return response, err
}

// ProcessHeartbeat processes a heartbeat request without session history.
// Each heartbeat is independent and doesn't accumulate context.
// If the heartbeat sends a proactive message to the user, that message is
// injected into the user's real session so follow-up conversations have context.
func (al *AgentLoop) ProcessHeartbeat(ctx context.Context, content, channel, chatID string) (string, error) {
	response, _, err := al.runAgentLoop(ctx, processOptions{
		SessionKey:      "heartbeat",
		Channel:         channel,
		ChatID:          chatID,
		UserMessage:     content,
		DefaultResponse: "I've completed processing but have no response to give.",
		EnableSummary:   false,
		SendResponse:    false,
		NoHistory:       true, // Don't load session history for heartbeat
		Feature:         telemetry.FeatureHeartbeat,
	})

	// If the heartbeat sent a message to the user, inject it into the real session
	// so follow-up conversations have context about what was said.
	if tool, ok := al.tools.Get("message"); ok {
		if mt, ok := tool.(*tools.MessageTool); ok && mt.HasSentInRound() {
			if sent := mt.LastSentContent(); sent != "" {
				realSession := fmt.Sprintf("%s:%s", channel, chatID)
				al.sessions.AddMessage(realSession, "assistant", sent)
				al.sessions.Save(realSession)
				logger.DebugCF("agent", "Heartbeat message injected into real session",
					map[string]interface{}{
						"session": realSession,
						"length":  len(sent),
					})
			}
		}
	}

	// Clear heartbeat session to prevent unbounded growth
	// (NoHistory=true means we never load it, so messages just accumulate)
	al.sessions.TruncateHistory("heartbeat", 0)

	return response, err
}

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, []string, error) {
	// Add message preview to log (show full content for error messages)
	var logContent string
	if strings.Contains(msg.Content, "Error:") || strings.Contains(msg.Content, "error") {
		logContent = msg.Content // Full content for errors
	} else {
		logContent = utils.Truncate(msg.Content, 80)
	}
	logger.InfoCF("agent", fmt.Sprintf("Processing message from %s:%s: %s", msg.Channel, msg.SenderID, logContent),
		map[string]interface{}{
			"channel":     msg.Channel,
			"chat_id":     msg.ChatID,
			"sender_id":   msg.SenderID,
			"session_key": msg.SessionKey,
		})

	// Route system messages to processSystemMessage
	if msg.Channel == "system" {
		resp, err := al.processSystemMessage(ctx, msg)
		return resp, nil, err
	}

	// Handle /provider command
	if response, handled := al.handleProviderCommand(msg.Content); handled {
		return response, nil, nil
	}

	// Handle /model command
	if response, handled := al.handleModelCommand(msg.Content); handled {
		return response, nil, nil
	}

	// Detect feature: cron jobs have SenderID "cron"
	feature := telemetry.FeatureChat
	if msg.SenderID == "cron" {
		feature = telemetry.FeatureCron
	}

	// Process as user message
	return al.runAgentLoop(ctx, processOptions{
		SessionKey:      msg.SessionKey,
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		UserMessage:     msg.Content,
		Media:           msg.Media,
		DefaultResponse: "I've completed processing but have no response to give.",
		EnableSummary:   true,
		SendResponse:    false,
		Feature:         feature,
	})
}

func (al *AgentLoop) processSystemMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	// Verify this is a system message
	if msg.Channel != "system" {
		return "", fmt.Errorf("processSystemMessage called with non-system message channel: %s", msg.Channel)
	}

	logger.InfoCF("agent", "Processing system message",
		map[string]interface{}{
			"sender_id": msg.SenderID,
			"chat_id":   msg.ChatID,
		})

	// Parse origin channel from chat_id (format: "channel:chat_id")
	var originChannel string
	if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
		originChannel = msg.ChatID[:idx]
	} else {
		// Fallback
		originChannel = "cli"
	}

	// Extract subagent result from message content
	// Format: "Task 'label' completed.\n\nResult:\n<actual content>"
	content := msg.Content
	if idx := strings.Index(content, "Result:\n"); idx >= 0 {
		content = content[idx+8:] // Extract just the result part
	}

	// Skip internal channels - only log, don't send to user
	if constants.IsInternalChannel(originChannel) {
		logger.InfoCF("agent", "Subagent completed (internal channel)",
			map[string]interface{}{
				"sender_id":   msg.SenderID,
				"content_len": len(content),
				"channel":     originChannel,
			})
		return "", nil
	}

	// Agent acts as dispatcher only - subagent handles user interaction via message tool
	// Don't forward result here, subagent should use message tool to communicate with user
	logger.InfoCF("agent", "Subagent completed",
		map[string]interface{}{
			"sender_id":   msg.SenderID,
			"channel":     originChannel,
			"content_len": len(content),
		})

	// Agent only logs, does not respond to user
	return "", nil
}


// runAgentLoop is the core message processing logic.
// It handles context building, LLM calls, tool execution, and response handling.
func (al *AgentLoop) runAgentLoop(ctx context.Context, opts processOptions) (string, []string, error) {
	// 0. Record last channel for heartbeat notifications (skip internal channels)
	if opts.Channel != "" && opts.ChatID != "" {
		// Don't record internal channels (cli, system, subagent)
		if !constants.IsInternalChannel(opts.Channel) {
			channelKey := fmt.Sprintf("%s:%s", opts.Channel, opts.ChatID)
			if err := al.RecordLastChannel(channelKey); err != nil {
				logger.WarnCF("agent", "Failed to record last channel: %v", map[string]interface{}{"error": err.Error()})
			}
		}
	}

	// 1. Update tool contexts
	al.updateToolContexts(opts.Channel, opts.ChatID)

	// 2. Build messages (skip history for heartbeat)
	var history []providers.Message
	var summary string
	if !opts.NoHistory {
		history = al.sessions.GetHistory(opts.SessionKey)
		summary = al.sessions.GetSummary(opts.SessionKey)
	}
	messages := al.contextBuilder.BuildMessages(
		history,
		summary,
		opts.UserMessage,
		opts.Media,
		opts.Channel,
		opts.ChatID,
	)

	// 2b. Working memory scratchpad — inject active thoughts for this session
	if !opts.NoHistory {
		scratchpadText := al.scratchpad.Format(opts.SessionKey)
		if scratchpadText != "" {
			scratchpadMsg := providers.Message{
				Role:    "system",
				Content: scratchpadText,
			}
			// Insert before the last message (user message)
			messages = append(messages[:len(messages)-1], scratchpadMsg, messages[len(messages)-1])
		}
	}

	// 2c. Topic shift detection — inject hint if user changed subject
	if len(history) >= 3 && opts.UserMessage != "" {
		if detectTopicShift(opts.UserMessage, history) {
			// Insert a system hint just before the user message to signal the LLM
			topicHint := providers.Message{
				Role:    "system",
				Content: "⚡ The user has shifted to a new topic. Focus your response on the new subject. Do not carry over context from the previous topic unless explicitly relevant.",
			}
			messages = append(messages[:len(messages)-1], topicHint, messages[len(messages)-1])
			logger.DebugCF("agent", "Topic shift detected", map[string]interface{}{
				"session": opts.SessionKey,
			})
		}
	}

	// 2d. Inner monologue — plan before responding (System 2 thinking)
	// Skip for heartbeat, cron, short messages, and /commands
	var monologueResult string
	if !opts.NoHistory && len(opts.UserMessage) > 20 && !strings.HasPrefix(opts.UserMessage, "/") && opts.Feature == telemetry.FeatureChat {
		monologueResult = al.innerMonologue(ctx, opts.UserMessage, history)
		if monologueResult != "" {
			// Inject the monologue as a system message just before the user message
			// This guides the LLM's response without being visible to the user
			hint := providers.Message{
				Role:    "system",
				Content: "## Internal Analysis (not visible to user)\n\n" + monologueResult,
			}
			// Insert before the last message (which is the user message)
			messages = append(messages[:len(messages)-1], hint, messages[len(messages)-1])
		}
	}

	// 2e. Analogical reasoning — search KG and memory for similar past experiences
	// Skip for heartbeat, cron, and /commands
	if !opts.NoHistory && opts.UserMessage != "" && !strings.HasPrefix(opts.UserMessage, "/") {
		analogies := al.findAnalogies(opts.UserMessage)
		if analogies != "" {
			analogyMsg := providers.Message{
				Role:    "system",
				Content: analogies,
			}
			// Insert before the last message (which is the user message)
			messages = append(messages[:len(messages)-1], analogyMsg, messages[len(messages)-1])
			logger.DebugCF("agent", "Analogical reasoning injected", map[string]interface{}{
				"session": opts.SessionKey,
				"length":  len(analogies),
			})
		}
	}

	// 3. Save user message to session
	al.sessions.AddMessage(opts.SessionKey, "user", opts.UserMessage)

	// 4. Run LLM iteration loop (or Tree of Thought for complex decisions)
	var finalContent string
	var iteration int
	var media []string
	var err error

	// Background token optimization: prefer local model for cron/heartbeat
	useLocal := al.localProvider != nil && al.cfg.Background.PreferLocal &&
		(opts.Feature == telemetry.FeatureCron || opts.Feature == telemetry.FeatureHeartbeat)
	var origProvider providers.LLMProvider
	var origModel string
	if useLocal {
		origProvider = al.provider
		origModel = al.model
		al.provider = al.localProvider
		al.model = ""
		logger.DebugCF("agent", "Using local provider for background task", map[string]interface{}{
			"feature": opts.Feature,
		})
	}

	// 4a. Tree of Thought for complex decisions (chat only)
	totUsed := false
	if opts.Feature == telemetry.FeatureChat && !opts.NoHistory && shouldUseToT(opts.UserMessage, monologueResult) {
		totResponse, totErr := al.treeOfThought(ctx, messages, opts)
		if totErr == nil && totResponse != "" {
			finalContent = totResponse
			totUsed = true
			logger.InfoCF("agent", "Tree of Thought produced response", map[string]interface{}{
				"chars": len(finalContent),
			})
		}
	}

	// 4b. Normal LLM iteration (if ToT was not used or failed)
	if !totUsed {
		finalContent, iteration, media, err = al.runLLMIteration(ctx, messages, opts)
		if err != nil {
			// If local provider failed, escalate to cloud
			if useLocal {
				logger.InfoCF("agent", "Local provider failed, escalating to cloud", map[string]interface{}{
					"error": err.Error(),
				})
				al.provider = origProvider
				al.model = origModel
				useLocal = false // prevent double restore below
				finalContent, iteration, media, err = al.runLLMIteration(ctx, messages, opts)
			}
			if err != nil {
				return "", nil, err
			}
		}
	}

	// Restore original provider if we swapped to local
	if useLocal {
		// Check if local response is too short or low quality — escalate to cloud
		needsEscalation := len(strings.TrimSpace(finalContent)) < 20 ||
			strings.Contains(strings.ToLower(finalContent), "i don't know") ||
			strings.Contains(strings.ToLower(finalContent), "no puedo")
		al.provider = origProvider
		al.model = origModel
		if needsEscalation {
			logger.InfoCF("agent", "Local response too short/uncertain, escalating to cloud", map[string]interface{}{
				"local_response_len": len(finalContent),
			})
			finalContent, iteration, media, err = al.runLLMIteration(ctx, messages, opts)
			if err != nil {
				return "", nil, err
			}
		}
	}

	// 4c. Self-critique: catch low-quality responses before sending (chat only)
	if opts.Feature == telemetry.FeatureChat && !opts.NoHistory && finalContent != "" {
		if critiqueHint := al.selfCritique(ctx, opts.UserMessage, finalContent); critiqueHint != "" {
			logger.InfoCF("agent", "Self-critique triggered revision", map[string]interface{}{
				"hint": critiqueHint,
			})
			// Re-run with the critique as guidance
			critiqueMsg := providers.Message{
				Role:    "system",
				Content: "REVISION NEEDED: " + critiqueHint + "\nRewrite your response addressing this feedback.",
			}
			reviseMsgs := make([]providers.Message, len(messages))
			copy(reviseMsgs, messages)
			reviseMsgs = append(reviseMsgs,
				providers.Message{Role: "assistant", Content: finalContent},
				critiqueMsg,
			)
			revised, _, _, revErr := al.runLLMIteration(ctx, reviseMsgs, opts)
			if revErr == nil && revised != "" {
				finalContent = revised
			}
		}
	}

	// If last tool had ForUser content and we already sent it, we might not need to send final response
	// This is controlled by the tool's Silent flag and ForUser content

	// 5. Handle empty response
	if finalContent == "" {
		finalContent = opts.DefaultResponse
	}

	// 6. Save final assistant message to session
	al.sessions.AddMessage(opts.SessionKey, "assistant", finalContent)
	al.sessions.Save(opts.SessionKey)

	// 6b. Update working memory scratchpad with this exchange
	if !opts.NoHistory {
		al.scratchpad.Update(opts.SessionKey, opts.UserMessage, finalContent)
	}

	// 7. Optional: summarization
	if opts.EnableSummary {
		al.maybeSummarize(opts.SessionKey)
	}

	// 8. Optional: send response via bus
	if opts.SendResponse {
		al.bus.PublishOutbound(bus.OutboundMessage{
			Channel: opts.Channel,
			ChatID:  opts.ChatID,
			Content: finalContent,
			Media:   media,
		})
	}

	// 9. Log response
	responsePreview := utils.Truncate(finalContent, 120)
	logger.InfoCF("agent", fmt.Sprintf("Response: %s", responsePreview),
		map[string]interface{}{
			"session_key":  opts.SessionKey,
			"iterations":   iteration,
			"final_length": len(finalContent),
		})

	// 10. Score interaction quality (non-blocking)
	if al.scoring != nil && !opts.NoHistory {
		sessionHistory := al.sessions.GetHistory(opts.SessionKey)
		go al.scoring.Score(opts.SessionKey, sessionHistory)
	}

	return finalContent, media, nil
}

// runLLMIteration executes the LLM call loop with tool handling.
// Returns the final content, iteration count, collected media URLs, and any error.
func (al *AgentLoop) runLLMIteration(ctx context.Context, messages []providers.Message, opts processOptions) (string, int, []string, error) {
	iteration := 0
	var finalContent string
	var collectedMedia []string

	for iteration < al.maxIterations {
		iteration++

		logger.DebugCF("agent", "LLM iteration",
			map[string]interface{}{
				"iteration": iteration,
				"max":       al.maxIterations,
			})

		// Build tool definitions
		providerToolDefs := al.tools.ToProviderDefs()

		// Log LLM request details
		logger.DebugCF("agent", "LLM request",
			map[string]interface{}{
				"iteration":         iteration,
				"model":             al.model,
				"messages_count":    len(messages),
				"tools_count":       len(providerToolDefs),
				"max_tokens":        constants.DefaultMaxTokens,
				"temperature":       constants.DefaultTemperature,
				"system_prompt_len": len(messages[0].Content),
			})

		// Log full messages (detailed) — only build strings if debug level is active
		if logger.GetLevel() <= logger.DEBUG {
			logger.DebugCF("agent", "Full LLM request",
				map[string]interface{}{
					"iteration":     iteration,
					"messages_json": formatMessagesForLog(messages),
					"tools_json":    formatToolsForLog(providerToolDefs),
				})
		}

		// Call LLM
		al.emitEvent("think")
		response, err := al.provider.Chat(ctx, messages, providerToolDefs, al.model, map[string]interface{}{
			"max_tokens":  constants.DefaultMaxTokens,
			"temperature": constants.DefaultTemperature,
		})

		// Record token usage
		if response != nil && response.Usage != nil && al.tracker != nil {
			al.tracker.Record(opts.Feature, response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.TotalTokens)
		}

		if err != nil {
			logger.ErrorCF("agent", "LLM call failed",
				map[string]interface{}{
					"iteration": iteration,
					"error":     err.Error(),
				})
			// Return a user-friendly error message instead of raw error
			errMsg := err.Error()
			var userMsg string
			switch {
			case strings.Contains(errMsg, "429") || strings.Contains(errMsg, "rate"):
				userMsg = "La API está saturada (rate limit). Intentá de nuevo en unos segundos."
			case strings.Contains(errMsg, "500") || strings.Contains(errMsg, "502") || strings.Contains(errMsg, "503"):
				userMsg = "El servidor de la API está con problemas. Intentá de nuevo en un rato."
			case strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "deadline"):
				userMsg = "La API tardó demasiado en responder (timeout). Intentá con un mensaje más corto o cambiá de modelo con /model."
			case strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403"):
				userMsg = "Error de autenticación con la API. Revisá la API key en config.json."
			default:
				userMsg = fmt.Sprintf("Error al comunicarme con la API: %s", errMsg)
			}
			return userMsg, iteration, nil, fmt.Errorf("LLM call failed: %w", err)
		}

		// Check if no tool calls - we're done
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
				map[string]interface{}{
					"iteration":      iteration,
					"content_chars":  len(finalContent),
					"media_count":    len(collectedMedia),
				})
			break
		}

		// Log tool calls
		toolNames := make([]string, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("agent", "LLM requested tool calls",
			map[string]interface{}{
				"tools":     toolNames,
				"count":     len(response.ToolCalls),
				"iteration": iteration,
			})

		// Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:    "assistant",
			Content: response.Content,
		}
		for _, tc := range response.ToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: &providers.FunctionCall{
					Name:      tc.Name,
					Arguments: string(argumentsJSON),
				},
			})
		}
		messages = append(messages, assistantMsg)

		// Save assistant message with tool calls to session
		al.sessions.AddFullMessage(opts.SessionKey, assistantMsg)

		// Execute tool calls
		for _, tc := range response.ToolCalls {
			// Log tool call with arguments preview
			argsJSON, _ := json.Marshal(tc.Arguments)
			argsPreview := utils.Truncate(string(argsJSON), 200)
			logger.InfoCF("agent", fmt.Sprintf("Tool call: %s(%s)", tc.Name, argsPreview),
				map[string]interface{}{
					"tool":      tc.Name,
					"iteration": iteration,
				})

			// Create async callback for tools that implement AsyncTool
			// NOTE: Following openclaw's design, async tools do NOT send results directly to users.
			// Instead, they notify the agent via PublishInbound, and the agent decides
			// whether to forward the result to the user (in processSystemMessage).
			asyncCallback := func(callbackCtx context.Context, result *tools.ToolResult) {
				// Log the async completion but don't send directly to user
				// The agent will handle user notification via processSystemMessage
				if !result.Silent && result.ForUser != "" {
					logger.InfoCF("agent", "Async tool completed, agent will handle notification",
						map[string]interface{}{
							"tool":        tc.Name,
							"content_len": len(result.ForUser),
						})
				}
			}

			// Emit activity event based on tool type
			switch tc.Name {
			case "memory":
				al.emitEvent("memory")
			case "browse", "web_search", "web_fetch":
				al.emitEvent("browse")
			case "learn":
				al.emitEvent("learn")
			default:
				al.emitEvent("tool")
			}
			toolResult := al.tools.ExecuteWithContext(ctx, tc.Name, tc.Arguments, opts.Channel, opts.ChatID, asyncCallback)

			// Collect media URLs from tool results
			if len(toolResult.Media) > 0 {
				collectedMedia = append(collectedMedia, toolResult.Media...)
			}

			// Send ForUser content to user immediately if not Silent
			if !toolResult.Silent && toolResult.ForUser != "" && opts.SendResponse {
				al.bus.PublishOutbound(bus.OutboundMessage{
					Channel: opts.Channel,
					ChatID:  opts.ChatID,
					Content: toolResult.ForUser,
				})
				logger.DebugCF("agent", "Sent tool result to user",
					map[string]interface{}{
						"tool":        tc.Name,
						"content_len": len(toolResult.ForUser),
					})
			}

			// Determine content for LLM based on tool result
			contentForLLM := toolResult.ForLLM
			if contentForLLM == "" && toolResult.Err != nil {
				contentForLLM = toolResult.Err.Error()
			}

			toolResultMsg := providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolResultMsg)

			// Save tool result message to session
			al.sessions.AddFullMessage(opts.SessionKey, toolResultMsg)
		}
	}

	return finalContent, iteration, collectedMedia, nil
}

// updateToolContexts updates the context for tools that need channel/chatID info.
func (al *AgentLoop) updateToolContexts(channel, chatID string) {
	// Use ContextualTool interface instead of type assertions
	if tool, ok := al.tools.Get("message"); ok {
		if mt, ok := tool.(tools.ContextualTool); ok {
			mt.SetContext(channel, chatID)
		}
	}
	if tool, ok := al.tools.Get("spawn"); ok {
		if st, ok := tool.(tools.ContextualTool); ok {
			st.SetContext(channel, chatID)
		}
	}
	if tool, ok := al.tools.Get("subagent"); ok {
		if st, ok := tool.(tools.ContextualTool); ok {
			st.SetContext(channel, chatID)
		}
	}
	if tool, ok := al.tools.Get("reminder"); ok {
		if rt, ok := tool.(tools.ContextualTool); ok {
			rt.SetContext(channel, chatID)
		}
	}
}


// GetStartupInfo returns information about loaded tools and skills for logging.
func (al *AgentLoop) GetStartupInfo() map[string]interface{} {
	info := make(map[string]interface{})

	// Tools info
	tools := al.tools.List()
	info["tools"] = map[string]interface{}{
		"count": len(tools),
		"names": tools,
	}

	// Skills info
	info["skills"] = al.contextBuilder.GetSkillsInfo()

	return info
}







