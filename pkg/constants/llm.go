package constants

// LLM parameter defaults for internal components (summarizer, critique, etc.).
// These are NOT the user-facing defaults (those live in config.go).
const (
	// DefaultMaxTokens is the default max output tokens for the main agent loop.
	DefaultMaxTokens = 8192

	// SummarizeMaxTokens for conversation summarization.
	SummarizeMaxTokens = 1024

	// SubagentMaxTokens for subagent/tool-loop LLM calls.
	SubagentMaxTokens = 4096

	// TreeOfThoughtMaxTokens for ToT branch exploration.
	TreeOfThoughtBranchMaxTokens = 2048

	// TreeOfThoughtSynthesisMaxTokens for ToT synthesis.
	TreeOfThoughtSynthesisMaxTokens = 4096

	// CritiqueMaxTokens for self-critique (very short).
	CritiqueMaxTokens = 64

	// MonologueMaxTokens for inner monologue.
	MonologueMaxTokens = 256

	// ClassifierMaxTokens for privacy/attention classifiers.
	ClassifierMaxTokens = 30

	// DefaultTemperature for general agent responses.
	DefaultTemperature = 0.7

	// LowTemperature for focused/analytical tasks (summarize, classify, email).
	LowTemperature = 0.3

	// MinimalTemperature for near-deterministic tasks (critique, privacy).
	MinimalTemperature = 0.1

	// CreativeTemperature for synthesis/distillation.
	CreativeTemperature = 0.5
)
