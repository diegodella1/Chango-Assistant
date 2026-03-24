package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// InteractionScore tracks the quality of a single interaction.
type InteractionScore struct {
	SessionKey   string   `json:"session_key"`
	Timestamp    string   `json:"timestamp"`
	UserSentiment string  `json:"user_sentiment"` // positive, neutral, negative, correction
	WasUseful    bool     `json:"was_useful"`
	WasCorrected bool     `json:"was_corrected"`
	ResponseType string   `json:"response_type"` // listen, advise, execute, challenge
	TopicTags    []string `json:"topic_tags"`
}

// ScoringEngine analyzes and persists interaction quality scores.
type ScoringEngine struct {
	workspace string
	scores    []InteractionScore
	mu        sync.RWMutex
}

const maxScores = 500

// NewScoringEngine creates a ScoringEngine and loads existing scores from disk.
func NewScoringEngine(workspace string) *ScoringEngine {
	se := &ScoringEngine{
		workspace: workspace,
	}
	se.load()
	return se
}

func (se *ScoringEngine) filePath() string {
	return filepath.Join(se.workspace, "state", "interaction_scores.json")
}

func (se *ScoringEngine) load() {
	data, err := os.ReadFile(se.filePath())
	if err != nil {
		se.scores = make([]InteractionScore, 0)
		return
	}
	var scores []InteractionScore
	if err := json.Unmarshal(data, &scores); err != nil {
		logger.WarnCF("scoring", "Failed to parse interaction scores: %v", map[string]interface{}{"error": err.Error()})
		se.scores = make([]InteractionScore, 0)
		return
	}
	se.scores = scores
}

func (se *ScoringEngine) save() error {
	dir := filepath.Dir(se.filePath())
	os.MkdirAll(dir, 0755)

	data, err := json.MarshalIndent(se.scores, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write
	tmp := se.filePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, se.filePath())
}

// Score analyzes the last exchange in a session and records the interaction quality.
// This is designed to be lightweight and non-blocking — call it in a goroutine.
func (se *ScoringEngine) Score(sessionKey string, history []providers.Message) {
	if len(history) < 2 {
		return
	}

	// Find last user message and last assistant message
	var lastUser, lastAssistant string
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" && lastUser == "" {
			lastUser = history[i].Content
		}
		if history[i].Role == "assistant" && lastAssistant == "" {
			lastAssistant = history[i].Content
		}
		if lastUser != "" && lastAssistant != "" {
			break
		}
	}

	if lastUser == "" {
		return
	}

	sentiment := detectSentiment(lastUser)
	responseType := detectResponseType(lastAssistant)
	tags := extractTopicTags(lastUser)
	wasCorrected := sentiment == "correction"

	// Heuristic: user acted on the response if they continue the topic or say positive things
	wasUseful := sentiment == "positive" || (sentiment == "neutral" && lastAssistant != "")

	score := InteractionScore{
		SessionKey:    sessionKey,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		UserSentiment: sentiment,
		WasUseful:     wasUseful,
		WasCorrected:  wasCorrected,
		ResponseType:  responseType,
		TopicTags:     tags,
	}

	se.mu.Lock()
	defer se.mu.Unlock()

	se.scores = append(se.scores, score)

	// Rotate: keep last maxScores
	if len(se.scores) > maxScores {
		se.scores = se.scores[len(se.scores)-maxScores:]
	}

	if err := se.save(); err != nil {
		logger.WarnCF("scoring", "Failed to save interaction scores: %v", map[string]interface{}{"error": err.Error()})
	}
}

// GetWeeklyReport returns a summary of interaction quality for the past 7 days.
func (se *ScoringEngine) GetWeeklyReport() string {
	se.mu.RLock()
	defer se.mu.RUnlock()

	weekAgo := time.Now().UTC().Add(-7 * 24 * time.Hour)

	var weekScores []InteractionScore
	for _, s := range se.scores {
		t, err := time.Parse(time.RFC3339, s.Timestamp)
		if err != nil {
			continue
		}
		if t.After(weekAgo) {
			weekScores = append(weekScores, s)
		}
	}

	if len(weekScores) == 0 {
		return "No interactions recorded this week."
	}

	// Count sentiments
	sentiments := map[string]int{}
	responseTypes := map[string]int{}
	topicCounts := map[string]int{}
	corrections := 0
	useful := 0

	for _, s := range weekScores {
		sentiments[s.UserSentiment]++
		responseTypes[s.ResponseType]++
		if s.WasCorrected {
			corrections++
		}
		if s.WasUseful {
			useful++
		}
		for _, tag := range s.TopicTags {
			topicCounts[tag]++
		}
	}

	total := len(weekScores)
	correctionRate := float64(corrections) / float64(total) * 100
	usefulRate := float64(useful) / float64(total) * 100

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Weekly Interaction Report (%d interactions)\n", total))
	sb.WriteString(fmt.Sprintf("---\n"))
	sb.WriteString(fmt.Sprintf("Correction rate: %.1f%% (%d/%d)\n", correctionRate, corrections, total))
	sb.WriteString(fmt.Sprintf("Useful rate: %.1f%% (%d/%d)\n", usefulRate, useful, total))
	sb.WriteString("\nSentiment breakdown:\n")
	for s, c := range sentiments {
		sb.WriteString(fmt.Sprintf("  %s: %d (%.0f%%)\n", s, c, float64(c)/float64(total)*100))
	}
	sb.WriteString("\nResponse types:\n")
	for rt, c := range responseTypes {
		sb.WriteString(fmt.Sprintf("  %s: %d\n", rt, c))
	}

	// Top 5 topics
	if len(topicCounts) > 0 {
		sb.WriteString("\nTop topics:\n")
		type kv struct {
			k string
			v int
		}
		var sorted []kv
		for k, v := range topicCounts {
			sorted = append(sorted, kv{k, v})
		}
		// Simple sort (max 5)
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j].v > sorted[i].v {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		limit := 5
		if len(sorted) < limit {
			limit = len(sorted)
		}
		for _, t := range sorted[:limit] {
			sb.WriteString(fmt.Sprintf("  %s: %d mentions\n", t.k, t.v))
		}
	}

	return sb.String()
}

// GetCorrectionRate returns the fraction of interactions (0.0-1.0) that were
// corrections for a given topic in the last N days. Returns 0 if no data.
// Use topic="" to get the global correction rate across all topics.
func (se *ScoringEngine) GetCorrectionRate(topic string, days int) float64 {
	se.mu.RLock()
	defer se.mu.RUnlock()

	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	total := 0
	corrections := 0

	for _, s := range se.scores {
		t, err := time.Parse(time.RFC3339, s.Timestamp)
		if err != nil || t.Before(cutoff) {
			continue
		}
		if topic != "" {
			found := false
			for _, tag := range s.TopicTags {
				if tag == topic {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		total++
		if s.WasCorrected {
			corrections++
		}
	}

	if total == 0 {
		return 0
	}
	return float64(corrections) / float64(total)
}

// detectSentiment classifies user sentiment from the message text.
func detectSentiment(msg string) string {
	lower := strings.ToLower(msg)

	// Correction signals (Spanish + English)
	correctionSignals := []string{
		"no,", "no.", "eso no", "te dije que", "te pedí que", "no era eso",
		"mal", "está mal", "eso no es", "no es lo que", "corregí", "arreglá",
		"wrong", "that's not", "i said", "i asked for", "fix",
		"no no", "nono", "no!", "otra vez",
	}
	for _, signal := range correctionSignals {
		if strings.Contains(lower, signal) {
			return "correction"
		}
	}

	// Positive signals
	positiveSignals := []string{
		"genial", "perfecto", "sí", "dale", "buenísimo", "gracias", "groso",
		"excelente", "joya", "bien ahí", "ok dale", "listo", "impecable",
		"great", "perfect", "thanks", "awesome", "nice", "good",
		"bárbaro", "de una", "fenómeno",
	}
	for _, signal := range positiveSignals {
		if strings.Contains(lower, signal) {
			return "positive"
		}
	}

	// Negative signals (frustration, not correction)
	negativeSignals := []string{
		"no funciona", "no anda", "sigue sin", "todavía no", "roto",
		"doesn't work", "still broken", "not working",
	}
	for _, signal := range negativeSignals {
		if strings.Contains(lower, signal) {
			return "negative"
		}
	}

	return "neutral"
}

// detectResponseType classifies what kind of response the assistant gave.
func detectResponseType(msg string) string {
	if msg == "" {
		return "listen"
	}
	lower := strings.ToLower(msg)

	// Execute: the assistant performed an action
	executeSignals := []string{"listo", "done", "creé", "guardé", "ejecuté", "hecho", "saved", "created", "deployed"}
	for _, s := range executeSignals {
		if strings.Contains(lower, s) {
			return "execute"
		}
	}

	// Challenge: the assistant pushed back
	challengeSignals := []string{"no estoy de acuerdo", "cuidado con", "ojo que", "i disagree", "be careful", "the risk is", "pero considerá"}
	for _, s := range challengeSignals {
		if strings.Contains(lower, s) {
			return "challenge"
		}
	}

	// Advise: contains recommendations
	adviseSignals := []string{"te recomiendo", "podrías", "sugiero", "i'd suggest", "you could", "consider", "mi recomendación"}
	for _, s := range adviseSignals {
		if strings.Contains(lower, s) {
			return "advise"
		}
	}

	return "listen"
}

// extractTopicTags extracts simple topic keywords from a message.
func extractTopicTags(msg string) []string {
	// Simple keyword extraction: look for known domain words
	lower := strings.ToLower(msg)
	var tags []string

	topicKeywords := map[string]string{
		"deploy":     "deployment",
		"docker":     "docker",
		"api":        "api",
		"bug":        "debugging",
		"error":      "debugging",
		"test":       "testing",
		"database":   "database",
		"db":         "database",
		"postgres":   "database",
		"ui":         "frontend",
		"frontend":   "frontend",
		"backend":    "backend",
		"server":     "backend",
		"config":     "configuration",
		"ssl":        "infrastructure",
		"dns":        "infrastructure",
		"ram":        "system-health",
		"cpu":        "system-health",
		"memoria":    "system-health",
		"cron":       "automation",
		"schedule":   "automation",
		"telegram":   "messaging",
		"light":      "smart-home",
		"luz":        "smart-home",
		"luces":      "smart-home",
		"bitcoin":    "finance",
		"lightning":  "finance",
		"sats":       "finance",
		"idea":       "brainstorming",
		"proyecto":   "projects",
		"project":    "projects",
		"write":      "content",
		"blog":       "content",
		"post":       "content",
	}

	seen := map[string]bool{}
	for keyword, tag := range topicKeywords {
		if strings.Contains(lower, keyword) && !seen[tag] {
			tags = append(tags, tag)
			seen[tag] = true
		}
	}

	return tags
}
