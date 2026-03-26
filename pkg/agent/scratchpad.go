package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Scratchpad holds active working memory for conversation sessions.
// This is NOT long-term memory (vault) and NOT chat history (raw log).
// It's an active thinking space where the agent tracks hypotheses,
// contradictions, patterns, and open questions during a conversation.
// Updated after each turn, injected into the system prompt.
type Scratchpad struct {
	mu       sync.RWMutex
	sessions map[string]*ScratchpadState
}

// ScratchpadState holds the working memory for a single conversation session.
type ScratchpadState struct {
	Hypotheses     []string  // current hypotheses about what the user wants/means
	Contradictions []string  // things that don't add up
	OpenQuestions  []string  // things to verify or ask about
	Patterns       []string  // patterns noticed in this conversation
	EmotionalRead  string    // perceived emotional state of the user
	TopicTrail     []string  // topics discussed in order
	UpdatedAt      time.Time
}

const (
	maxScratchpadItems = 5
	maxTopicTrail      = 10
	topicWordCount     = 4 // words to extract for topic label
)

// NewScratchpad creates a new empty scratchpad.
func NewScratchpad() *Scratchpad {
	return &Scratchpad{
		sessions: make(map[string]*ScratchpadState),
	}
}

// Get returns the current scratchpad for a session (or empty state if none).
func (s *Scratchpad) Get(sessionKey string) *ScratchpadState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if state, ok := s.sessions[sessionKey]; ok {
		return state
	}
	return &ScratchpadState{}
}

// Update analyzes the latest exchange and updates the scratchpad.
// Called by the agent loop AFTER each response with the full message pair.
// Pure Go heuristics, no LLM call.
func (s *Scratchpad) Update(sessionKey string, userMessage string, assistantResponse string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.sessions[sessionKey]
	if !ok {
		state = &ScratchpadState{}
		s.sessions[sessionKey] = state
	}

	state.UpdatedAt = time.Now()

	// 1. Extract topic from user message
	topic := extractTopic(userMessage)
	if topic != "" {
		state.TopicTrail = appendCapped(state.TopicTrail, topic, maxTopicTrail)
	}

	// 2. Detect questions → OpenQuestions
	if strings.Contains(userMessage, "?") {
		// Extract the question sentence(s)
		for _, sentence := range splitSentences(userMessage) {
			if strings.Contains(sentence, "?") {
				q := strings.TrimSpace(sentence)
				if len(q) > 10 { // skip trivial "?"
					state.OpenQuestions = appendCapped(state.OpenQuestions, q, maxScratchpadItems)
				}
			}
		}
	}

	// 3. Detect emotional signals
	emotional := detectEmotion(userMessage)
	if emotional != "" {
		state.EmotionalRead = emotional
	}

	// 4. Detect contradictions (user says something that conflicts with earlier topics)
	contradiction := detectContradiction(userMessage, state.TopicTrail, state.Hypotheses)
	if contradiction != "" {
		state.Contradictions = appendCapped(state.Contradictions, contradiction, maxScratchpadItems)
	}

	// 5. Detect patterns (recurring topics)
	pattern := detectPattern(state.TopicTrail)
	if pattern != "" {
		// Avoid duplicate patterns
		found := false
		for _, p := range state.Patterns {
			if p == pattern {
				found = true
				break
			}
		}
		if !found {
			state.Patterns = appendCapped(state.Patterns, pattern, maxScratchpadItems)
		}
	}

	// 6. If assistant answered a question, mark it as potentially resolved
	// (simple heuristic: if response doesn't contain "?" and is substantial)
	if len(assistantResponse) > 50 && !strings.Contains(assistantResponse, "?") && len(state.OpenQuestions) > 0 {
		// Remove the oldest open question (FIFO — assume it was addressed)
		state.OpenQuestions = state.OpenQuestions[1:]
	}
}

// Format returns the scratchpad as a string suitable for system prompt injection.
// Returns empty string if scratchpad has no meaningful content.
func (s *Scratchpad) Format(sessionKey string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, ok := s.sessions[sessionKey]
	if !ok {
		return ""
	}

	var parts []string

	if len(state.TopicTrail) > 0 {
		parts = append(parts, fmt.Sprintf("- Topics: %s", strings.Join(state.TopicTrail, " → ")))
	}

	if state.EmotionalRead != "" {
		parts = append(parts, fmt.Sprintf("- Emotional read: %s", state.EmotionalRead))
	}

	if len(state.OpenQuestions) > 0 {
		parts = append(parts, fmt.Sprintf("- Open questions: %s", strings.Join(state.OpenQuestions, "; ")))
	}

	if len(state.Patterns) > 0 {
		parts = append(parts, fmt.Sprintf("- Patterns: %s", strings.Join(state.Patterns, "; ")))
	}

	if len(state.Contradictions) > 0 {
		parts = append(parts, fmt.Sprintf("- Contradictions: %s", strings.Join(state.Contradictions, "; ")))
	}

	if len(parts) == 0 {
		return ""
	}

	return "## Working Memory (active thoughts for this conversation)\n" + strings.Join(parts, "\n")
}

// Clear removes a session's scratchpad.
func (s *Scratchpad) Clear(sessionKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionKey)
}

// CleanupStale removes scratchpad sessions that haven't been updated in the given duration.
// Call periodically (e.g., from heartbeat or summarization) to prevent memory leaks.
func (s *Scratchpad) CleanupStale(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for key, state := range s.sessions {
		if state.UpdatedAt.Before(cutoff) {
			delete(s.sessions, key)
			removed++
		}
	}
	return removed
}

// --- Heuristic helpers ---

// extractTopic extracts a short topic label from a message using meaningful words.
func extractTopic(msg string) string {
	tokens := scratchpadTokenize(msg)
	if len(tokens) == 0 {
		return ""
	}
	if len(tokens) > topicWordCount {
		tokens = tokens[:topicWordCount]
	}
	return strings.Join(tokens, " ")
}

// scratchpadTokenize extracts meaningful words from text (reuses stop word logic).
func scratchpadTokenize(text string) []string {
	text = strings.ToLower(text)
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r >= 0x80)
	})

	var tokens []string
	for _, w := range words {
		if len(w) < 3 {
			continue
		}
		if scratchpadStopWords[w] {
			continue
		}
		tokens = append(tokens, w)
	}
	return tokens
}

// scratchpadStopWords — Spanish + English stop words for topic extraction.
var scratchpadStopWords = map[string]bool{
	// Spanish
	"que": true, "los": true, "las": true, "del": true, "una": true, "con": true,
	"por": true, "para": true, "como": true, "pero": true, "más": true, "mas": true,
	"este": true, "esta": true, "esto": true, "eso": true, "ese": true, "esa": true,
	"son": true, "ser": true, "hay": true, "está": true, "tiene": true, "todo": true,
	"también": true, "muy": true, "puede": true, "hacer": true, "quiero": true,
	"tengo": true, "estoy": true, "algo": true, "cómo": true, "cuando": true,
	"donde": true, "qué": true, "solo": true, "bien": true, "ahora": true,
	// English
	"the": true, "and": true, "for": true, "are": true, "but": true, "not": true,
	"you": true, "all": true, "can": true, "had": true, "her": true, "was": true,
	"one": true, "our": true, "out": true, "has": true, "have": true, "been": true,
	"some": true, "them": true, "than": true, "its": true, "over": true, "such": true,
	"that": true, "with": true, "this": true, "will": true, "each": true, "from": true,
	"they": true, "what": true, "about": true, "would": true, "there": true, "their": true,
	"just": true, "into": true, "also": true, "could": true, "which": true, "then": true,
}

// emotionSignals maps keywords to emotional reads (Spanish-first).
var emotionSignals = map[string]string{
	// Frustration
	"frustrado":   "frustrated",
	"frustrada":   "frustrated",
	"no funciona": "frustrated",
	"no anda":     "frustrated",
	"no entendés": "frustrated",
	"harto":       "frustrated",
	"cansado":     "frustrated",
	// Positive
	"genial":    "positive, engaged",
	"perfecto":  "positive, satisfied",
	"excelente": "positive, satisfied",
	"buenísimo": "positive, excited",
	"gracias":   "appreciative",
	"joya":      "positive, satisfied",
	"crack":     "positive, appreciative",
	// Urgency
	"urgente":   "urgent",
	"apurado":   "urgent, time-pressured",
	"rápido":    "urgent",
	"ya mismo":  "urgent, immediate",
	"necesito":  "focused, determined",
	// Confusion
	"no entiendo": "confused",
	"confundido":  "confused",
	"confundida":  "confused",
	"perdido":     "confused, lost",
	// Curiosity
	"interesante": "curious, engaged",
	"contame":     "curious, interested",
	"explicame":   "curious, wants depth",
}

// detectEmotion scans for emotional signals in the message.
func detectEmotion(msg string) string {
	lower := strings.ToLower(msg)
	for signal, emotion := range emotionSignals {
		if strings.Contains(lower, signal) {
			return emotion
		}
	}
	return ""
}

// negationPairs maps affirmative concepts to their negations for contradiction detection.
var negationPairs = []struct {
	affirm string
	negate string
}{
	{"quiero", "no quiero"},
	{"necesito", "no necesito"},
	{"vamos", "no vamos"},
	{"sí", "no"},
	{"acepto", "no acepto"},
	{"me gusta", "no me gusta"},
	{"funciona", "no funciona"},
}

// detectContradiction checks if the current message contradicts earlier context.
func detectContradiction(msg string, topicTrail []string, hypotheses []string) string {
	lower := strings.ToLower(msg)

	// Check negation patterns against topic trail
	trailText := strings.ToLower(strings.Join(topicTrail, " "))
	for _, pair := range negationPairs {
		if strings.Contains(lower, pair.negate) && strings.Contains(trailText, pair.affirm) {
			return fmt.Sprintf("said '%s' now but earlier context suggested '%s'", pair.negate, pair.affirm)
		}
		if strings.Contains(lower, pair.affirm) && strings.Contains(trailText, pair.negate) {
			return fmt.Sprintf("said '%s' now but earlier context suggested '%s'", pair.affirm, pair.negate)
		}
	}

	return ""
}

// detectPattern finds recurring topics in the trail.
func detectPattern(topicTrail []string) string {
	if len(topicTrail) < 3 {
		return ""
	}

	// Count word frequency across topics
	freq := make(map[string]int)
	for _, topic := range topicTrail {
		seen := make(map[string]bool) // count each word once per topic
		for _, word := range strings.Fields(topic) {
			w := strings.ToLower(word)
			if !seen[w] {
				freq[w]++
				seen[w] = true
			}
		}
	}

	// Find words that appear in 3+ different topics
	for word, count := range freq {
		if count >= 3 && len(word) >= 3 {
			return fmt.Sprintf("keeps coming back to '%s' topic", word)
		}
	}

	return ""
}

// splitSentences splits text by common sentence delimiters.
func splitSentences(text string) []string {
	var sentences []string
	current := strings.Builder{}
	for _, r := range text {
		current.WriteRune(r)
		if r == '.' || r == '?' || r == '!' || r == '\n' {
			s := strings.TrimSpace(current.String())
			if s != "" {
				sentences = append(sentences, s)
			}
			current.Reset()
		}
	}
	if s := strings.TrimSpace(current.String()); s != "" {
		sentences = append(sentences, s)
	}
	return sentences
}

// appendCapped appends an item to a slice, removing the oldest if over capacity.
func appendCapped(slice []string, item string, maxLen int) []string {
	slice = append(slice, item)
	if len(slice) > maxLen {
		slice = slice[len(slice)-maxLen:]
	}
	return slice
}
