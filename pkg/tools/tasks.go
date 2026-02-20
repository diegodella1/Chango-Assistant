package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Task struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status"` // pending, in_progress, done, cancelled
	Priority    string   `json:"priority,omitempty"` // high, medium, low
	DueDate     string   `json:"due_date,omitempty"` // YYYY-MM-DD
	Tags        []string `json:"tags,omitempty"`
	Notes       string   `json:"notes,omitempty"`
	GoalID      string   `json:"goal_id,omitempty"` // link to parent task
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type TasksTool struct {
	filePath string
	mu       sync.Mutex
}

func NewTasksTool(workspace string) *TasksTool {
	dir := filepath.Join(workspace, "tasks")
	os.MkdirAll(dir, 0755)
	return &TasksTool{
		filePath: filepath.Join(dir, "tasks.json"),
	}
}

func (t *TasksTool) Name() string { return "tasks" }

func (t *TasksTool) Description() string {
	return "Task and goal tracking. Add, list, update, complete, cancel, delete, or search tasks. Use this to track goals, projects, and to-dos across sessions."
}

func (t *TasksTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"add", "list", "get", "update", "complete", "cancel", "delete", "search"},
				"description": "Action to perform",
			},
			"id": map[string]interface{}{
				"type":        "string",
				"description": "Task ID (required for get, update, complete, cancel, delete)",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Task title (required for add)",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Task description",
			},
			"status": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"pending", "in_progress", "done", "cancelled"},
				"description": "Task status (for update)",
			},
			"priority": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"high", "medium", "low"},
				"description": "Task priority",
			},
			"due_date": map[string]interface{}{
				"type":        "string",
				"description": "Due date in YYYY-MM-DD format",
			},
			"tags": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Tags for the task",
			},
			"notes": map[string]interface{}{
				"type":        "string",
				"description": "Additional notes",
			},
			"goal_id": map[string]interface{}{
				"type":        "string",
				"description": "Parent task/goal ID to link this task to",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query (for search action)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *TasksTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "add":
		return t.add(args)
	case "list":
		return t.list()
	case "get":
		return t.get(args)
	case "update":
		return t.update(args)
	case "complete":
		return t.complete(args)
	case "cancel":
		return t.cancelTask(args)
	case "delete":
		return t.del(args)
	case "search":
		return t.search(args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func generateTaskID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (t *TasksTool) loadTasks() ([]Task, error) {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (t *TasksTool) saveTasks(tasks []Task) error {
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.filePath, data, 0644)
}

func (t *TasksTool) add(args map[string]interface{}) *ToolResult {
	title, _ := args["title"].(string)
	if title == "" {
		return ErrorResult("title is required for add")
	}

	description, _ := args["description"].(string)
	priority, _ := args["priority"].(string)
	if priority == "" {
		priority = "medium"
	}
	dueDate, _ := args["due_date"].(string)
	notes, _ := args["notes"].(string)
	goalID, _ := args["goal_id"].(string)

	var tags []string
	if rawTags, ok := args["tags"].([]interface{}); ok {
		for _, rt := range rawTags {
			if s, ok := rt.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	var tasks []Task
	data, err := os.ReadFile(t.filePath)
	if err == nil {
		if err := json.Unmarshal(data, &tasks); err != nil {
			return ErrorResult(fmt.Sprintf("corrupted tasks file: %v", err))
		}
	}

	now := time.Now().Format(time.RFC3339)
	task := Task{
		ID:          generateTaskID(),
		Title:       title,
		Description: description,
		Status:      "pending",
		Priority:    priority,
		DueDate:     dueDate,
		Tags:        tags,
		Notes:       notes,
		GoalID:      goalID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	tasks = append(tasks, task)

	if err := t.saveTasks(tasks); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}

	result := fmt.Sprintf("Task created: %s (ID: %s, priority: %s", task.Title, task.ID, task.Priority)
	if dueDate != "" {
		result += fmt.Sprintf(", due: %s", dueDate)
	}
	result += ")"
	return SilentResult(result)
}

func (t *TasksTool) list() *ToolResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	tasks, err := t.loadTasks()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load tasks: %v", err))
	}

	if len(tasks) == 0 {
		return SilentResult("No tasks found")
	}

	today := time.Now().Format("2006-01-02")
	var lines []string
	overdueCount := 0

	for _, task := range tasks {
		if task.Status == "done" || task.Status == "cancelled" {
			continue
		}

		overdue := ""
		if task.DueDate != "" && task.DueDate < today && task.Status != "done" && task.Status != "cancelled" {
			overdue = " [OVERDUE]"
			overdueCount++
		}

		line := fmt.Sprintf("- [%s] %s (ID: %s, status: %s, priority: %s",
			strings.ToUpper(task.Status), task.Title, task.ID, task.Status, task.Priority)
		if task.DueDate != "" {
			line += fmt.Sprintf(", due: %s", task.DueDate)
		}
		if task.GoalID != "" {
			line += fmt.Sprintf(", goal: %s", task.GoalID)
		}
		line += ")" + overdue
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		return SilentResult("No active tasks")
	}

	header := fmt.Sprintf("%d active task(s)", len(lines))
	if overdueCount > 0 {
		header += fmt.Sprintf(" (%d overdue)", overdueCount)
	}
	return SilentResult(fmt.Sprintf("%s:\n%s", header, strings.Join(lines, "\n")))
}

func (t *TasksTool) get(args map[string]interface{}) *ToolResult {
	id, _ := args["id"].(string)
	if id == "" {
		return ErrorResult("id is required for get")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tasks, err := t.loadTasks()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load tasks: %v", err))
	}

	for _, task := range tasks {
		if task.ID == id {
			return SilentResult(formatTask(task))
		}
	}
	return SilentResult(fmt.Sprintf("No task found with ID '%s'", id))
}

func formatTask(task Task) string {
	lines := []string{
		fmt.Sprintf("ID: %s", task.ID),
		fmt.Sprintf("Title: %s", task.Title),
		fmt.Sprintf("Status: %s", task.Status),
		fmt.Sprintf("Priority: %s", task.Priority),
	}
	if task.Description != "" {
		lines = append(lines, fmt.Sprintf("Description: %s", task.Description))
	}
	if task.DueDate != "" {
		lines = append(lines, fmt.Sprintf("Due Date: %s", task.DueDate))

		today := time.Now().Format("2006-01-02")
		if task.DueDate < today && task.Status != "done" && task.Status != "cancelled" {
			lines = append(lines, "** OVERDUE **")
		}
	}
	if len(task.Tags) > 0 {
		lines = append(lines, fmt.Sprintf("Tags: %s", strings.Join(task.Tags, ", ")))
	}
	if task.Notes != "" {
		lines = append(lines, fmt.Sprintf("Notes: %s", task.Notes))
	}
	if task.GoalID != "" {
		lines = append(lines, fmt.Sprintf("Goal ID: %s", task.GoalID))
	}
	lines = append(lines, fmt.Sprintf("Created: %s", task.CreatedAt))
	lines = append(lines, fmt.Sprintf("Updated: %s", task.UpdatedAt))
	return strings.Join(lines, "\n")
}

func (t *TasksTool) update(args map[string]interface{}) *ToolResult {
	id, _ := args["id"].(string)
	if id == "" {
		return ErrorResult("id is required for update")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	var tasks []Task
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load tasks: %v", err))
	}
	if err := json.Unmarshal(data, &tasks); err != nil {
		return ErrorResult(fmt.Sprintf("corrupted tasks file: %v", err))
	}

	found := false
	for i, task := range tasks {
		if task.ID == id {
			if title, ok := args["title"].(string); ok && title != "" {
				tasks[i].Title = title
			}
			if desc, ok := args["description"].(string); ok {
				tasks[i].Description = desc
			}
			if status, ok := args["status"].(string); ok && status != "" {
				tasks[i].Status = status
			}
			if priority, ok := args["priority"].(string); ok && priority != "" {
				tasks[i].Priority = priority
			}
			if dueDate, ok := args["due_date"].(string); ok {
				tasks[i].DueDate = dueDate
			}
			if notes, ok := args["notes"].(string); ok {
				tasks[i].Notes = notes
			}
			if goalID, ok := args["goal_id"].(string); ok {
				tasks[i].GoalID = goalID
			}
			if rawTags, ok := args["tags"].([]interface{}); ok {
				var tags []string
				for _, rt := range rawTags {
					if s, ok := rt.(string); ok {
						tags = append(tags, s)
					}
				}
				tasks[i].Tags = tags
			}
			tasks[i].UpdatedAt = time.Now().Format(time.RFC3339)
			found = true
			break
		}
	}

	if !found {
		return SilentResult(fmt.Sprintf("No task found with ID '%s'", id))
	}

	if err := t.saveTasks(tasks); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}
	return SilentResult(fmt.Sprintf("Task '%s' updated", id))
}

func (t *TasksTool) complete(args map[string]interface{}) *ToolResult {
	id, _ := args["id"].(string)
	if id == "" {
		return ErrorResult("id is required for complete")
	}
	args["status"] = "done"
	return t.update(args)
}

func (t *TasksTool) cancelTask(args map[string]interface{}) *ToolResult {
	id, _ := args["id"].(string)
	if id == "" {
		return ErrorResult("id is required for cancel")
	}
	args["status"] = "cancelled"
	return t.update(args)
}

func (t *TasksTool) del(args map[string]interface{}) *ToolResult {
	id, _ := args["id"].(string)
	if id == "" {
		return ErrorResult("id is required for delete")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	var tasks []Task
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load tasks: %v", err))
	}
	if err := json.Unmarshal(data, &tasks); err != nil {
		return ErrorResult(fmt.Sprintf("corrupted tasks file: %v", err))
	}

	found := false
	var filtered []Task
	for _, task := range tasks {
		if task.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, task)
	}

	if !found {
		return SilentResult(fmt.Sprintf("No task found with ID '%s'", id))
	}

	if err := t.saveTasks(filtered); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}
	return SilentResult(fmt.Sprintf("Task '%s' deleted", id))
}

func (t *TasksTool) search(args map[string]interface{}) *ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ErrorResult("query is required for search")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tasks, err := t.loadTasks()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load tasks: %v", err))
	}

	q := strings.ToLower(query)
	var matches []string
	for _, task := range tasks {
		haystack := strings.ToLower(task.Title + " " + task.Description + " " + strings.Join(task.Tags, " "))
		if strings.Contains(haystack, q) {
			matches = append(matches, fmt.Sprintf("- [%s] %s (ID: %s, status: %s)", strings.ToUpper(task.Priority), task.Title, task.ID, task.Status))
		}
	}

	if len(matches) == 0 {
		return SilentResult(fmt.Sprintf("No tasks matching '%s'", query))
	}
	return SilentResult(fmt.Sprintf("Found %d task(s):\n%s", len(matches), strings.Join(matches, "\n")))
}
