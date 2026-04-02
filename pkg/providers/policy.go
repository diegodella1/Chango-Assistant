package providers

import "strings"

type WorkloadClass string

const (
	WorkloadChat          WorkloadClass = "chat"
	WorkloadReasoning     WorkloadClass = "reasoning"
	WorkloadBackground    WorkloadClass = "background"
	WorkloadVision        WorkloadClass = "vision"
	WorkloadTranscription WorkloadClass = "transcription"
	WorkloadToolLoop      WorkloadClass = "toolloop"
)

type ProviderPolicy struct {
	Workload         WorkloadClass
	MaxMessages      int
	MaxChars         int
	MaxLocalMessages int
	MaxLocalChars    int
	DegradedChars    int
	PreferredRoute   string
	FallbackChain    []string
}

func policyForOptions(messages []Message, tools []ToolDefinition, options map[string]interface{}) ProviderPolicy {
	workload := inferWorkloadClass(messages, tools, options)
	switch workload {
	case WorkloadReasoning, WorkloadBackground:
		return ProviderPolicy{
			Workload:         workload,
			MaxMessages:      16,
			MaxChars:         24000,
			MaxLocalMessages: 8,
			MaxLocalChars:    5000,
			DegradedChars:    18000,
			PreferredRoute:   "cloud",
			FallbackChain:    []string{"local"},
		}
	case WorkloadVision:
		return ProviderPolicy{
			Workload:         workload,
			MaxMessages:      12,
			MaxChars:         18000,
			MaxLocalMessages: 6,
			MaxLocalChars:    3500,
			DegradedChars:    12000,
			PreferredRoute:   "cloud",
			FallbackChain:    []string{"local"},
		}
	case WorkloadToolLoop:
		return ProviderPolicy{
			Workload:         workload,
			MaxMessages:      18,
			MaxChars:         28000,
			MaxLocalMessages: 8,
			MaxLocalChars:    5000,
			DegradedChars:    22000,
			PreferredRoute:   "cloud",
			FallbackChain:    []string{"local"},
		}
	default:
		return ProviderPolicy{
			Workload:         workload,
			MaxMessages:      20,
			MaxChars:         32000,
			MaxLocalMessages: 10,
			MaxLocalChars:    6000,
			DegradedChars:    24000,
			PreferredRoute:   "cloud",
			FallbackChain:    []string{"local"},
		}
	}
}

func inferWorkloadClass(messages []Message, tools []ToolDefinition, options map[string]interface{}) WorkloadClass {
	if HasVisionInput(messages) {
		return WorkloadVision
	}
	if len(tools) > 0 {
		return WorkloadToolLoop
	}
	feature, _ := options["feature"].(string)
	switch strings.ToLower(strings.TrimSpace(feature)) {
	case "heartbeat", "cron", "summarize":
		return WorkloadBackground
	case "reasoning", "monologue", "critique", "tot_branch", "tot_synthesis":
		return WorkloadReasoning
	case "transcription":
		return WorkloadTranscription
	default:
		return WorkloadChat
	}
}
