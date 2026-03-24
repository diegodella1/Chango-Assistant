package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/telemetry"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// simpleStopWords contains common stop words in Spanish and English,
// used by simpleTokenize for topic shift detection.
var simpleStopWords = map[string]bool{
	// Spanish
	"que": true, "los": true, "las": true, "del": true, "una": true, "con": true,
	"por": true, "para": true, "como": true, "pero": true, "más": true, "mas": true,
	"este": true, "esta": true, "esto": true, "eso": true, "ese": true, "esa": true,
	"son": true, "ser": true, "hay": true, "está": true, "tiene": true, "todo": true,
	"también": true, "cuando": true, "muy": true, "sin": true, "sobre": true,
	"bien": true, "puede": true, "otro": true, "otra": true,
	// English
	"the": true, "and": true, "that": true, "have": true, "for": true, "not": true,
	"with": true, "you": true, "this": true, "but": true, "from": true, "they": true,
	"would": true, "there": true, "their": true, "what": true, "about": true,
	"which": true, "when": true, "make": true, "can": true, "will": true, "more": true,
}

// summarizeSession summarizes the conversation history for a session.
func (al *AgentLoop) summarizeSession(sessionKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	history := al.sessions.GetHistory(sessionKey)
	summary := al.sessions.GetSummary(sessionKey)

	// Keep last 4 messages for continuity
	if len(history) <= 4 {
		return
	}

	toSummarize := history[:len(history)-4]

	// Oversized Message Guard
	// Skip messages larger than 50% of context window to prevent summarizer overflow
	maxMessageTokens := al.contextWindow / 2
	validMessages := make([]providers.Message, 0)
	omitted := false

	for _, m := range toSummarize {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		// Estimate tokens for this message
		msgTokens := len(m.Content) / 4
		if msgTokens > maxMessageTokens {
			omitted = true
			continue
		}
		validMessages = append(validMessages, m)
	}

	if len(validMessages) == 0 {
		return
	}

	// Multi-Part Summarization
	// Split into two parts if history is significant
	var finalSummary string
	if len(validMessages) > 10 {
		mid := len(validMessages) / 2
		part1 := validMessages[:mid]
		part2 := validMessages[mid:]

		s1, err1 := al.summarizeBatch(ctx, part1, "")
		s2, err2 := al.summarizeBatch(ctx, part2, "")

		if err1 != nil && err2 != nil {
			logger.WarnCF("agent", "Both summarize batches failed", map[string]interface{}{
				"err1": err1.Error(), "err2": err2.Error(),
			})
			return
		}

		// Merge them
		mergePrompt := fmt.Sprintf("Merge these two conversation summaries into one cohesive summary:\n\n1: %s\n\n2: %s", s1, s2)
		resp, err := al.provider.Chat(ctx, []providers.Message{{Role: "user", Content: mergePrompt}}, nil, al.model, map[string]interface{}{
			"max_tokens":  1024,
			"temperature": 0.3,
		})
		if resp != nil && resp.Usage != nil && al.tracker != nil {
			al.tracker.Record(telemetry.FeatureSummarize, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
		}
		if err == nil {
			finalSummary = resp.Content
		} else {
			finalSummary = s1 + " " + s2
		}
	} else {
		finalSummary, _ = al.summarizeBatch(ctx, validMessages, summary)
	}

	if omitted && finalSummary != "" {
		finalSummary += "\n[Note: Some oversized messages were omitted from this summary for efficiency.]"
	}

	if finalSummary != "" {
		al.sessions.SetSummary(sessionKey, finalSummary)
		al.sessions.TruncateHistory(sessionKey, 4)
		al.sessions.Save(sessionKey)
	}

	// Auto-distill memories from the conversation being summarized
	// Only for real conversations (not heartbeat/cron), with enough messages to be meaningful
	if al.memoryTool != nil && len(validMessages) >= 8 &&
		sessionKey != "heartbeat" && !strings.HasPrefix(sessionKey, "cron:") {
		al.distillMemories(ctx, validMessages)
	}
}

// distillMemories extracts durable facts, decisions, preferences, and context from a conversation
// batch and saves them to the obsidian vault automatically. This is the "subconscious" that
// ensures conversations produce long-term learning without explicit user instructions.
func (al *AgentLoop) distillMemories(ctx context.Context, batch []providers.Message) {
	// Check token budget before spending on background distillation
	if al.tokenBudget != nil && !al.tokenBudget.CanSpend(1500, true) {
		logger.InfoCF("agent", "Skipping memory distillation — token budget exceeded", nil)
		return
	}

	// Build a condensed view of the conversation for the extraction LLM call
	var sb strings.Builder
	for _, m := range batch {
		if m.Role == "user" || m.Role == "assistant" {
			sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, utils.Truncate(m.Content, 500)))
		}
	}

	prompt := `Analyze this conversation and extract DURABLE information worth remembering long-term.
Return a JSON array of items to save. Only include genuinely useful information — not ephemeral details.

Categories:
- preferences: user preferences, communication style, corrections (folder: "preferences")
- insights: technical learnings, useful discoveries (folder: "insights")
- decisions: decisions made, patterns observed (folder: "decisions")
- people: info about people mentioned (folder: "people")
- projects: project status updates, milestones (folder: "projects")

For each item return: {"key": "slug-name", "content": "concise note", "folder": "category", "tags": ["tag1"]}

Rules:
- key should be descriptive (e.g., "preference-no-auto-schedule", "person-nico-investor")
- content should be 1-3 sentences max, factual and actionable
- Skip anything ephemeral, already obvious, or too vague
- If nothing worth saving, return []

Conversation:
` + sb.String() + `

Return ONLY valid JSON array, no markdown fences:`

	distillCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := al.provider.Chat(distillCtx, []providers.Message{{Role: "user", Content: prompt}}, nil, al.model, map[string]interface{}{
		"max_tokens":  constants.SummarizeMaxTokens,
		"temperature": constants.MinimalTemperature,
	})
	if err != nil {
		logger.WarnCF("agent", "Memory distillation failed", map[string]interface{}{"error": err.Error()})
		return
	}
	if resp != nil && resp.Usage != nil {
		if al.tracker != nil {
			al.tracker.Record(telemetry.FeatureSummarize, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
		}
		if al.tokenBudget != nil {
			al.tokenBudget.Record(int64(resp.Usage.TotalTokens), true)
		}
	}

	// Parse JSON response
	content := strings.TrimSpace(resp.Content)
	// Strip markdown code fences if present
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var items []struct {
		Key     string   `json:"key"`
		Content string   `json:"content"`
		Folder  string   `json:"folder"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(content), &items); err != nil {
		logger.WarnCF("agent", "Memory distillation parse failed", map[string]interface{}{
			"error": err.Error(), "content": utils.Truncate(content, 200),
		})
		return
	}

	saved := 0
	for _, item := range items {
		if item.Key == "" || item.Content == "" {
			continue
		}
		if err := al.memoryTool.SaveNote(item.Key, item.Content, item.Tags, item.Folder); err != nil {
			logger.WarnCF("agent", "Memory distillation save failed", map[string]interface{}{
				"key": item.Key, "error": err.Error(),
			})
			continue
		}
		saved++
	}

	if saved > 0 {
		logger.InfoCF("agent", "Auto-distilled memories from conversation", map[string]interface{}{
			"saved": saved, "total": len(items),
		})
	}
}

// summarizeBatch summarizes a batch of messages.
func (al *AgentLoop) summarizeBatch(ctx context.Context, batch []providers.Message, existingSummary string) (string, error) {
	prompt := "Provide a concise summary of this conversation segment, preserving core context and key points.\n" +
		"CRITICAL: The assistant in this conversation is Chango, an autonomous AI agent (NOT a generic AI assistant). " +
		"Chango has a real email (changobot@diegodella.ar), a crypto wallet, GitHub access, and 37+ tools. " +
		"NEVER describe the assistant as 'an AI', 'a language model', or 'GPT'. Always refer to the assistant as 'Chango'.\n"
	if existingSummary != "" {
		prompt += "Existing context: " + existingSummary + "\n"
	}
	prompt += "\nCONVERSATION:\n"
	for _, m := range batch {
		prompt += fmt.Sprintf("%s: %s\n", m.Role, m.Content)
	}

	response, err := al.provider.Chat(ctx, []providers.Message{{Role: "user", Content: prompt}}, nil, al.model, map[string]interface{}{
		"max_tokens":  constants.SummarizeMaxTokens,
		"temperature": constants.LowTemperature,
	})
	if response != nil && response.Usage != nil && al.tracker != nil {
		al.tracker.Record(telemetry.FeatureSummarize, response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.TotalTokens)
	}
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// maybeSummarize triggers summarization if the session history exceeds thresholds.
func (al *AgentLoop) maybeSummarize(sessionKey string) {
	newHistory := al.sessions.GetHistory(sessionKey)
	tokenEstimate := al.estimateTokens(newHistory)
	threshold := al.contextWindow * 75 / 100

	if len(newHistory) > 20 || tokenEstimate > threshold {
		if _, loading := al.summarizing.LoadOrStore(sessionKey, true); !loading {
			go func() {
				defer al.summarizing.Delete(sessionKey)
				al.summarizeSession(sessionKey)
			}()
		}
	}
}

// estimateTokens estimates the number of tokens in a message list.
// Uses rune count instead of byte length so that CJK and other multi-byte
// characters are not over-counted (a Chinese character is 3 bytes but roughly
// one token).
func (al *AgentLoop) estimateTokens(messages []providers.Message) int {
	total := 0
	for _, m := range messages {
		total += utf8.RuneCountInString(m.Content) / 3
	}
	return total
}

// detectTopicShift returns true if the current message appears to be about a
// different topic than the recent conversation history. Uses Jaccard similarity
// on word tokens — pure Go, zero tokens, zero latency.
func detectTopicShift(currentMsg string, history []providers.Message) bool {
	currentTokens := simpleTokenize(currentMsg)
	if len(currentTokens) < 3 {
		// Too short to reliably detect shift
		return false
	}

	// Collect tokens from last 3 user+assistant messages
	recentTokens := make(map[string]bool)
	count := 0
	for i := len(history) - 1; i >= 0 && count < 3; i-- {
		if history[i].Role == "user" || history[i].Role == "assistant" {
			for _, t := range simpleTokenize(history[i].Content) {
				recentTokens[t] = true
			}
			count++
		}
	}

	if len(recentTokens) == 0 {
		return false
	}

	// Jaccard similarity: |intersection| / |union|
	currentSet := make(map[string]bool)
	for _, t := range currentTokens {
		currentSet[t] = true
	}

	intersection := 0
	for t := range currentSet {
		if recentTokens[t] {
			intersection++
		}
	}

	union := len(recentTokens)
	for t := range currentSet {
		if !recentTokens[t] {
			union++
		}
	}

	if union == 0 {
		return false
	}

	similarity := float64(intersection) / float64(union)
	return similarity < 0.1
}

// simpleTokenize splits text into lowercase word tokens, filtering short words
// and common stop words. Used for topic shift detection.
func simpleTokenize(text string) []string {
	text = strings.ToLower(text)
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r >= 0x80)
	})

	var tokens []string
	for _, w := range words {
		if len(w) < 3 {
			continue
		}
		// Skip common stop words (Spanish + English)
		if simpleStopWords[w] {
			continue
		}
		tokens = append(tokens, w)
	}
	return tokens
}
