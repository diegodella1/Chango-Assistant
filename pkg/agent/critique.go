package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// selfCritique evaluates the draft response and optionally triggers a revision.
// Uses the local model (zero cloud cost) to catch:
// - Generic/filler responses ("it depends", "great question")
// - Yes-man behavior (agreeing when should challenge)
// - Missing context from memory/scratchpad
// - Responses that don't match the user's actual intent
// Returns the revision hint if critique fails, or "" if the response passes.
func (al *AgentLoop) selfCritique(ctx context.Context, userMessage, draftResponse string) string {
	// Skip if no local provider available
	if al.localProvider == nil {
		return ""
	}

	// Skip short responses (greetings, confirmations, etc.)
	if len(draftResponse) < 50 {
		return ""
	}

	// Check token budget before spending on critique
	if al.tokenBudget != nil && !al.tokenBudget.CanSpend(300, true) {
		logger.DebugCF("agent", "Skipping self-critique — token budget exceeded", nil)
		return ""
	}

	critiqueCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	prompt := `You are Chango's quality filter. Evaluate this draft response:

User asked: ` + userMessage + `
Draft response: ` + draftResponse + `

Check:
1. Is this generic filler or genuinely useful? (GENERIC / USEFUL)
2. Am I being a yes-man? Should I challenge instead? (YES-MAN / HONEST)
3. Does the response match what the user actually wants? (MATCH / MISMATCH)
4. Am I missing something obvious? (COMPLETE / MISSING: what?)

If ALL checks pass, reply: PASS
If any fail, reply: REVISE: [one line explaining what to fix]`

	messages := []providers.Message{
		{Role: "system", Content: "You are a quality filter. Be brief and direct. Reply only PASS or REVISE: [reason]."},
		{Role: "user", Content: prompt},
	}

	al.emitEvent("think")

	resp, err := al.localProvider.Chat(critiqueCtx, messages, nil, "", map[string]interface{}{
		"max_tokens":  constants.CritiqueMaxTokens,
		"temperature": constants.MinimalTemperature,
	})
	if err != nil {
		logger.DebugCF("agent", "Self-critique failed (non-critical)", map[string]interface{}{"error": err.Error()})
		return "" // fail open
	}

	// Record token usage
	if resp != nil && resp.Usage != nil {
		if al.tracker != nil {
			al.tracker.Record("critique", resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
		}
		if al.tokenBudget != nil {
			al.tokenBudget.Record(int64(resp.Usage.TotalTokens), true)
		}
	}

	result := strings.TrimSpace(resp.Content)
	logger.DebugCF("agent", "Self-critique result", map[string]interface{}{
		"result": result,
	})

	// Check for PASS
	if strings.Contains(strings.ToUpper(result), "PASS") {
		return ""
	}

	// Extract revision hint
	if idx := strings.Index(strings.ToUpper(result), "REVISE:"); idx >= 0 {
		hint := strings.TrimSpace(result[idx+7:])
		if hint != "" {
			// Persist failure pattern for long-term learning
			al.recordCritiqueFailure(hint)
			return hint
		}
	}

	// If response is ambiguous, fail open
	return ""
}

// critiqueFailure records a pattern where self-critique triggered a revision.
type critiqueFailure struct {
	Timestamp string `json:"timestamp"`
	Pattern   string `json:"pattern"`
}

// recordCritiqueFailure appends a critique failure to the durable log.
// Accumulated patterns can be analyzed by the reasoning loop.
func (al *AgentLoop) recordCritiqueFailure(hint string) {
	logPath := filepath.Join(al.workspace, "state", "critique_failures.json")

	var failures []critiqueFailure
	if data, err := os.ReadFile(logPath); err == nil {
		json.Unmarshal(data, &failures)
	}

	failures = append(failures, critiqueFailure{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Pattern:   hint,
	})

	// Keep last 50 failures
	if len(failures) > 50 {
		failures = failures[len(failures)-50:]
	}

	if data, err := json.MarshalIndent(failures, "", "  "); err == nil {
		os.MkdirAll(filepath.Dir(logPath), 0755)
		tmp := logPath + ".tmp"
		if err := os.WriteFile(tmp, data, 0644); err == nil {
			os.Rename(tmp, logPath)
		}
	}

	logger.InfoCF("critique", "Recorded failure pattern", map[string]interface{}{
		"pattern": hint,
		"total":   len(failures),
	})
}
