package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// WorkflowStep defines a single step in a workflow.
type WorkflowStep struct {
	Tool   string                 `json:"tool"`
	Args   map[string]interface{} `json:"args"`
	OnFail string                 `json:"on_fail"` // "abort", "retry", "skip"
	Label  string                 `json:"label"`
}

// StepResult captures the outcome of executing a single workflow step.
type StepResult struct {
	StepIndex int    `json:"step_index"`
	Tool      string `json:"tool"`
	Status    string `json:"status"` // "success", "failed", "skipped", "retrying"
	Output    string `json:"output"`
	Error     string `json:"error,omitempty"`
	Duration  string `json:"duration"`
}

// Workflow represents a multi-step task execution with state.
type Workflow struct {
	Name      string         `json:"name"`
	Steps     []WorkflowStep `json:"steps"`
	Results   []StepResult   `json:"results"`
	Status    string         `json:"status"` // "running", "completed", "failed", "aborted"
	StartedAt string         `json:"started_at"`
	EndedAt   string         `json:"ended_at,omitempty"`
	Error     string         `json:"error,omitempty"`
}

const maxWorkflows = 20

// WorkflowTool enables multi-step task execution with state persistence and error recovery.
type WorkflowTool struct {
	filePath string
	registry *ToolRegistry
	mu       sync.Mutex
}

// NewWorkflowTool creates a new WorkflowTool that persists state under workspace/state/.
func NewWorkflowTool(workspace string, registry *ToolRegistry) *WorkflowTool {
	dir := filepath.Join(workspace, "state")
	os.MkdirAll(dir, 0755)
	return &WorkflowTool{
		filePath: filepath.Join(dir, "workflows.json"),
		registry: registry,
	}
}

func (t *WorkflowTool) Name() string { return "workflow" }

func (t *WorkflowTool) Description() string {
	return "Multi-step workflow engine. Create and execute workflows that chain multiple tools together with error recovery. Use action 'create' to define and run a workflow, 'status' to check a workflow, 'list' to see recent workflows."
}

func (t *WorkflowTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"create", "status", "list"},
				"description": "Action to perform",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Workflow name/identifier (required for create and status)",
			},
			"steps": map[string]interface{}{
				"type":        "array",
				"description": "Ordered list of steps to execute (required for create)",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"tool": map[string]interface{}{
							"type":        "string",
							"description": "Tool name to call",
						},
						"args": map[string]interface{}{
							"type":        "object",
							"description": "Arguments for the tool",
						},
						"on_fail": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"abort", "retry", "skip"},
							"description": "Failure strategy: abort (default), retry (up to 3x with backoff), skip",
						},
						"label": map[string]interface{}{
							"type":        "string",
							"description": "Human-readable step label",
						},
					},
					"required": []string{"tool"},
				},
			},
		},
		"required": []string{"action"},
	}
}

func (t *WorkflowTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "create":
		return t.create(ctx, args)
	case "status":
		return t.status(args)
	case "list":
		return t.list()
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *WorkflowTool) create(ctx context.Context, args map[string]interface{}) *ToolResult {
	name, _ := args["name"].(string)
	if name == "" {
		return ErrorResult("name is required for create")
	}

	rawSteps, ok := args["steps"].([]interface{})
	if !ok || len(rawSteps) == 0 {
		return ErrorResult("steps array is required for create (at least 1 step)")
	}

	// Parse steps
	steps := make([]WorkflowStep, 0, len(rawSteps))
	for i, raw := range rawSteps {
		stepMap, ok := raw.(map[string]interface{})
		if !ok {
			return ErrorResult(fmt.Sprintf("step %d: invalid format, expected object", i))
		}

		toolName, _ := stepMap["tool"].(string)
		if toolName == "" {
			return ErrorResult(fmt.Sprintf("step %d: tool name is required", i))
		}

		// Prevent recursive workflow calls
		if toolName == "workflow" {
			return ErrorResult(fmt.Sprintf("step %d: cannot call workflow from within a workflow", i))
		}

		stepArgs, _ := stepMap["args"].(map[string]interface{})
		if stepArgs == nil {
			stepArgs = map[string]interface{}{}
		}

		onFail, _ := stepMap["on_fail"].(string)
		if onFail == "" {
			onFail = "abort"
		}
		if onFail != "abort" && onFail != "retry" && onFail != "skip" {
			return ErrorResult(fmt.Sprintf("step %d: on_fail must be abort, retry, or skip", i))
		}

		label, _ := stepMap["label"].(string)
		if label == "" {
			label = fmt.Sprintf("Step %d: %s", i+1, toolName)
		}

		steps = append(steps, WorkflowStep{
			Tool:   toolName,
			Args:   stepArgs,
			OnFail: onFail,
			Label:  label,
		})
	}

	// Create workflow state
	wf := Workflow{
		Name:      name,
		Steps:     steps,
		Results:   make([]StepResult, 0, len(steps)),
		Status:    "running",
		StartedAt: time.Now().Format(time.RFC3339),
	}

	// Save initial state
	t.saveWorkflow(wf)

	logger.InfoCF("workflow", "Workflow started", map[string]interface{}{
		"name":  name,
		"steps": len(steps),
	})

	// Execute steps sequentially
	allSuccess := true
	for i, step := range steps {
		logger.InfoCF("workflow", "Step started", map[string]interface{}{
			"workflow": name,
			"step":     i,
			"tool":     step.Tool,
			"label":    step.Label,
		})

		result := t.executeStep(ctx, step, i)
		wf.Results = append(wf.Results, result)

		if result.Status == "failed" {
			allSuccess = false
			if step.OnFail == "abort" {
				wf.Status = "failed"
				wf.Error = fmt.Sprintf("step %d (%s) failed: %s", i, step.Label, result.Error)
				break
			}
			// skip: continue to next step
		}

		// Check if context was cancelled (agent shutting down)
		if ctx.Err() != nil {
			wf.Status = "aborted"
			wf.Error = "workflow cancelled: context done"
			allSuccess = false
			break
		}
	}

	if allSuccess && wf.Status == "running" {
		wf.Status = "completed"
	}

	wf.EndedAt = time.Now().Format(time.RFC3339)
	t.saveWorkflow(wf)

	logger.InfoCF("workflow", "Workflow finished", map[string]interface{}{
		"name":   name,
		"status": wf.Status,
		"steps":  len(wf.Results),
	})

	return SilentResult(t.formatWorkflowSummary(wf))
}

// executeStep runs a single workflow step with timeout, handling retry logic.
func (t *WorkflowTool) executeStep(ctx context.Context, step WorkflowStep, index int) StepResult {
	maxAttempts := 1
	if step.OnFail == "retry" {
		maxAttempts = 3
	}

	backoffs := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

	var lastErr string
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			logger.InfoCF("workflow", "Retrying step", map[string]interface{}{
				"step":    index,
				"tool":    step.Tool,
				"attempt": attempt + 1,
			})

			// Wait with backoff, but respect context cancellation
			select {
			case <-time.After(backoffs[attempt-1]):
			case <-ctx.Done():
				return StepResult{
					StepIndex: index,
					Tool:      step.Tool,
					Status:    "failed",
					Error:     "cancelled during retry backoff",
					Duration:  "0s",
				}
			}
		}

		// Create per-step timeout context (120s)
		stepCtx, stepCancel := context.WithTimeout(ctx, 120*time.Second)
		start := time.Now()

		result := t.registry.Execute(stepCtx, step.Tool, step.Args)
		duration := time.Since(start)
		stepCancel()

		if !result.IsError {
			return StepResult{
				StepIndex: index,
				Tool:      step.Tool,
				Status:    "success",
				Output:    wfTruncate(result.ForLLM, 500),
				Duration:  duration.Round(time.Millisecond).String(),
			}
		}

		lastErr = result.ForLLM
	}

	// All attempts exhausted
	finalStatus := "failed"
	if step.OnFail == "skip" {
		finalStatus = "skipped"
	}

	return StepResult{
		StepIndex: index,
		Tool:      step.Tool,
		Status:    finalStatus,
		Error:     lastErr,
		Duration:  "0s",
	}
}

func (t *WorkflowTool) status(args map[string]interface{}) *ToolResult {
	name, _ := args["name"].(string)
	if name == "" {
		return ErrorResult("name is required for status")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	workflows, err := t.loadWorkflows()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load workflows: %v", err))
	}

	// Search from newest to oldest
	for i := len(workflows) - 1; i >= 0; i-- {
		if workflows[i].Name == name {
			return SilentResult(t.formatWorkflowSummary(workflows[i]))
		}
	}

	return SilentResult(fmt.Sprintf("No workflow found with name '%s'", name))
}

func (t *WorkflowTool) list() *ToolResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	workflows, err := t.loadWorkflows()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load workflows: %v", err))
	}

	if len(workflows) == 0 {
		return SilentResult("No workflows found")
	}

	// Show last 10
	start := 0
	if len(workflows) > 10 {
		start = len(workflows) - 10
	}

	var lines []string
	for i := len(workflows) - 1; i >= start; i-- {
		wf := workflows[i]
		successCount := 0
		for _, r := range wf.Results {
			if r.Status == "success" {
				successCount++
			}
		}
		lines = append(lines, fmt.Sprintf("- %s [%s] %d/%d steps ok, started %s",
			wf.Name, strings.ToUpper(wf.Status), successCount, len(wf.Steps), wf.StartedAt))
	}

	return SilentResult(fmt.Sprintf("%d workflow(s) (showing last %d):\n%s",
		len(workflows), len(lines), strings.Join(lines, "\n")))
}

// --- persistence ---

func (t *WorkflowTool) loadWorkflows() ([]Workflow, error) {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var workflows []Workflow
	if err := json.Unmarshal(data, &workflows); err != nil {
		return nil, err
	}
	return workflows, nil
}

func (t *WorkflowTool) saveWorkflow(wf Workflow) {
	t.mu.Lock()
	defer t.mu.Unlock()

	workflows, _ := t.loadWorkflows()

	// Update existing or append
	found := false
	for i, existing := range workflows {
		if existing.Name == wf.Name && existing.StartedAt == wf.StartedAt {
			workflows[i] = wf
			found = true
			break
		}
	}
	if !found {
		workflows = append(workflows, wf)
	}

	// FIFO: keep only last maxWorkflows
	if len(workflows) > maxWorkflows {
		workflows = workflows[len(workflows)-maxWorkflows:]
	}

	data, err := json.MarshalIndent(workflows, "", "  ")
	if err != nil {
		logger.ErrorCF("workflow", "Failed to marshal workflows", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Atomic write: tmp file + rename
	tmpPath := t.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		logger.ErrorCF("workflow", "Failed to write workflows", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}
	os.Rename(tmpPath, t.filePath)
}

// --- formatting ---

func (t *WorkflowTool) formatWorkflowSummary(wf Workflow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Workflow: %s [%s]\n", wf.Name, strings.ToUpper(wf.Status))
	fmt.Fprintf(&b, "Started: %s\n", wf.StartedAt)
	if wf.EndedAt != "" {
		fmt.Fprintf(&b, "Ended: %s\n", wf.EndedAt)
	}
	if wf.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", wf.Error)
	}

	fmt.Fprintf(&b, "\nSteps (%d/%d completed):\n", countByStatus(wf.Results, "success"), len(wf.Steps))

	for i, step := range wf.Steps {
		status := "pending"
		var detail string
		if i < len(wf.Results) {
			r := wf.Results[i]
			status = r.Status
			detail = r.Duration
			if r.Error != "" {
				detail += " | " + wfTruncate(r.Error, 100)
			} else if r.Output != "" {
				detail += " | " + wfTruncate(r.Output, 100)
			}
		}

		icon := statusIcon(status)
		line := fmt.Sprintf("  %s %d. %s (%s)", icon, i+1, step.Label, status)
		if detail != "" {
			line += " — " + detail
		}
		b.WriteString(line + "\n")
	}

	return b.String()
}

func countByStatus(results []StepResult, status string) int {
	n := 0
	for _, r := range results {
		if r.Status == status {
			n++
		}
	}
	return n
}

func statusIcon(status string) string {
	switch status {
	case "success":
		return "[OK]"
	case "failed":
		return "[FAIL]"
	case "skipped":
		return "[SKIP]"
	case "retrying":
		return "[RETRY]"
	default:
		return "[..]"
	}
}

func wfTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
