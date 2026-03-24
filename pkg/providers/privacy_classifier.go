package providers

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// SensitivityLevel represents the privacy classification of a message.
type SensitivityLevel int

const (
	Safe      SensitivityLevel = iota
	Sensitive
)

// ClassifyResult holds the classification decision and metadata.
type ClassifyResult struct {
	Level  SensitivityLevel
	Reason string  // e.g. "image_detected", "dni_pattern", "llm_classified"
	Tier   int     // 1 or 2
	Score  float64 // weighted score from Tier 1
}

type privacyPattern struct {
	regex  *regexp.Regexp
	reason string
	weight float64 // 1.0 = auto-sensitive, 0.5 = contributes to score
}

// PrivacyClassifier performs two-tier sensitivity classification.
// Tier 1: fast regex/keyword matching (zero latency, zero cost)
// Tier 2: optional local LLM classification for ambiguous cases
type PrivacyClassifier struct {
	patterns []privacyPattern
	keywords map[string]bool
	localLLM LLMProvider // optional, for Tier 2
	cfg      PrivacyClassifierConfig
}

// PrivacyClassifierConfig mirrors the fields needed from config.PrivacyConfig.
type PrivacyClassifierConfig struct {
	Tier2Enabled       bool
	AlwaysPrivateMedia bool
	FailClosed         bool
	ExtraKeywords      []string
	ExtraPatterns      []string
}

// Built-in patterns for Argentine/common sensitive data.
var builtinPatterns = []privacyPattern{
	// CUIT/CUIL (XX-XXXXXXXX-X)
	{regexp.MustCompile(`\b\d{2}-\d{8}-\d\b`), "cuit_pattern", 1.0},
	// Credit card numbers (13-19 digits with optional separators)
	{regexp.MustCompile(`\b(?:\d[ -]*?){13,19}\b`), "credit_card_pattern", 1.0},
	// Credentials/secrets with values
	{regexp.MustCompile(`(?i)\b(?:password|contraseña|clave|secret|token|api[_\s]?key)\s*[:=]\s*\S+`), "credential_pattern", 1.0},
	// Argentine DNI (XX.XXX.XXX or XXXXXXXX)
	{regexp.MustCompile(`\b\d{2}[\.\-]\d{3}[\.\-]\d{3}\b`), "dni_pattern", 0.7},
	// Phone numbers (Argentine format)
	{regexp.MustCompile(`\b(?:\+?54|0)?(?:11|[2-9]\d)\d{8}\b`), "phone_pattern", 0.5},
	// Email addresses
	{regexp.MustCompile(`\b[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}\b`), "email_pattern", 0.3},
	// Medical terms (Spanish)
	{regexp.MustCompile(`(?i)\b(?:diagn[oó]s|medicament|receta\s+m[eé]dica|tratamiento\s+m[eé]dico|s[ií]ntoma|biopsia|an[aá]lisis\s+de\s+sangre)\b`), "medical_pattern", 0.8},
	// Financial terms with personal context (Spanish)
	{regexp.MustCompile(`(?i)\b(?:sueldo|salario|deuda|pr[eé]stamo|hipoteca|cuenta\s*bancaria|CBU|CVU)\b`), "financial_pattern", 0.8},
}

// NewPrivacyClassifier creates a classifier with built-in + custom patterns.
func NewPrivacyClassifier(cfg PrivacyClassifierConfig, localLLM LLMProvider) *PrivacyClassifier {
	pc := &PrivacyClassifier{
		patterns: make([]privacyPattern, len(builtinPatterns)),
		keywords: make(map[string]bool),
		localLLM: localLLM,
		cfg:      cfg,
	}
	copy(pc.patterns, builtinPatterns)

	// Add user-configured patterns
	for _, p := range cfg.ExtraPatterns {
		if re, err := regexp.Compile(p); err == nil {
			pc.patterns = append(pc.patterns, privacyPattern{regex: re, reason: "custom_pattern", weight: 0.8})
		}
	}

	// Add user-configured keywords
	for _, kw := range cfg.ExtraKeywords {
		pc.keywords[strings.ToLower(kw)] = true
	}

	return pc
}

// Classify determines the sensitivity level of the messages.
// Only inspects the last user message (current turn).
func (pc *PrivacyClassifier) Classify(ctx context.Context, messages []Message) ClassifyResult {
	// Find the last user message
	var lastUserMsg *Message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserMsg = &messages[i]
			break
		}
	}

	if lastUserMsg == nil {
		return ClassifyResult{Level: Safe, Reason: "no_user_message", Tier: 1}
	}

	// Check for media (images/documents) — always sensitive if configured
	hasMedia := false
	if pc.cfg.AlwaysPrivateMedia && len(lastUserMsg.Parts) > 0 {
		for _, part := range lastUserMsg.Parts {
			if part.Type == "image_url" && part.ImageURL != nil {
				hasMedia = true
				break
			}
		}
	}
	if hasMedia {
		return ClassifyResult{Level: Sensitive, Reason: "image_detected", Tier: 1, Score: 1.0}
	}

	// Tier 1: pattern matching
	content := lastUserMsg.Content
	score, reason := pc.tier1(content)

	if score >= 1.0 {
		return ClassifyResult{Level: Sensitive, Reason: reason, Tier: 1, Score: score}
	}

	// Ambiguous zone (0.5 - 1.0) — try Tier 2 if enabled
	if score >= 0.5 && pc.cfg.Tier2Enabled && pc.localLLM != nil {
		tier2Result := pc.tier2(ctx, content)
		return ClassifyResult{Level: tier2Result, Reason: "llm_classified", Tier: 2, Score: score}
	}

	// Ambiguous but no Tier 2 — fail-closed or safe
	if score >= 0.5 && pc.cfg.FailClosed {
		return ClassifyResult{Level: Sensitive, Reason: reason + "_fail_closed", Tier: 1, Score: score}
	}

	return ClassifyResult{Level: Safe, Reason: "clean", Tier: 1, Score: score}
}

// tier1 runs regex patterns and keyword matching, returning a weighted score.
func (pc *PrivacyClassifier) tier1(content string) (float64, string) {
	if content == "" {
		return 0, ""
	}

	var maxScore float64
	var maxReason string
	contentLower := strings.ToLower(content)

	// Pattern matching
	for _, p := range pc.patterns {
		if p.regex.MatchString(content) {
			if p.weight > maxScore {
				maxScore = p.weight
				maxReason = p.reason
			}
			// If already at max, no need to continue
			if maxScore >= 1.0 {
				return maxScore, maxReason
			}
		}
	}

	// Keyword matching
	words := strings.Fields(contentLower)
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?¿¡\"'()[]{}") // strip punctuation
		if pc.keywords[w] {
			if 0.8 > maxScore {
				maxScore = 0.8
				maxReason = "keyword_" + w
			}
		}
	}

	return maxScore, maxReason
}

// tier2 uses the local LLM to classify ambiguous content.
func (pc *PrivacyClassifier) tier2(ctx context.Context, text string) SensitivityLevel {
	// Truncate to avoid wasting local model context
	if len(text) > 500 {
		text = text[:500]
	}

	prompt := `Classify if this message contains personal, sensitive, or private information.
Reply ONLY "SENSITIVE" or "SAFE". No explanation.

Sensitive categories: personal identity (names+IDs), financial data, health/medical,
credentials/passwords, legal matters, intimate/personal life details.

Message: ` + text

	classifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := pc.localLLM.Chat(classifyCtx, []Message{
		{Role: "user", Content: prompt},
	}, nil, "", map[string]interface{}{
		"max_tokens":  8,
		"temperature": 0.0,
	})

	if err != nil || resp == nil {
		// Fail-closed: if classification fails, assume sensitive
		return Sensitive
	}

	answer := strings.TrimSpace(strings.ToUpper(resp.Content))
	if strings.Contains(answer, "SAFE") {
		return Safe
	}
	return Sensitive
}
