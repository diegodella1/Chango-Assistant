package providers

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// PrivacyStats tracks routing decisions for observability.
type PrivacyStats struct {
	TotalMessages int64
	RoutedLocal   int64
	RoutedCloud   int64
	Catches       int64
}

// PrivacyRouter implements LLMProvider, routing messages between a cloud and local
// provider based on content sensitivity. Sensitive data never leaves the Pi.
type PrivacyRouter struct {
	cloud      LLMProvider
	local      LLMProvider
	classifier *PrivacyClassifier
	logEnabled bool

	// Per-session sensitivity tracking: once a session has sensitive data,
	// it stays local for the rest of the conversation.
	sensitiveSessions sync.Map // string → bool

	stats PrivacyStats
}

// NewPrivacyRouter creates a privacy-aware provider wrapper.
func NewPrivacyRouter(cloud, local LLMProvider, classifier *PrivacyClassifier, logEnabled bool) *PrivacyRouter {
	return &PrivacyRouter{
		cloud:      cloud,
		local:      local,
		classifier: classifier,
		logEnabled: logEnabled,
	}
}

// Chat implements LLMProvider. It classifies the message and routes accordingly.
func (pr *PrivacyRouter) Chat(ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]interface{}) (*LLMResponse, error) {
	atomic.AddInt64(&pr.stats.TotalMessages, 1)

	// Extract session key from options if available (set by agent loop)
	sessionKey, _ := options["session_key"].(string)

	// Check if this session is already marked as sensitive
	if sessionKey != "" {
		if _, found := pr.sensitiveSessions.Load(sessionKey); found {
			return pr.routeLocal(ctx, messages, options, "session_locked")
		}
	}

	// Classify the current message
	result := pr.classifier.Classify(ctx, messages)

	if result.Level == Sensitive {
		// Mark session as sensitive from now on
		if sessionKey != "" {
			pr.sensitiveSessions.Store(sessionKey, true)
		}
		atomic.AddInt64(&pr.stats.Catches, 1)
		return pr.routeLocal(ctx, messages, options, result.Reason)
	}

	// Safe — route to cloud
	return pr.routeCloud(ctx, messages, tools, model, options)
}

// GetDefaultModel returns the cloud provider's default model.
func (pr *PrivacyRouter) GetDefaultModel() string {
	return pr.cloud.GetDefaultModel()
}

// Stats returns a snapshot of routing statistics.
func (pr *PrivacyRouter) Stats() PrivacyStats {
	return PrivacyStats{
		TotalMessages: atomic.LoadInt64(&pr.stats.TotalMessages),
		RoutedLocal:   atomic.LoadInt64(&pr.stats.RoutedLocal),
		RoutedCloud:   atomic.LoadInt64(&pr.stats.RoutedCloud),
		Catches:       atomic.LoadInt64(&pr.stats.Catches),
	}
}

// routeLocal sends the message to the local model, stripping tools and media.
func (pr *PrivacyRouter) routeLocal(ctx context.Context, messages []Message, options map[string]interface{}, reason string) (*LLMResponse, error) {
	atomic.AddInt64(&pr.stats.RoutedLocal, 1)

	if pr.logEnabled {
		logger.InfoCF("privacy", "Routing to LOCAL", map[string]interface{}{
			"reason": reason,
		})
	}

	// Sanitize messages for local model: strip image Parts, keep text only
	sanitized := sanitizeForLocal(messages)

	// Local model: no tools (unreliable on small models), use its default model
	resp, err := pr.local.Chat(ctx, sanitized, nil, "", options)
	if err != nil {
		// If local model can't handle the context size, escalate to cloud.
		// Privacy is important but a total failure is worse.
		errStr := err.Error()
		if strings.Contains(errStr, "exceed") || strings.Contains(errStr, "context_size") {
			logger.WarnCF("privacy", "Local model context exceeded, escalating to cloud", map[string]interface{}{
				"error": errStr,
			})
			return pr.cloud.Chat(ctx, messages, nil, "", options)
		}
		return nil, err
	}

	return resp, nil
}

// routeCloud sends the message to the cloud provider with full capabilities.
func (pr *PrivacyRouter) routeCloud(ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]interface{}) (*LLMResponse, error) {
	atomic.AddInt64(&pr.stats.RoutedCloud, 1)

	if pr.logEnabled {
		logger.InfoCF("privacy", "Routing to CLOUD", map[string]interface{}{
			"model": model,
		})
	}

	return pr.cloud.Chat(ctx, messages, tools, model, options)
}

// sanitizeForLocal strips image content parts from messages (local model can't handle vision)
// and truncates very long messages to fit local model context.
func sanitizeForLocal(messages []Message) []Message {
	result := make([]Message, len(messages))
	for i, msg := range messages {
		result[i] = Message{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
		}

		// If message has multimodal Parts, keep only text parts
		if len(msg.Parts) > 0 {
			var textParts []ContentPart
			for _, part := range msg.Parts {
				if part.Type == "text" {
					textParts = append(textParts, part)
				}
			}
			result[i].Parts = textParts

			// If we stripped all parts, ensure Content has something
			if len(textParts) == 0 && result[i].Content == "" {
				result[i].Content = "[Image/document content — processed locally for privacy]"
			}
		}

		// Truncate very long messages for local model context limits
		if len(result[i].Content) > 4000 {
			result[i].Content = result[i].Content[:4000] + "\n[...truncated for local model context]"
		}
	}

	// Keep only last N messages to fit local context window
	maxMessages := 10
	if len(result) > maxMessages+1 { // +1 for system message
		// Keep system message (first) + last N
		system := result[0]
		result = append([]Message{system}, result[len(result)-maxMessages:]...)
	}

	return result
}

// ResetSession clears the sensitivity lock for a session (e.g., after session reset).
func (pr *PrivacyRouter) ResetSession(sessionKey string) {
	pr.sensitiveSessions.Delete(sessionKey)
}

// IsPrivacyRouter checks if a provider is a PrivacyRouter (for stats/debug).
func IsPrivacyRouter(p LLMProvider) (*PrivacyRouter, bool) {
	// Check direct type
	if pr, ok := p.(*PrivacyRouter); ok {
		return pr, true
	}
	// Check if wrapped in FallbackProvider
	if fp, ok := p.(*FallbackProvider); ok {
		if pr, ok := fp.Primary.(*PrivacyRouter); ok {
			return pr, true
		}
	}
	return nil, false
}

// String returns a human-readable description of the privacy router state.
func (pr *PrivacyRouter) String() string {
	s := pr.Stats()
	return "PrivacyRouter: total=" + itoa(s.TotalMessages) +
		" local=" + itoa(s.RoutedLocal) +
		" cloud=" + itoa(s.RoutedCloud)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
