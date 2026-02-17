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
)

type memoryNote struct {
	Key       string   `json:"key"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

type MemoryTool struct {
	filePath string
	mu       sync.Mutex
}

func NewMemoryTool(workspace string) *MemoryTool {
	dir := filepath.Join(workspace, "memory")
	os.MkdirAll(dir, 0755)
	return &MemoryTool{
		filePath: filepath.Join(dir, "notes.json"),
	}
}

func (t *MemoryTool) Name() string { return "memory" }

func (t *MemoryTool) Description() string {
	return "Persistent notes storage. Save, recall, search, list, or delete notes by key. Use this to remember things for the user across conversations."
}

func (t *MemoryTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"save", "recall", "search", "list", "delete"},
				"description": "Action to perform",
			},
			"key": map[string]interface{}{
				"type":        "string",
				"description": "Note key (required for save, recall, delete)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Note content (required for save)",
			},
			"tags": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional tags for the note",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query (for search action, searches in key+content+tags)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *MemoryTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "save":
		return t.save(args)
	case "recall":
		return t.recall(args)
	case "search":
		return t.search(args)
	case "list":
		return t.list()
	case "delete":
		return t.del(args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *MemoryTool) loadNotes() ([]memoryNote, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var notes []memoryNote
	if err := json.Unmarshal(data, &notes); err != nil {
		return nil, err
	}
	return notes, nil
}

func (t *MemoryTool) saveNotes(notes []memoryNote) error {
	// Caller must hold t.mu if needed, but we already hold it in loadNotes
	// For save operations, we handle locking at the action level
	data, err := json.MarshalIndent(notes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.filePath, data, 0644)
}

func (t *MemoryTool) save(args map[string]interface{}) *ToolResult {
	key, _ := args["key"].(string)
	content, _ := args["content"].(string)
	if key == "" || content == "" {
		return ErrorResult("key and content are required for save")
	}

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

	var notes []memoryNote
	data, err := os.ReadFile(t.filePath)
	if err == nil {
		json.Unmarshal(data, &notes)
	}

	now := time.Now().Format(time.RFC3339)
	found := false
	for i, n := range notes {
		if n.Key == key {
			notes[i].Content = content
			notes[i].Tags = tags
			notes[i].UpdatedAt = now
			found = true
			break
		}
	}
	if !found {
		notes = append(notes, memoryNote{
			Key:       key,
			Content:   content,
			Tags:      tags,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	d, _ := json.MarshalIndent(notes, "", "  ")
	if err := os.WriteFile(t.filePath, d, 0644); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}

	if found {
		return SilentResult(fmt.Sprintf("Note '%s' updated", key))
	}
	return SilentResult(fmt.Sprintf("Note '%s' saved", key))
}

func (t *MemoryTool) recall(args map[string]interface{}) *ToolResult {
	key, _ := args["key"].(string)
	if key == "" {
		return ErrorResult("key is required for recall")
	}

	notes, err := t.loadNotes()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load notes: %v", err))
	}

	for _, n := range notes {
		if n.Key == key {
			result := fmt.Sprintf("Key: %s\nContent: %s", n.Key, n.Content)
			if len(n.Tags) > 0 {
				result += fmt.Sprintf("\nTags: %s", strings.Join(n.Tags, ", "))
			}
			return SilentResult(result)
		}
	}
	return SilentResult(fmt.Sprintf("No note found with key '%s'", key))
}

func (t *MemoryTool) search(args map[string]interface{}) *ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ErrorResult("query is required for search")
	}

	notes, err := t.loadNotes()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load notes: %v", err))
	}

	q := strings.ToLower(query)
	var matches []string
	for _, n := range notes {
		haystack := strings.ToLower(n.Key + " " + n.Content + " " + strings.Join(n.Tags, " "))
		if strings.Contains(haystack, q) {
			matches = append(matches, fmt.Sprintf("- %s: %s", n.Key, n.Content))
		}
	}

	if len(matches) == 0 {
		return SilentResult(fmt.Sprintf("No notes matching '%s'", query))
	}
	return SilentResult(fmt.Sprintf("Found %d note(s):\n%s", len(matches), strings.Join(matches, "\n")))
}

func (t *MemoryTool) list() *ToolResult {
	notes, err := t.loadNotes()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load notes: %v", err))
	}

	if len(notes) == 0 {
		return SilentResult("No notes saved")
	}

	var lines []string
	for _, n := range notes {
		line := fmt.Sprintf("- %s", n.Key)
		if len(n.Tags) > 0 {
			line += fmt.Sprintf(" [%s]", strings.Join(n.Tags, ", "))
		}
		lines = append(lines, line)
	}
	return SilentResult(fmt.Sprintf("%d note(s):\n%s", len(notes), strings.Join(lines, "\n")))
}

func (t *MemoryTool) del(args map[string]interface{}) *ToolResult {
	key, _ := args["key"].(string)
	if key == "" {
		return ErrorResult("key is required for delete")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	var notes []memoryNote
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load notes: %v", err))
	}
	json.Unmarshal(data, &notes)

	found := false
	var filtered []memoryNote
	for _, n := range notes {
		if n.Key == key {
			found = true
			continue
		}
		filtered = append(filtered, n)
	}

	if !found {
		return SilentResult(fmt.Sprintf("No note found with key '%s'", key))
	}

	d, _ := json.MarshalIndent(filtered, "", "  ")
	os.WriteFile(t.filePath, d, 0644)
	return SilentResult(fmt.Sprintf("Note '%s' deleted", key))
}
