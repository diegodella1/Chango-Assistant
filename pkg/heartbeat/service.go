// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package heartbeat

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	minIntervalMinutes     = 5
	defaultIntervalMinutes = 30
)

// HeartbeatHandler is the function type for handling heartbeat.
// It returns a ToolResult that can indicate async operations.
// channel and chatID are derived from the last active user channel.
type HeartbeatHandler func(prompt, channel, chatID string) *tools.ToolResult

// HeartbeatService manages periodic heartbeat checks
type HeartbeatService struct {
	workspace string
	bus       *bus.MessageBus
	state     *state.Manager
	handler   HeartbeatHandler
	interval  time.Duration
	enabled   bool
	mu        sync.RWMutex
	stopChan  chan struct{}
	executing atomic.Bool
	onEvent   func(string) // SSE event callback for neural visualization
}

// NewHeartbeatService creates a new heartbeat service
func NewHeartbeatService(workspace string, intervalMinutes int, enabled bool) *HeartbeatService {
	// Apply minimum interval
	if intervalMinutes < minIntervalMinutes && intervalMinutes != 0 {
		intervalMinutes = minIntervalMinutes
	}

	if intervalMinutes == 0 {
		intervalMinutes = defaultIntervalMinutes
	}

	return &HeartbeatService{
		workspace: workspace,
		interval:  time.Duration(intervalMinutes) * time.Minute,
		enabled:   enabled,
		state:     state.NewManager(workspace),
	}
}

// SetBus sets the message bus for delivering heartbeat results.
func (hs *HeartbeatService) SetBus(msgBus *bus.MessageBus) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.bus = msgBus
}

// SetEventCallback sets the SSE event callback for neural visualization.
func (hs *HeartbeatService) SetEventCallback(fn func(string)) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.onEvent = fn
}

// SetHandler sets the heartbeat handler.
func (hs *HeartbeatService) SetHandler(handler HeartbeatHandler) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.handler = handler
}

// Start begins the heartbeat service
func (hs *HeartbeatService) Start() error {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	if hs.stopChan != nil {
		logger.InfoC("heartbeat", "Heartbeat service already running")
		return nil
	}

	if !hs.enabled {
		logger.InfoC("heartbeat", "Heartbeat service disabled")
		return nil
	}

	hs.stopChan = make(chan struct{})
	go hs.runLoop(hs.stopChan)

	logger.InfoCF("heartbeat", "Heartbeat service started", map[string]any{
		"interval_minutes": hs.interval.Minutes(),
	})

	return nil
}

// Stop gracefully stops the heartbeat service
func (hs *HeartbeatService) Stop() {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	if hs.stopChan == nil {
		return
	}

	logger.InfoC("heartbeat", "Stopping heartbeat service")
	close(hs.stopChan)
	hs.stopChan = nil
}

// IsRunning returns whether the service is running
func (hs *HeartbeatService) IsRunning() bool {
	hs.mu.RLock()
	defer hs.mu.RUnlock()
	return hs.stopChan != nil
}

// runLoop runs the heartbeat ticker
func (hs *HeartbeatService) runLoop(stopChan chan struct{}) {
	ticker := time.NewTicker(hs.interval)
	defer ticker.Stop()

	// Run first heartbeat after initial delay
	time.AfterFunc(time.Second, func() {
		defer func() {
			if r := recover(); r != nil {
				hs.logError("Initial heartbeat panic recovered: %v", r)
			}
		}()
		hs.executeHeartbeat()
	})

	for {
		select {
		case <-stopChan:
			return
		case <-ticker.C:
			hs.executeHeartbeat()
		}
	}
}

// executeHeartbeat performs a single heartbeat check
func (hs *HeartbeatService) executeHeartbeat() {
	// Emit SSE event for neural visualization
	if hs.onEvent != nil {
		hs.onEvent("heartbeat")
	}

	// Prevent overlapping executions
	if !hs.executing.CompareAndSwap(false, true) {
		hs.logInfo("Heartbeat still running, skipping this tick")
		return
	}
	defer hs.executing.Store(false)

	// Panic recovery to prevent silent goroutine death
	defer func() {
		if r := recover(); r != nil {
			hs.logError("Heartbeat panic recovered: %v", r)
		}
	}()

	hs.mu.RLock()
	enabled := hs.enabled
	handler := hs.handler
	if !hs.enabled || hs.stopChan == nil {
		hs.mu.RUnlock()
		return
	}
	hs.mu.RUnlock()

	if !enabled {
		return
	}

	logger.DebugC("heartbeat", "Executing heartbeat")

	// Get last channel info for context
	lastChannel := hs.state.GetLastChannel()
	channel, chatID := hs.parseLastChannel(lastChannel)

	// Debug log for channel resolution
	hs.logInfo("Resolved channel: %s, chatID: %s (from lastChannel: %s)", channel, chatID, lastChannel)

	followUpContext := hs.processDueFollowUps(channel, chatID)

	prompt := hs.buildPrompt()
	if prompt == "" {
		if followUpContext == "" {
			logger.InfoC("heartbeat", "No heartbeat prompt (HEARTBEAT.md empty or missing)")
			return
		}
		prompt = fmt.Sprintf(`# Heartbeat Check

Current time: %s

You are a proactive AI assistant. This is a scheduled heartbeat check.
Review the autonomous follow-up state below and take any necessary action.
If there is nothing that requires attention, respond ONLY with: HEARTBEAT_OK

%s
`, time.Now().Format("2006-01-02 15:04:05"), followUpContext)
	} else if followUpContext != "" {
		prompt = strings.TrimSpace(prompt) + "\n\n## Autonomous Follow-ups\n\n" + followUpContext + "\n"
	}

	if handler == nil {
		hs.logError("Heartbeat handler not configured")
		return
	}

	result := handler(prompt, channel, chatID)

	if result == nil {
		hs.logInfo("Heartbeat handler returned nil result")
		return
	}

	// Handle different result types
	if result.IsError {
		hs.logError("Heartbeat error: %s", result.ForLLM)
		return
	}

	if result.Async {
		hs.logInfo("Async task started: %s", result.ForLLM)
		logger.InfoCF("heartbeat", "Async heartbeat task started",
			map[string]interface{}{
				"message": result.ForLLM,
			})
		return
	}

	// Check if silent
	if result.Silent {
		hs.logInfo("Heartbeat OK - silent")
		return
	}

	// Send result to user
	if result.ForUser != "" {
		hs.sendResponse(result.ForUser)
	} else if result.ForLLM != "" {
		hs.sendResponse(result.ForLLM)
	}

	hs.logInfo("Heartbeat completed: %s", result.ForLLM)
}

// buildPrompt builds the heartbeat prompt from HEARTBEAT.md
func (hs *HeartbeatService) buildPrompt() string {
	heartbeatPath := filepath.Join(hs.workspace, "HEARTBEAT.md")

	data, err := os.ReadFile(heartbeatPath)
	if err != nil {
		if os.IsNotExist(err) {
			hs.createDefaultHeartbeatTemplate()
			return ""
		}
		hs.logError("Error reading HEARTBEAT.md: %v", err)
		return ""
	}

	content := string(data)
	if len(content) == 0 {
		return ""
	}

	now := time.Now().Format("2006-01-02 15:04:05")
	return fmt.Sprintf(`# Heartbeat Check

Current time: %s

You are a proactive AI assistant. This is a scheduled heartbeat check.
Review the following tasks and execute any necessary actions using available skills.
If there is nothing that requires attention, respond ONLY with: HEARTBEAT_OK

%s
`, now, content)
}

// createDefaultHeartbeatTemplate creates the default HEARTBEAT.md file
func (hs *HeartbeatService) createDefaultHeartbeatTemplate() {
	heartbeatPath := filepath.Join(hs.workspace, "HEARTBEAT.md")

	defaultContent := `# Heartbeat Check List

This file contains tasks for the heartbeat service to check periodically.

## Examples

- Check for unread messages
- Review upcoming calendar events
- Check device status (e.g., MaixCam)

## Instructions

- Execute ALL tasks listed below. Do NOT skip any task.
- For simple tasks (e.g., report current time), respond directly.
- For complex tasks that may take time, use the spawn tool to create a subagent.
- The spawn tool is async - subagent results will be sent to the user automatically.
- After spawning a subagent, CONTINUE to process remaining tasks.
- Only respond with HEARTBEAT_OK when ALL tasks are done AND nothing needs attention.

---

Add your heartbeat tasks below this line:
`

	if err := os.WriteFile(heartbeatPath, []byte(defaultContent), 0644); err != nil {
		hs.logError("Failed to create default HEARTBEAT.md: %v", err)
	} else {
		hs.logInfo("Created default HEARTBEAT.md template")
	}
}

// sendResponse sends the heartbeat response to the last channel
func (hs *HeartbeatService) sendResponse(response string) {
	hs.mu.RLock()
	msgBus := hs.bus
	hs.mu.RUnlock()

	if msgBus == nil {
		hs.logInfo("No message bus configured, heartbeat result not sent")
		return
	}

	// Get last channel from state
	lastChannel := hs.state.GetLastChannel()
	if lastChannel == "" {
		hs.logInfo("No last channel recorded, heartbeat result not sent")
		return
	}

	platform, userID := hs.parseLastChannel(lastChannel)

	// Skip internal channels that can't receive messages
	if platform == "" || userID == "" {
		return
	}

	msgBus.PublishOutbound(bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: response,
	})

	hs.logInfo("Heartbeat result sent to %s", platform)
}

// parseLastChannel parses the last channel string into platform and userID.
// Returns empty strings for invalid or internal channels.
func (hs *HeartbeatService) parseLastChannel(lastChannel string) (platform, userID string) {
	if lastChannel == "" {
		return "", ""
	}

	// Parse channel format: "platform:user_id" (e.g., "telegram:123456")
	parts := strings.SplitN(lastChannel, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		hs.logError("Invalid last channel format: %s", lastChannel)
		return "", ""
	}

	platform, userID = parts[0], parts[1]

	// Skip internal channels
	if constants.IsInternalChannel(platform) {
		hs.logInfo("Skipping internal channel: %s", platform)
		return "", ""
	}

	return platform, userID
}

func (hs *HeartbeatService) processDueFollowUps(channel, chatID string) string {
	followUps := hs.state.GetFollowUps()
	if len(followUps) == 0 {
		return ""
	}

	tasks := hs.state.GetTasks()
	taskIndex := make(map[string]int, len(tasks))
	for i := range tasks {
		taskIndex[tasks[i].ID] = i
	}

	now := time.Now().UTC()
	changed := false
	var summary []string

	for i := range followUps {
		followUp := &followUps[i]
		if followUp.Status == "done" || followUp.Status == "cancelled" {
			continue
		}
		if followUp.NextCheckAt == "" {
			continue
		}

		nextCheck, err := time.Parse(time.RFC3339, followUp.NextCheckAt)
		if err != nil || nextCheck.After(now) {
			continue
		}

		followUp.AttemptCount++
		followUp.LastCheckedAt = now.Format(time.RFC3339)
		followUp.UpdatedAt = now.Format(time.RFC3339)
		changed = true

		taskIdx, ok := taskIndex[followUp.ReferenceID]
		if !ok {
			followUp.Status = "cancelled"
			followUp.LastOutcome = "reference_missing"
			followUp.NextCheckAt = ""
			hs.appendAutonomyEvent("followup_reference_missing", followUp, channel, chatID, "referenced task no longer exists")
			continue
		}

		task := &tasks[taskIdx]
		task.UpdatedAt = now.Format(time.RFC3339)

		switch task.Status {
		case "done", "cancelled":
			followUp.Status = "done"
			followUp.LastOutcome = "closed"
			followUp.NextCheckAt = ""
			hs.appendAutonomyEvent("followup_closed", followUp, channel, chatID, "referenced task already closed")
		case "waiting_external":
			followUp.Status = "waiting_external"
			followUp.LastOutcome = "approval_required"
			followUp.NextCheckAt = now.Add(12 * time.Hour).Format(time.RFC3339)
			if followUp.AttemptCount >= 2 {
				followUp.EscalatedAt = now.Format(time.RFC3339)
				summary = append(summary, fmt.Sprintf("- approval still needed: %s", followUp.Title))
				hs.appendAutonomyEvent("followup_escalated", followUp, channel, chatID, "still waiting on external approval")
			} else {
				hs.appendAutonomyEvent("followup_waiting", followUp, channel, chatID, "waiting on external input")
			}
		case "blocked", "verification_failed":
			if followUp.AttemptCount >= 3 {
				task.Status = "waiting_external"
				if !strings.Contains(task.Notes, "Escalated by follow-up engine") {
					if strings.TrimSpace(task.Notes) != "" {
						task.Notes += "\n"
					}
					task.Notes += "Escalated by follow-up engine after repeated blocked checks."
				}
				followUp.Status = "waiting_external"
				followUp.LastOutcome = "escalated_for_review"
				followUp.EscalatedAt = now.Format(time.RFC3339)
				followUp.NextCheckAt = now.Add(12 * time.Hour).Format(time.RFC3339)
				summary = append(summary, fmt.Sprintf("- blocked goal escalated: %s", followUp.Title))
				hs.appendAutonomyEvent("followup_escalated", followUp, channel, chatID, "blocked goal escalated for review")
			} else {
				followUp.Status = task.Status
				followUp.LastOutcome = "retry_scheduled"
				followUp.NextCheckAt = now.Add(backoffForAttempt(followUp.AttemptCount)).Format(time.RFC3339)
				hs.appendAutonomyEvent("followup_retry_scheduled", followUp, channel, chatID, "blocked goal scheduled for retry")
			}
		default:
			followUp.Status = task.Status
			followUp.LastOutcome = "monitoring"
			followUp.NextCheckAt = now.Add(backoffForAttempt(followUp.AttemptCount)).Format(time.RFC3339)
			hs.appendAutonomyEvent("followup_monitoring", followUp, channel, chatID, "goal still active")
		}
	}

	if changed {
		if err := hs.state.ReplaceTasks(tasks); err != nil {
			hs.logError("Failed to save follow-up task state: %v", err)
		}
		if err := hs.state.ReplaceFollowUps(followUps); err != nil {
			hs.logError("Failed to save follow-ups: %v", err)
		}
	}

	if len(summary) == 0 {
		return ""
	}
	sort.Strings(summary)
	if len(summary) > 5 {
		summary = summary[:5]
	}
	return strings.Join(summary, "\n")
}

func (hs *HeartbeatService) appendAutonomyEvent(status string, followUp *state.FollowUpRecord, channel, chatID, reason string) {
	if followUp == nil {
		return
	}
	entry := state.AutonomyLogRecord{
		ID:          fmt.Sprintf("%s:%s:%d", status, followUp.ID, followUp.AttemptCount),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Tool:        "followup_engine",
		Risk:        "operational",
		Status:      status,
		Summary:     followUp.Title,
		Reason:      reason,
		ReferenceID: followUp.ReferenceID,
		Channel:     channel,
		ChatID:      chatID,
	}
	if err := hs.state.AppendAutonomyLog(entry); err != nil {
		hs.logError("Failed to append autonomy event: %v", err)
	}
}

func backoffForAttempt(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 2 * time.Hour
	case attempt == 2:
		return 4 * time.Hour
	case attempt == 3:
		return 8 * time.Hour
	default:
		return 12 * time.Hour
	}
}

// logInfo logs an informational message to the heartbeat log
func (hs *HeartbeatService) logInfo(format string, args ...any) {
	hs.log("INFO", format, args...)
}

// logError logs an error message to the heartbeat log
func (hs *HeartbeatService) logError(format string, args ...any) {
	hs.log("ERROR", format, args...)
}

// log writes a message to the heartbeat log file
func (hs *HeartbeatService) log(level, format string, args ...any) {
	logFile := filepath.Join(hs.workspace, "heartbeat.log")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(f, "[%s] [%s] %s\n", timestamp, level, fmt.Sprintf(format, args...))
}
