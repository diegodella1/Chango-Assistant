package agent

import (
	"context"
	"time"

	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// innerMonologue generates an internal chain-of-thought before the main response.
// This implements "System 2" thinking — deliberate, analytical, not reactive.
// The result is injected as a system hint to guide the main LLM call.
func (al *AgentLoop) innerMonologue(ctx context.Context, userMessage string, history []providers.Message) string {
	// Skip for short/trivial messages
	if len(userMessage) < 30 {
		return ""
	}

	// Check token budget before spending on background monologue
	if al.tokenBudget != nil && !al.tokenBudget.CanSpend(500, true) {
		logger.DebugCF("agent", "Skipping inner monologue — token budget exceeded", nil)
		return ""
	}

	monologueCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Build a focused prompt for internal deliberation
	prompt := `You are the internal reasoning layer of an autonomous AI agent called Chango.
The user just sent a message. Before the main agent responds, analyze:

1. INTENT: What does the user really want? (information? action? emotional support? challenge?)
2. CONTEXT: What do I know from memory/history that's relevant? Any patterns or contradictions?
3. APPROACH: Should I listen, advise, challenge, or execute? What's the best angle?
4. RISKS: What could go wrong if I respond naively? Am I about to be a yes-man?
5. PLAN: In 1-2 sentences, what should my response focus on?

Be concise — 3-5 lines max. This is internal thinking, not a response to the user.

User's message: ` + userMessage

	// Use the last 6 history messages for context (keep it light)
	var contextMsgs []providers.Message
	contextMsgs = append(contextMsgs, providers.Message{Role: "system", Content: "You are Chango's internal reasoning module."})

	// Add last few messages for context
	start := len(history) - 6
	if start < 0 {
		start = 0
	}
	for i := start; i < len(history); i++ {
		if history[i].Role == "user" || history[i].Role == "assistant" {
			contextMsgs = append(contextMsgs, providers.Message{
				Role:    history[i].Role,
				Content: history[i].Content,
			})
		}
	}
	contextMsgs = append(contextMsgs, providers.Message{Role: "user", Content: prompt})

	al.emitEvent("think") // emit event for neural visualization

	// Use local provider if available (zero cost, private, faster for short prompts)
	monologueProvider := al.provider
	monologueModel := al.model
	if al.localProvider != nil {
		monologueProvider = al.localProvider
		monologueModel = "" // use local model's default
	}

	resp, err := monologueProvider.Chat(monologueCtx, contextMsgs, nil, monologueModel, map[string]interface{}{
		"max_tokens":  constants.MonologueMaxTokens,
		"temperature": constants.LowTemperature,
	})
	if err != nil {
		logger.DebugCF("agent", "Inner monologue failed (non-critical)", map[string]interface{}{"error": err.Error()})
		return ""
	}

	if resp != nil && resp.Usage != nil {
		if al.tracker != nil {
			al.tracker.Record("monologue", resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
		}
		if al.tokenBudget != nil {
			al.tokenBudget.Record(int64(resp.Usage.TotalTokens), true)
		}
	}

	logger.DebugCF("agent", "Inner monologue completed", map[string]interface{}{
		"chars": len(resp.Content),
	})

	return resp.Content
}
