package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// decisionWords triggers Tree of Thought when present in the user message.
var decisionWords = []string{
	"debería", "should", "qué hago", "what do", "mejor opción",
	"pros y contras", "conviene", "what should", "qué me recomendás",
	"best option", "trade-off", "tradeoff", "dilema", "dilemma",
	"comparar", "compare", "elegir", "choose", "decidir", "decide",
}

// shouldUseToT determines if Tree of Thought should be activated for this message.
func shouldUseToT(userMsg, monologue string) bool {
	if len(userMsg) < 50 {
		return false
	}

	lower := strings.ToLower(userMsg)

	// Check for decision words in the user message
	for _, word := range decisionWords {
		if strings.Contains(lower, word) {
			return true
		}
	}

	// Check if inner monologue flagged complexity
	if monologue != "" {
		monLower := strings.ToLower(monologue)
		if strings.Contains(monLower, "complex") || strings.Contains(monLower, "multiple approaches") ||
			strings.Contains(monLower, "complejo") || strings.Contains(monLower, "varios enfoques") {
			return true
		}
	}

	return false
}

// treeOfThought generates multiple response branches and selects/synthesizes the best.
// Only activated for complex decisions (detected by heuristic or inner monologue).
// Returns the synthesized response, or "" if ToT was skipped/failed.
func (al *AgentLoop) treeOfThought(ctx context.Context, messages []providers.Message, opts processOptions) (string, error) {
	// Check token budget — ToT costs ~3-4x a normal response
	if al.tokenBudget != nil && !al.tokenBudget.CanSpend(3000, false) {
		logger.DebugCF("agent", "Skipping Tree of Thought — token budget exceeded", nil)
		return "", nil
	}

	logger.InfoCF("agent", "Tree of Thought activated", map[string]interface{}{
		"session": opts.SessionKey,
		"msg_len": len(opts.UserMessage),
	})

	al.emitEvent("think")

	totCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Define the three branch hints
	branchHints := [3]string{
		"Give the most practical, actionable answer. Focus on what works right now with minimal effort.",
		"Challenge the assumption. What if the opposite is true? What's the contrarian view?",
		"What questions should be asked before deciding? What's missing from the picture?",
	}
	branchNames := [3]string{"pragmatic", "contrarian", "exploratory"}

	// Generate 3 branches in parallel
	var branches [3]string
	var branchErrors [3]error
	var wg sync.WaitGroup

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Clone messages and append the branch-specific system hint
			branchMsgs := make([]providers.Message, len(messages))
			copy(branchMsgs, messages)

			// Insert hint before the last message (user message)
			hint := providers.Message{
				Role:    "system",
				Content: "## Response Approach\n" + branchHints[idx],
			}
			branchMsgs = append(branchMsgs[:len(branchMsgs)-1], hint, branchMsgs[len(branchMsgs)-1])

			resp, err := al.provider.Chat(totCtx, branchMsgs, nil, al.model, map[string]interface{}{
				"max_tokens":  2048,
				"temperature": 0.7,
			})
			if err != nil {
				branchErrors[idx] = err
				return
			}

			branches[idx] = resp.Content

			// Record token usage per branch
			if resp.Usage != nil {
				if al.tracker != nil {
					al.tracker.Record("tot_branch", resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
				}
				if al.tokenBudget != nil {
					al.tokenBudget.Record(int64(resp.Usage.TotalTokens), false)
				}
			}

			logger.DebugCF("agent", fmt.Sprintf("ToT branch '%s' completed", branchNames[idx]),
				map[string]interface{}{"chars": len(resp.Content)})
		}(i)
	}
	wg.Wait()

	// Check if we got at least 2 branches
	validCount := 0
	for i := 0; i < 3; i++ {
		if branches[i] != "" {
			validCount++
		}
		if branchErrors[i] != nil {
			logger.WarnCF("agent", fmt.Sprintf("ToT branch '%s' failed", branchNames[i]),
				map[string]interface{}{"error": branchErrors[i].Error()})
		}
	}
	if validCount < 2 {
		logger.WarnCF("agent", "Tree of Thought aborted — not enough branches succeeded",
			map[string]interface{}{"valid": validCount})
		return "", fmt.Errorf("only %d/3 branches succeeded", validCount)
	}

	// Synthesize the best response from all branches
	synthesisPrompt := fmt.Sprintf(`Three perspectives on the user's question:

PRAGMATIC: %s

CONTRARIAN: %s

EXPLORATORY: %s

Synthesize the best response combining these perspectives.
Be direct — don't say "here are three views". Give ONE clear answer that incorporates the strongest points.
Respond in the same language the user used.`, branches[0], branches[1], branches[2])

	synthMsgs := []providers.Message{
		{Role: "system", Content: "You synthesize multiple perspectives into one clear, direct answer. Never mention that you considered multiple approaches."},
		{Role: "user", Content: synthesisPrompt},
	}

	al.emitEvent("think")

	synthResp, err := al.provider.Chat(totCtx, synthMsgs, nil, al.model, map[string]interface{}{
		"max_tokens":  4096,
		"temperature": 0.5,
	})
	if err != nil {
		logger.WarnCF("agent", "ToT synthesis failed", map[string]interface{}{"error": err.Error()})
		// Fall back to the pragmatic branch if synthesis fails
		if branches[0] != "" {
			return branches[0], nil
		}
		return "", err
	}

	// Record synthesis token usage
	if synthResp.Usage != nil {
		if al.tracker != nil {
			al.tracker.Record("tot_synthesis", synthResp.Usage.PromptTokens, synthResp.Usage.CompletionTokens, synthResp.Usage.TotalTokens)
		}
		if al.tokenBudget != nil {
			al.tokenBudget.Record(int64(synthResp.Usage.TotalTokens), false)
		}
	}

	logger.InfoCF("agent", "Tree of Thought synthesis completed", map[string]interface{}{
		"branch_chars":    [3]int{len(branches[0]), len(branches[1]), len(branches[2])},
		"synthesis_chars": len(synthResp.Content),
	})

	return synthResp.Content, nil
}
