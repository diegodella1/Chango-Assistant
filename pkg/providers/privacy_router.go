package providers

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/telemetry"
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
	cloud                 LLMProvider
	local                 LLMProvider
	classifier            *PrivacyClassifier
	logEnabled            bool
	allowCloudVisionMedia bool

	// Per-session sensitivity tracking with TTL: sensitive sessions decay
	// after a period of inactivity or message count.
	sensitiveSessions sync.Map // string → *sensitiveSession

	stats     PrivacyStats
	runtimeMu sync.RWMutex
	runtime   RouterRuntimeStatus
}

type RouterRuntimeStatus struct {
	Workload            string `json:"workload,omitempty"`
	LastRoute           string `json:"last_route,omitempty"`
	LastProvider        string `json:"last_provider,omitempty"`
	LastModel           string `json:"last_model,omitempty"`
	LastReason          string `json:"last_reason,omitempty"`
	Degraded            bool   `json:"degraded"`
	LastError           string `json:"last_error,omitempty"`
	LastErrorClass      string `json:"last_error_class,omitempty"`
	LastErrorAt         string `json:"last_error_at,omitempty"`
	LastSuccessAt       string `json:"last_success_at,omitempty"`
	ConsecutiveFailures int64  `json:"consecutive_failures"`
}

// sensitiveSession tracks when and why a session was marked sensitive.
type sensitiveSession struct {
	MarkedAt time.Time
	MsgCount int // messages since marked
	Reason   string
}

const (
	sensitiveSessionTTL     = 30 * time.Minute // decay after 30 min
	sensitiveSessionMaxMsgs = 5                // decay after 5 messages
)

// NewPrivacyRouter creates a privacy-aware provider wrapper.
func NewPrivacyRouter(cloud, local LLMProvider, classifier *PrivacyClassifier, logEnabled, allowCloudVisionMedia bool) *PrivacyRouter {
	return &PrivacyRouter{
		cloud:                 cloud,
		local:                 local,
		classifier:            classifier,
		logEnabled:            logEnabled,
		allowCloudVisionMedia: allowCloudVisionMedia,
	}
}

// Chat implements LLMProvider. It classifies the message and routes accordingly.
func (pr *PrivacyRouter) Chat(ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]interface{}) (*LLMResponse, error) {
	atomic.AddInt64(&pr.stats.TotalMessages, 1)
	policy := policyForOptions(messages, tools, options)
	messages, degraded := applyPolicyGuardrails(messages, policy)
	pr.setDegraded(degraded, string(policy.Workload))
	hasVisionInput := HasVisionInput(messages)
	cloudCaps := ResolveCapabilities(pr.cloud, model)

	// Extract session key from options if available (set by agent loop)
	sessionKey, _ := options["session_key"].(string)

	// Check if this session is marked as sensitive (with TTL decay)
	if sessionKey != "" {
		if val, found := pr.sensitiveSessions.Load(sessionKey); found {
			ss := val.(*sensitiveSession)
			ss.MsgCount++

			// Decay: unlock session after TTL or message count
			if time.Since(ss.MarkedAt) > sensitiveSessionTTL || ss.MsgCount > sensitiveSessionMaxMsgs {
				pr.sensitiveSessions.Delete(sessionKey)
				logger.InfoCF("privacy", "Session sensitivity expired", map[string]interface{}{
					"session": sessionKey,
					"reason":  ss.Reason,
					"msgs":    ss.MsgCount,
				})
				// Fall through to normal classification
			} else {
				// Still locked — but if tools are requested, escalate to cloud
				// (local model can't use tools reliably)
				if len(tools) > 0 || pr.shouldRouteVisionToCloud(hasVisionInput, cloudCaps) {
					logger.InfoCF("privacy", "Session locked but tools needed — escalating to cloud", map[string]interface{}{
						"session": sessionKey,
					})
					return pr.routeCloud(ctx, messages, tools, model, options, policy, "session_locked_with_tools")
				}
				return pr.routeLocal(ctx, messages, options, policy, "session_locked")
			}
		}
	}

	// Classify the current message
	result := pr.classifier.Classify(ctx, messages)

	if result.Level == Sensitive {
		if result.Reason == "image_detected" && pr.shouldRouteVisionToCloud(hasVisionInput, cloudCaps) {
			logger.InfoCF("privacy", "Image turn allowed to use cloud vision", map[string]interface{}{
				"model":    cloudCaps.Model,
				"provider": cloudCaps.Provider,
			})
			return pr.routeCloud(ctx, messages, tools, model, options, policy, "cloud_vision_allowed")
		}

		// Mark session as sensitive with decay tracking
		if sessionKey != "" {
			pr.sensitiveSessions.Store(sessionKey, &sensitiveSession{
				MarkedAt: time.Now(),
				MsgCount: 0,
				Reason:   result.Reason,
			})
		}
		atomic.AddInt64(&pr.stats.Catches, 1)

		// Even for sensitive content: if tools are requested, escalate to cloud.
		// The local model (Qwen 0.5B) can't generate tool calls reliably.
		// Trade-off: privacy vs functionality — functionality wins when tools are needed.
		if len(tools) > 0 {
			logger.WarnCF("privacy", "Sensitive content detected but tools needed — routing to cloud", map[string]interface{}{
				"reason": result.Reason,
				"score":  result.Score,
			})
			return pr.routeCloud(ctx, messages, tools, model, options, policy, "sensitive_with_tools")
		}

		return pr.routeLocal(ctx, messages, options, policy, result.Reason)
	}

	// Safe — route to cloud
	return pr.routeCloud(ctx, messages, tools, model, options, policy, "safe")
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
func (pr *PrivacyRouter) routeLocal(ctx context.Context, messages []Message, options map[string]interface{}, policy ProviderPolicy, reason string) (*LLMResponse, error) {
	atomic.AddInt64(&pr.stats.RoutedLocal, 1)
	start := time.Now()

	localCaps := ResolveCapabilities(pr.local, "")
	hasVisionInput := HasVisionInput(messages)

	if pr.logEnabled {
		fields := map[string]interface{}{
			"reason":   reason,
			"model":    localCaps.Model,
			"provider": localCaps.Provider,
		}
		if hasVisionInput {
			fields["vision_support"] = string(localCaps.Vision)
		}
		logger.InfoCF("privacy", "Routing to LOCAL", fields)
	}

	if hasVisionInput && localCaps.Vision == CapabilityUnsupported {
		logger.WarnCF("privacy", "Image kept local but local model has no vision", map[string]interface{}{
			"reason":         reason,
			"provider":       localCaps.Provider,
			"model":          localCaps.Model,
			"vision_support": string(localCaps.Vision),
		})
	}

	// Sanitize messages for local model: strip image Parts, keep text only
	sanitized := sanitizeForLocal(messages)
	sanitized = trimMessagesForLocalPolicy(sanitized, policy)

	// Local model: no tools (unreliable on small models), use its default model
	resp, err := pr.local.Chat(ctx, sanitized, nil, "", options)
	if err != nil {
		pr.recordRouteResult(options, string(policy.Workload), "local", localCaps.Provider, localCaps.Model, start, err, false, reason)
		// If local model can't handle the context size, escalate to cloud.
		// Privacy is important but a total failure is worse.
		errStr := err.Error()
		if strings.Contains(errStr, "exceed") || strings.Contains(errStr, "context_size") {
			logger.WarnCF("privacy", "Local model context exceeded, escalating to cloud", map[string]interface{}{
				"error": errStr,
			})
			resp, cloudErr := pr.cloud.Chat(ctx, messages, nil, "", options)
			pr.recordRouteResult(options, string(policy.Workload), "cloud", ResolveCapabilities(pr.cloud, "").Provider, ResolveCapabilities(pr.cloud, "").Model, start, cloudErr, true, "local_context_escalation")
			return resp, cloudErr
		}
		return nil, err
	}
	pr.recordRouteResult(options, string(policy.Workload), "local", localCaps.Provider, localCaps.Model, start, nil, false, reason)

	if hasVisionInput && localCaps.Vision == CapabilityUnsupported {
		resp.Content = strings.TrimSpace(VisionUnsupportedNotice(localCaps.Model) + "\n\n" + resp.Content)
	}

	return resp, nil
}

func (pr *PrivacyRouter) shouldRouteVisionToCloud(hasVisionInput bool, cloudCaps ModelCapabilities) bool {
	return hasVisionInput &&
		pr.allowCloudVisionMedia &&
		cloudCaps.Vision == CapabilitySupported
}

// routeCloud sends the message to the cloud provider with full capabilities.
func (pr *PrivacyRouter) routeCloud(ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]interface{}, policy ProviderPolicy, reason string) (*LLMResponse, error) {
	atomic.AddInt64(&pr.stats.RoutedCloud, 1)
	start := time.Now()

	cloudCaps := ResolveCapabilities(pr.cloud, model)
	hasVisionInput := HasVisionInput(messages)

	if pr.logEnabled {
		fields := map[string]interface{}{
			"model":    cloudCaps.Model,
			"provider": cloudCaps.Provider,
		}
		if hasVisionInput {
			fields["vision_support"] = string(cloudCaps.Vision)
		}
		logger.InfoCF("privacy", "Routing to CLOUD", fields)
	}

	if hasVisionInput && cloudCaps.Vision == CapabilityUnsupported {
		logger.WarnCF("privacy", "Image routed to cloud model without vision support", map[string]interface{}{
			"provider":       cloudCaps.Provider,
			"model":          cloudCaps.Model,
			"vision_support": string(cloudCaps.Vision),
		})
	}

	resp, err := pr.cloud.Chat(ctx, messages, tools, model, options)
	if err != nil {
		pr.recordRouteResult(options, string(policy.Workload), "cloud", cloudCaps.Provider, cloudCaps.Model, start, err, false, reason)
		return nil, err
	}
	pr.recordRouteResult(options, string(policy.Workload), "cloud", cloudCaps.Provider, cloudCaps.Model, start, nil, false, reason)
	if hasVisionInput && cloudCaps.Vision == CapabilityUnsupported {
		resp.Content = strings.TrimSpace(VisionUnsupportedNotice(cloudCaps.Model) + "\n\n" + resp.Content)
	}
	return resp, nil
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

func (pr *PrivacyRouter) RuntimeStatus() RouterRuntimeStatus {
	pr.runtimeMu.RLock()
	defer pr.runtimeMu.RUnlock()
	return pr.runtime
}

func (pr *PrivacyRouter) setDegraded(degraded bool, workload string) {
	pr.runtimeMu.Lock()
	defer pr.runtimeMu.Unlock()
	pr.runtime.Degraded = degraded
	pr.runtime.Workload = workload
}

func (pr *PrivacyRouter) recordRouteResult(options map[string]interface{}, workload, route, provider, model string, started time.Time, err error, fallback bool, reason string) {
	errClass := ""
	if err != nil {
		errClass = classifyProviderError(err)
	}
	if tracker, ok := options["telemetry_tracker"].(*telemetry.Tracker); ok && tracker != nil {
		tracker.RecordProviderCall(provider, model, workload, route, time.Since(started), err == nil, fallback, errClass)
	}

	pr.runtimeMu.Lock()
	defer pr.runtimeMu.Unlock()
	pr.runtime.Workload = workload
	pr.runtime.LastRoute = route
	pr.runtime.LastProvider = provider
	pr.runtime.LastModel = model
	pr.runtime.LastReason = reason
	if err != nil {
		pr.runtime.LastError = err.Error()
		pr.runtime.LastErrorClass = errClass
		pr.runtime.LastErrorAt = time.Now().Format(time.RFC3339)
		pr.runtime.ConsecutiveFailures++
		return
	}
	pr.runtime.LastError = ""
	pr.runtime.LastErrorClass = ""
	pr.runtime.LastSuccessAt = time.Now().Format(time.RFC3339)
	pr.runtime.ConsecutiveFailures = 0
}

func applyPolicyGuardrails(messages []Message, policy ProviderPolicy) ([]Message, bool) {
	if len(messages) == 0 {
		return messages, false
	}
	degraded := false
	trimmed := messages
	if policy.MaxMessages > 0 && len(trimmed) > policy.MaxMessages {
		degraded = true
		system := trimmed[0]
		trimmed = append([]Message{system}, trimmed[len(trimmed)-(policy.MaxMessages-1):]...)
	}

	totalChars := 0
	for _, msg := range trimmed {
		totalChars += len(msg.Content)
		for _, part := range msg.Parts {
			totalChars += len(part.Text)
			if part.ImageURL != nil {
				totalChars += len(part.ImageURL.URL)
			}
		}
	}
	if policy.MaxChars > 0 && totalChars > policy.MaxChars {
		degraded = true
		overflow := totalChars - policy.MaxChars
		for i := len(trimmed) - 1; i >= 1 && overflow > 0; i-- {
			if len(trimmed[i].Content) == 0 {
				continue
			}
			if len(trimmed[i].Content) > 800 {
				cut := len(trimmed[i].Content) - 800
				trimmed[i].Content = trimmed[i].Content[:800] + "\n[...context trimmed for runtime budget]"
				overflow -= cut
			}
		}
	}
	if policy.DegradedChars > 0 && totalChars > policy.DegradedChars {
		degraded = true
	}
	return trimmed, degraded
}

func trimMessagesForLocalPolicy(messages []Message, policy ProviderPolicy) []Message {
	if len(messages) == 0 {
		return messages
	}
	result := messages
	if policy.MaxLocalMessages > 0 && len(result) > policy.MaxLocalMessages+1 {
		system := result[0]
		result = append([]Message{system}, result[len(result)-policy.MaxLocalMessages:]...)
	}
	totalChars := 0
	for i := range result {
		totalChars += len(result[i].Content)
	}
	if policy.MaxLocalChars > 0 && totalChars > policy.MaxLocalChars {
		for i := len(result) - 1; i >= 1 && totalChars > policy.MaxLocalChars; i-- {
			if len(result[i].Content) <= 500 {
				continue
			}
			removed := len(result[i].Content) - 500
			result[i].Content = result[i].Content[:500] + "\n[...trimmed for local context]"
			totalChars -= removed
		}
	}
	return result
}

func classifyProviderError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "429"), strings.Contains(msg, "rate limit"), strings.Contains(msg, "usage limit"):
		return "rate_limit"
	case strings.Contains(msg, "402"), strings.Contains(msg, "insufficient credits"), strings.Contains(msg, "quota"):
		return "quota"
	case strings.Contains(msg, "413"), strings.Contains(msg, "context"), strings.Contains(msg, "request too large"), strings.Contains(msg, "exceed"):
		return "context_pressure"
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "timeout"):
		return "timeout"
	default:
		return "provider_error"
	}
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
