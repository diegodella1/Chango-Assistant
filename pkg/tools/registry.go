package tools

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/state"
)

type ToolRegistry struct {
	tools          map[string]Tool
	riskClasses    map[string]ToolRiskClass
	autonomyConfig config.AutonomyConfig
	stateManager   *state.Manager
	mu             sync.RWMutex
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:       make(map[string]Tool),
		riskClasses: make(map[string]ToolRiskClass),
	}
}

func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
	r.riskClasses[tool.Name()] = defaultRiskClass(tool.Name())
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *ToolRegistry) SetAutonomyPolicy(cfg config.AutonomyConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autonomyConfig = cfg
}

func (r *ToolRegistry) SetStateManager(sm *state.Manager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stateManager = sm
}

func (r *ToolRegistry) GetRiskClass(name string) ToolRiskClass {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if risk, ok := r.riskClasses[name]; ok {
		return risk
	}
	return defaultRiskClass(name)
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, args map[string]interface{}) *ToolResult {
	return r.ExecuteWithContext(ctx, name, args, "", "", nil)
}

// ExecuteWithContext executes a tool with channel/chatID context and optional async callback.
// If the tool implements AsyncTool and a non-nil callback is provided,
// the callback will be set on the tool before execution.
func (r *ToolRegistry) ExecuteWithContext(ctx context.Context, name string, args map[string]interface{}, channel, chatID string, asyncCallback AsyncCallback) *ToolResult {
	logger.InfoCF("tool", "Tool execution started",
		map[string]interface{}{
			"tool": name,
			"args": args,
		})

	tool, ok := r.Get(name)
	if !ok {
		logger.ErrorCF("tool", "Tool not found",
			map[string]interface{}{
				"tool": name,
			})
		return ErrorResult(fmt.Sprintf("tool %q not found", name)).WithError(fmt.Errorf("tool not found"))
	}

	risk := r.GetRiskClass(name)
	approved := boolArg(args, "approved")
	requiresApproval := boolArg(args, "require_approval")
	if err := r.enforcePolicy(name, risk, args); err != nil {
		logger.WarnCF("tool", "Tool execution blocked by autonomy policy",
			map[string]interface{}{
				"tool": name,
				"risk": risk,
			})
		r.appendAutonomyLog(state.AutonomyLogRecord{
			ID:               autonomyEventID(name, args, "blocked_policy"),
			Timestamp:        time.Now().UTC().Format(time.RFC3339),
			Tool:             name,
			Risk:             string(risk),
			Status:           "blocked_policy",
			Summary:          summarizeToolIntent(name, args),
			Reason:           stringArg(args, "reason"),
			Channel:          channel,
			ChatID:           chatID,
			Approved:         approved,
			RequiresApproval: requiresApproval,
			Error:            err.Error(),
		})
		return ErrorResult(err.Error()).WithError(err)
	}

	// If tool implements ContextualTool, set context
	if contextualTool, ok := tool.(ContextualTool); ok && channel != "" && chatID != "" {
		contextualTool.SetContext(channel, chatID)
	}

	// If tool implements AsyncTool and callback is provided, set callback
	if asyncTool, ok := tool.(AsyncTool); ok && asyncCallback != nil {
		asyncTool.SetCallback(asyncCallback)
		logger.DebugCF("tool", "Async callback injected",
			map[string]interface{}{
				"tool": name,
			})
	}

	// Global timeout safety net: prevent any tool from hanging forever.
	// Tools with their own timeout (like exec) will finish before this.
	// Council needs more time: 3 sequential LLM calls with tools.
	toolTimeout := constants.ToolExecutionTimeout
	if name == "council" {
		toolTimeout = constants.ToolExecutionTimeoutLong
	}
	toolCtx, toolCancel := context.WithTimeout(ctx, toolTimeout)
	defer toolCancel()

	start := time.Now()
	resultCh := make(chan *ToolResult, 1)
	// Known limitation: if the tool ignores context cancellation the goroutine will
	// leak until the tool returns on its own. We rely on toolCancel() (deferred above)
	// to signal cancellation promptly, but cannot force-kill the goroutine.
	go func() {
		resultCh <- tool.Execute(toolCtx, args)
	}()

	var result *ToolResult
	select {
	case result = <-resultCh:
		// Tool completed normally
	case <-toolCtx.Done():
		// Cancel context immediately so well-behaved tools can exit fast.
		toolCancel()
		result = ErrorResult(fmt.Sprintf("tool %q timed out after %v", name, toolTimeout))
	}
	duration := time.Since(start)

	// Log based on result type
	if !result.IsError {
		if verificationErr := verifyToolExecution(toolCtx, name, args); verificationErr != nil {
			result = ErrorResult(fmt.Sprintf("tool %q verification failed: %v", name, verificationErr)).WithError(verificationErr)
		}
	}

	verificationStatus := "not_required"
	if requiresVerification(name) {
		verificationStatus = "passed"
		if result.IsError && result.Err != nil && strings.Contains(strings.ToLower(result.Err.Error()), "verification failed") {
			verificationStatus = "failed"
		}
	}

	// Log based on result type
	if result.IsError {
		logger.ErrorCF("tool", "Tool execution failed",
			map[string]interface{}{
				"tool":     name,
				"duration": duration.Milliseconds(),
				"error":    result.ForLLM,
			})
	} else if result.Async {
		logger.InfoCF("tool", "Tool started (async)",
			map[string]interface{}{
				"tool":     name,
				"duration": duration.Milliseconds(),
			})
	} else {
		logger.InfoCF("tool", "Tool execution completed",
			map[string]interface{}{
				"tool":          name,
				"duration_ms":   duration.Milliseconds(),
				"result_length": len(result.ForLLM),
			})
	}

	finalStatus := "completed"
	if result.IsError {
		finalStatus = "failed"
		if verificationStatus == "failed" {
			finalStatus = "verification_failed"
		}
	} else if result.Async {
		finalStatus = "started_async"
	}
	r.appendAutonomyLog(state.AutonomyLogRecord{
		ID:               autonomyEventID(name, args, finalStatus),
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
		Tool:             name,
		Risk:             string(risk),
		Status:           finalStatus,
		Summary:          summarizeToolIntent(name, args),
		Reason:           stringArg(args, "reason"),
		ReferenceID:      stringArg(args, "id"),
		Channel:          channel,
		ChatID:           chatID,
		Approved:         approved,
		Async:            result.Async,
		DurationMs:       duration.Milliseconds(),
		Verification:     verificationStatus,
		Error:            resultError(result),
		RequiresApproval: requiresApproval,
	})

	return result
}

func (r *ToolRegistry) GetDefinitions() []map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	definitions := make([]map[string]interface{}, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, ToolToSchema(tool))
	}
	return definitions
}

// ToProviderDefs converts tool definitions to provider-compatible format.
// This is the format expected by LLM provider APIs.
func (r *ToolRegistry) ToProviderDefs() []providers.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	definitions := make([]providers.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		schema := ToolToSchema(tool)

		// Safely extract nested values with type checks
		fn, ok := schema["function"].(map[string]interface{})
		if !ok {
			continue
		}

		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]interface{})

		definitions = append(definitions, providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name:        name,
				Description: desc,
				Parameters:  params,
			},
		})
	}
	return definitions
}

// List returns a list of all registered tool names.
func (r *ToolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// GetSummaries returns human-readable summaries of all registered tools.
// Returns a slice of "name - description" strings.
func (r *ToolRegistry) GetSummaries() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	summaries := make([]string, 0, len(r.tools))
	for _, tool := range r.tools {
		summaries = append(summaries, fmt.Sprintf("- `%s` - %s", tool.Name(), tool.Description()))
	}
	return summaries
}

func (r *ToolRegistry) enforcePolicy(name string, risk ToolRiskClass, args map[string]interface{}) error {
	r.mu.RLock()
	cfg := r.autonomyConfig
	r.mu.RUnlock()

	if !cfg.Enabled {
		return nil
	}
	if boolArg(args, "approved") {
		return nil
	}
	if boolArg(args, "require_approval") {
		return fmt.Errorf("tool %q requires explicit approval", name)
	}
	for _, blockedRisk := range cfg.RequireApprovalForRisk {
		if strings.EqualFold(blockedRisk, string(risk)) {
			return fmt.Errorf("tool %q blocked by autonomy policy for risk class %q; re-run with approval", name, risk)
		}
	}
	return nil
}

func (r *ToolRegistry) appendAutonomyLog(entry state.AutonomyLogRecord) {
	r.mu.RLock()
	sm := r.stateManager
	r.mu.RUnlock()
	if sm == nil {
		return
	}
	if err := sm.AppendAutonomyLog(entry); err != nil {
		logger.WarnCF("tool", "Failed to append autonomy log", map[string]interface{}{
			"tool":  entry.Tool,
			"error": err.Error(),
		})
	}
}

func summarizeToolIntent(name string, args map[string]interface{}) string {
	fields := []string{name}
	if action := stringArg(args, "action"); action != "" {
		fields = append(fields, action)
	}
	for _, key := range []string{"title", "goal", "query", "topic", "command", "path", "url", "message", "text", "branch", "service", "name"} {
		if value := stringArg(args, key); value != "" {
			fields = append(fields, truncateToolField(value, 80))
			break
		}
	}
	return strings.Join(fields, " | ")
}

func truncateToolField(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}

func stringArg(args map[string]interface{}, key string) string {
	if value, ok := args[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func resultError(result *ToolResult) string {
	if result == nil {
		return ""
	}
	if result.Err != nil {
		return result.Err.Error()
	}
	if result.IsError {
		return result.ForLLM
	}
	return ""
}

func requiresVerification(name string) bool {
	switch name {
	case "deploy", "edit_file", "write_file", "append_file", "exec", "host_exec", "git":
		return true
	default:
		return false
	}
}

func autonomyEventID(name string, args map[string]interface{}, status string) string {
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	parts = append(parts, name, status)
	for _, key := range keys {
		parts = append(parts, key+"="+fmt.Sprint(args[key]))
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:8])
}

func defaultRiskClass(name string) ToolRiskClass {
	switch name {
	case "read_file", "list_dir", "web_search", "web_fetch", "weather", "agenda", "memory", "telemetry":
		return RiskReadOnly
	case "write_file", "append_file", "edit_file", "tasks", "reminder", "snippet":
		return RiskLowRiskWrite
	case "git", "self", "credentials", "host_exec":
		return RiskSensitiveWrite
	case "deploy", "message", "gmail", "calendar", "gdrive", "wallet":
		return RiskExternalSideEffect
	case "exec":
		return RiskDestructive
	default:
		return RiskReadOnly
	}
}

func boolArg(args map[string]interface{}, key string) bool {
	value, ok := args[key]
	if !ok {
		return false
	}
	boolean, ok := value.(bool)
	return ok && boolean
}

func verifyToolExecution(ctx context.Context, name string, args map[string]interface{}) error {
	switch name {
	case "deploy":
		action, _ := args["action"].(string)
		if action != "deploy" {
			return nil
		}
		app, _ := args["app"].(string)
		healthURL := appHealthURLs[app]
		if healthURL == "" {
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("health check returned HTTP %d", resp.StatusCode)
		}
	}
	return nil
}
