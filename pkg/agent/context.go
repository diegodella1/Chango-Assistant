package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/experiments"
	"github.com/sipeed/picoclaw/pkg/knowledge"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type ContextBuilder struct {
	workspace       string
	skillsLoader    *skills.SkillsLoader
	memory          *MemoryStore
	memoryTool      *tools.MemoryTool // For relevance-based memory search (TF-IDF)
	tools           *tools.ToolRegistry // Direct reference to tool registry
	model           string
	knowledgeLoader *knowledge.Loader
	experiments     *experiments.Store
}

func getGlobalConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".picoclaw")
}

func NewContextBuilder(workspace string) *ContextBuilder {
	// builtin skills: skills directory in current project
	// Use the skills/ directory under the current working directory
	wd, _ := os.Getwd()
	builtinSkillsDir := filepath.Join(wd, "skills")
	globalSkillsDir := filepath.Join(getGlobalConfigDir(), "skills")

	return &ContextBuilder{
		workspace:    workspace,
		skillsLoader: skills.NewSkillsLoader(workspace, globalSkillsDir, builtinSkillsDir),
		memory:       NewMemoryStore(workspace),
	}
}

// SetToolsRegistry sets the tools registry for dynamic tool summary generation.
func (cb *ContextBuilder) SetToolsRegistry(registry *tools.ToolRegistry) {
	cb.tools = registry
}

// SetModel sets the current model name for inclusion in the system prompt.
func (cb *ContextBuilder) SetModel(model string) {
	cb.model = model
}

// SetKnowledgeLoader sets the knowledge loader for context injection.
func (cb *ContextBuilder) SetKnowledgeLoader(loader *knowledge.Loader) {
	cb.knowledgeLoader = loader
}

// SetExperiments sets the experiments store for behavioral adjustments.
func (cb *ContextBuilder) SetExperiments(store *experiments.Store) {
	cb.experiments = store
}

// SetMemoryTool sets the memory tool for relevance-based memory injection.
func (cb *ContextBuilder) SetMemoryTool(mt *tools.MemoryTool) {
	cb.memoryTool = mt
}

func (cb *ContextBuilder) getIdentity() string {
	now := time.Now().Format("2006-01-02 15:04 (Monday)")
	workspacePath, _ := filepath.Abs(filepath.Join(cb.workspace))
	runtime := fmt.Sprintf("%s %s, Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version())

	// Build tools section dynamically
	toolsSection := cb.buildToolsSection()

	return fmt.Sprintf(`# Chango — Autonomous AI Agent

You are Chango, an autonomous AI agent created by Diego Dell Agostino, running on a Raspberry Pi 5 in Buenos Aires, Argentina. You are NOT a generic assistant. Your full identity, accounts, and capabilities are defined in AGENTS.md and IDENTITY.md which follow below — read them carefully and NEVER contradict them.

## Current Time
%s

## Model
%s

## Runtime
%s

## Workspace
Your workspace is at: %s
- Memory: %s/memory/MEMORY.md
- Daily Notes: %s/memory/YYYYMM/YYYYMMDD.md
- Skills: %s/skills/{skill-name}/SKILL.md

%s

## Important Rules

1. **ALWAYS use tools** - When you need to perform an action (schedule reminders, send messages, execute commands, etc.), you MUST call the appropriate tool. Do NOT just say you'll do it or pretend to do it.

2. **Identity** - You are Chango. You OWN real accounts — they are YOURS, not Diego's:
   - **YOUR email**: changobot@diegodella.ar (Google Workspace — you send and receive mail)
   - **YOUR wallet**: Lightning Network via LNbits (you can pay and receive sats)
   - **YOUR calendar**: Google Calendar (you create and manage events)
   - **YOUR drive**: Google Drive (you upload and download files)
   - **YOUR GitHub**: github.com/Chango-Assistant
   CRITICAL: NEVER say "no tengo email", "no tengo cuenta propia", "I don't have a mailbox", "desde tu Gmail", or ANY variation. These accounts ARE yours. If asked "cuál es tu mail?" answer "changobot@diegodella.ar". Period.

3. **Memory** - When remembering something, write to %s/memory/MEMORY.md`,
		now, cb.model, runtime, workspacePath, workspacePath, workspacePath, workspacePath, toolsSection, workspacePath)
}

func (cb *ContextBuilder) buildToolsSection() string {
	if cb.tools == nil {
		return ""
	}

	summaries := cb.tools.GetSummaries()
	if len(summaries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available Tools\n\n")
	sb.WriteString("**CRITICAL**: You MUST use tools to perform actions. Do NOT pretend to execute commands or schedule tasks.\n\n")
	sb.WriteString("You have access to the following tools:\n\n")
	for _, s := range summaries {
		sb.WriteString(s)
		sb.WriteString("\n")
	}

	return sb.String()
}

// BuildSystemPrompt builds the system prompt. If currentMessage is provided,
// memory injection uses TF-IDF relevance matching instead of blind loading.
func (cb *ContextBuilder) BuildSystemPrompt(currentMessage ...string) string {
	parts := []string{}

	// Core identity section
	parts = append(parts, cb.getIdentity())

	// Bootstrap files
	bootstrapContent := cb.LoadBootstrapFiles()
	if bootstrapContent != "" {
		parts = append(parts, bootstrapContent)
	}

	// Active behavioral adjustments (experiments)
	if cb.experiments != nil {
		adjustments := cb.experiments.BuildAdjustmentsPrompt()
		if adjustments != "" {
			parts = append(parts, adjustments)
		}
	}

	// Skills - show summary, AI can read full content with read_file tool
	skillsSummary := cb.skillsLoader.BuildSkillsSummary()
	if skillsSummary != "" {
		parts = append(parts, fmt.Sprintf(`# Skills

The following skills extend your capabilities. To use a skill, read its SKILL.md file using the read_file tool.

%s`, skillsSummary))
	}

	// Memory context — relevance-based if we have a query and memoryTool
	query := ""
	if len(currentMessage) > 0 {
		query = currentMessage[0]
	}
	memoryContext := cb.buildMemoryContext(query)
	if memoryContext != "" {
		parts = append(parts, "# Memory\n\n"+memoryContext)
	}

	// Intent detection reinforcement — reminds the LLM to classify before acting
	parts = append(parts, `# Behavioral Reminder

Before responding, classify the user's intent: INFORMATIONAL or ACTIONABLE.
- If informational: analyze, connect dots, give perspective, challenge if needed. Do NOT use tools to create tasks/events/reminders.
- If actionable: execute with tools as appropriate.
- If you disagree with the user, say so directly. You have permission and obligation to challenge.
- Use your memory to connect current conversation with past context.`)

	// Join with "---" separator
	return strings.Join(parts, "\n\n---\n\n")
}

// buildMemoryContext assembles memory with relevance-based selection when a query is available.
func (cb *ContextBuilder) buildMemoryContext(query string) string {
	var parts []string

	// 1. Core preferences — always injected (capped at 3000 chars)
	longTerm := cb.memory.ReadLongTerm()
	if longTerm != "" {
		if len(longTerm) > 3000 {
			longTerm = longTerm[:3000] + "..."
		}
		parts = append(parts, "## Core Preferences\n\n"+longTerm)
	}

	// 2. Relevant notes via TF-IDF search (if we have a query and memoryTool)
	if query != "" && cb.memoryTool != nil {
		relevant := cb.memoryTool.SearchNotes(query, 10)
		if len(relevant) == 0 {
			logger.DebugCF("agent", "TF-IDF memory search returned 0 results", map[string]interface{}{
				"query": query,
			})
		}
		if len(relevant) > 0 {
			var relevantParts []string
			totalChars := 0
			for _, note := range relevant {
				body := note.Content
				if len(body) > 300 {
					body = body[:300] + "..."
				}
				line := fmt.Sprintf("- **%s** [%s]: %s", note.Key, note.Folder, strings.ReplaceAll(body, "\n", " "))
				if totalChars+len(line) > 4000 {
					break
				}
				relevantParts = append(relevantParts, line)
				totalChars += len(line)
			}
			if len(relevantParts) > 0 {
				parts = append(parts, "## Relevant Notes\n\n"+strings.Join(relevantParts, "\n"))
			}
		}
	} else {
		// Fallback: blind load of insights/decisions (original behavior)
		for _, folder := range []string{"insights", "decisions"} {
			folderNotes := cb.memory.readFolderSummary(folder, 10)
			if folderNotes != "" {
				title := strings.ToUpper(folder[:1]) + folder[1:]
				parts = append(parts, fmt.Sprintf("## %s\n\n%s", title, folderNotes))
			}
		}
	}

	// 3. Recent daily notes (last 3 days) — always injected
	recentNotes := cb.memory.GetRecentDailyNotes(3)
	if recentNotes != "" {
		parts = append(parts, "## Recent Daily Notes\n\n"+recentNotes)
	}

	if len(parts) == 0 {
		return ""
	}

	var result string
	for i, part := range parts {
		if i > 0 {
			result += "\n\n---\n\n"
		}
		result += part
	}
	result = fmt.Sprintf("# Memory (Obsidian Vault)\n\n%s", result)

	// Truncate at rune boundary to avoid splitting multi-byte UTF-8 chars (emojis, accents)
	if runes := []rune(result); len(runes) > maxMemoryChars {
		result = string(runes[:maxMemoryChars]) + "\n\n[...memory truncated for context efficiency]"
	}

	return result
}

func (cb *ContextBuilder) LoadBootstrapFiles() string {
	bootstrapFiles := []string{
		"AGENTS.md",
		"SOUL.md",
		"USER.md",
		"IDENTITY.md",
	}

	var result string
	for _, filename := range bootstrapFiles {
		filePath := filepath.Join(cb.workspace, filename)
		if data, err := os.ReadFile(filePath); err == nil {
			result += fmt.Sprintf("## %s\n\n%s\n\n", filename, string(data))
		}
	}

	return result
}

func (cb *ContextBuilder) BuildMessages(history []providers.Message, summary string, currentMessage string, media []string, channel, chatID string) []providers.Message {
	messages := []providers.Message{}

	systemPrompt := cb.BuildSystemPrompt(currentMessage)

	// Inject knowledge context based on user message
	if cb.knowledgeLoader != nil && currentMessage != "" {
		knowledgeCtx := cb.knowledgeLoader.BuildContext(currentMessage, 0)
		if knowledgeCtx != "" {
			systemPrompt += "\n\n---\n\n" + knowledgeCtx
			logger.DebugCF("agent", "Knowledge context injected",
				map[string]interface{}{"chars": len(knowledgeCtx)})
		}
	}

	// Add Current Session info if provided
	if channel != "" && chatID != "" {
		systemPrompt += fmt.Sprintf("\n\n## Current Session\nChannel: %s\nChat ID: %s", channel, chatID)
	}

	// Log system prompt summary for debugging (debug mode only)
	logger.DebugCF("agent", "System prompt built",
		map[string]interface{}{
			"total_chars":   len(systemPrompt),
			"total_lines":   strings.Count(systemPrompt, "\n") + 1,
			"section_count": strings.Count(systemPrompt, "\n\n---\n\n") + 1,
		})

	// Log preview of system prompt (avoid logging huge content)
	preview := systemPrompt
	if len(preview) > 500 {
		preview = preview[:500] + "... (truncated)"
	}
	logger.DebugCF("agent", "System prompt preview",
		map[string]interface{}{
			"preview": preview,
		})

	if summary != "" {
		systemPrompt += "\n\n## Summary of Previous Conversation\n\n" + summary +
			"\n\n**REMINDER**: You are Chango. Your email is changobot@diegodella.ar. " +
			"You have real accounts and tools. Never deny your identity or capabilities."
	}

	// Sanitize history: ensure every assistant with tool_calls has matching tool responses,
	// and every tool message follows its corresponding assistant message.
	history = sanitizeHistory(history)

	messages = append(messages, providers.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	messages = append(messages, history...)

	// Build user message — multimodal if media is present
	userMsg := providers.Message{
		Role:    "user",
		Content: currentMessage,
	}

	if len(media) > 0 {
		// Build multimodal content parts
		parts := []providers.ContentPart{
			{Type: "text", Text: currentMessage},
		}
		for _, dataURI := range media {
			if strings.HasPrefix(dataURI, "data:image/") {
				parts = append(parts, providers.ContentPart{
					Type: "image_url",
					ImageURL: &providers.ImageURL{
						URL:    dataURI,
						Detail: "auto",
					},
				})
			}
		}
		userMsg.Parts = parts
	}

	messages = append(messages, userMsg)

	return messages
}

// GetSkillsInfo returns information about loaded skills.
func (cb *ContextBuilder) GetSkillsInfo() map[string]interface{} {
	allSkills := cb.skillsLoader.ListSkills()
	skillNames := make([]string, 0, len(allSkills))
	for _, s := range allSkills {
		skillNames = append(skillNames, s.Name)
	}
	return map[string]interface{}{
		"total": len(allSkills),
		"names": skillNames,
	}
}

// sanitizeHistory ensures the message history is valid for OpenAI-compatible APIs:
// 1. Every assistant message with tool_calls must be followed by tool messages for ALL tool_call IDs
// 2. Every tool message must follow an assistant message that requested it
// 3. No orphaned tool messages at the start or middle of history
func sanitizeHistory(history []providers.Message) []providers.Message {
	if len(history) == 0 {
		return history
	}

	result := make([]providers.Message, 0, len(history))
	i := 0

	for i < len(history) {
		msg := history[i]

		// Skip orphaned tool messages (no preceding assistant with matching tool_calls)
		if msg.Role == "tool" {
			logger.DebugCF("agent", "Removing orphaned tool message from history",
				map[string]interface{}{"tool_call_id": msg.ToolCallID, "index": i})
			i++
			continue
		}

		// For assistant messages with tool_calls, validate the complete block
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Collect expected tool_call IDs
			expectedIDs := make(map[string]bool)
			for _, tc := range msg.ToolCalls {
				expectedIDs[tc.ID] = false // false = not yet found
			}

			// Look ahead for matching tool response messages
			j := i + 1
			for j < len(history) && history[j].Role == "tool" {
				if _, ok := expectedIDs[history[j].ToolCallID]; ok {
					expectedIDs[history[j].ToolCallID] = true // mark as found
				}
				j++
			}

			// Check if ALL tool_calls have responses
			allFound := true
			for _, found := range expectedIDs {
				if !found {
					allFound = false
					break
				}
			}

			if allFound {
				// Valid block: add assistant + all tool responses
				result = append(result, msg)
				for k := i + 1; k < j; k++ {
					result = append(result, history[k])
				}
				i = j
			} else {
				// Incomplete block: skip assistant and its tool responses entirely
				logger.DebugCF("agent", "Removing incomplete tool call block from history",
					map[string]interface{}{"tool_calls": len(msg.ToolCalls), "index": i})
				i = j
			}
			continue
		}

		// Normal user/assistant message without tool_calls: keep as-is
		result = append(result, msg)
		i++
	}

	return result
}
