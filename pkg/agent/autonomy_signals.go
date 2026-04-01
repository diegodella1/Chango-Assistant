package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
)

var autonomyStopWords = map[string]bool{
	"de": true, "la": true, "el": true, "los": true, "las": true,
	"en": true, "del": true, "al": true, "un": true, "una": true,
	"y": true, "o": true, "a": true, "por": true, "con": true,
	"para": true, "como": true, "pero": true, "sobre": true,
	"que": true, "qué": true, "esto": true, "esta": true, "este": true,
	"ese": true, "esa": true, "eso": true, "más": true, "mas": true,
	"muy": true, "the": true, "and": true, "for": true, "with": true,
	"from": true, "this": true, "that": true, "have": true, "your": true,
}

type autonomySignal struct {
	RecurringTopics []string
	NeedsChallenge  bool
}

func buildAutonomyHint(current string, history []providers.Message) string {
	signal := analyzeAutonomySignals(current, history)
	if len(signal.RecurringTopics) == 0 && !signal.NeedsChallenge {
		return ""
	}

	var parts []string
	if len(signal.RecurringTopics) > 0 {
		parts = append(parts, "Recurring topics from recent conversation: "+strings.Join(signal.RecurringTopics, ", "))
	}
	if signal.NeedsChallenge {
		parts = append(parts, "Critical thinking mode: do not default to agreement. Stress-test assumptions, point out risks, and say clearly if the plan sounds weak.")
	}

	return "## Autonomy Signals\n\n" + strings.Join(parts, "\n")
}

func analyzeAutonomySignals(current string, history []providers.Message) autonomySignal {
	return autonomySignal{
		RecurringTopics: detectRecurringTopics(current, history, 3),
		NeedsChallenge:  shouldChallengeUser(current, history),
	}
}

func detectRecurringTopics(current string, history []providers.Message, maxTopics int) []string {
	if maxTopics <= 0 {
		maxTopics = 3
	}

	counts := map[string]int{}
	currentTerms := extractAutonomyTerms(current)
	for _, term := range currentTerms {
		counts[term] = 0
	}

	start := len(history) - 12
	if start < 0 {
		start = 0
	}
	for i := start; i < len(history); i++ {
		msg := history[i]
		if msg.Role != "user" {
			continue
		}
		seen := map[string]bool{}
		for _, term := range extractAutonomyTerms(msg.Content) {
			if _, ok := counts[term]; ok && !seen[term] {
				counts[term]++
				seen[term] = true
			}
		}
	}

	type topicScore struct {
		term  string
		score int
	}
	var ranked []topicScore
	for term, count := range counts {
		if count >= 2 {
			ranked = append(ranked, topicScore{term: term, score: count})
		}
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].term < ranked[j].term
		}
		return ranked[i].score > ranked[j].score
	})

	if len(ranked) > maxTopics {
		ranked = ranked[:maxTopics]
	}

	topics := make([]string, 0, len(ranked))
	for _, item := range ranked {
		topics = append(topics, item.term)
	}
	return topics
}

func shouldChallengeUser(current string, history []providers.Message) bool {
	lower := strings.ToLower(strings.TrimSpace(current))
	if lower == "" {
		return false
	}

	challengePhrases := []string{
		"qué te parece", "que te parece", "opinás", "opinas", "ves bien", "ves mal",
		"te parece bien", "debería", "deberia", "conviene", "sirve", "es buena idea",
		"lo mando", "lo publico", "lo deployo", "lo subo", "lo saco a produccion",
		"should i", "does it make sense", "is this okay", "is this a good idea",
	}
	riskMarkers := []string{
		"rápido", "rapido", "sin revisar", "ya fue", "da igual", "total",
		"prod", "producción", "produccion", "borrar", "delete", "romper",
		"hotfix", "urgente", "apurar", "atajo",
	}

	hasChallengeFrame := false
	for _, phrase := range challengePhrases {
		if strings.Contains(lower, phrase) {
			hasChallengeFrame = true
			break
		}
	}
	if !hasChallengeFrame {
		return false
	}

	for _, marker := range riskMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	// If the recent conversation is clustered around the same topic, bias toward critique.
	return len(detectRecurringTopics(current, history, 2)) > 0
}

func extractAutonomyTerms(text string) []string {
	normalized := strings.ToLower(text)
	replacer := strings.NewReplacer(
		".", " ", ",", " ", ";", " ", ":", " ", "!", " ", "?", " ",
		"¿", " ", "¡", " ", "(", " ", ")", " ", "[", " ", "]", " ",
		"{", " ", "}", " ", "\"", " ", "'", " ", "\n", " ", "\t", " ",
		"/", " ", "\\", " ", "-", " ", "_", " ",
	)
	normalized = replacer.Replace(normalized)

	words := strings.Fields(normalized)
	seen := map[string]bool{}
	var result []string
	for _, word := range words {
		if len(word) < 4 || autonomyStopWords[word] {
			continue
		}
		if seen[word] {
			continue
		}
		seen[word] = true
		result = append(result, word)
	}
	return result
}

func formatRecurringTopicsForPrompt(current string, history []providers.Message) string {
	topics := detectRecurringTopics(current, history, 3)
	if len(topics) == 0 {
		return ""
	}
	return fmt.Sprintf("Recurring topics in recent user messages: %s.", strings.Join(topics, ", "))
}
