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

type snippet struct {
	Content   string   `json:"content"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

type SnippetTool struct {
	filePath string
	mu       sync.Mutex
}

func NewSnippetTool(workspace string) *SnippetTool {
	return &SnippetTool{
		filePath: filepath.Join(workspace, "snippets.json"),
	}
}

func (t *SnippetTool) Name() string { return "snippet" }

func (t *SnippetTool) Description() string {
	return "Save, retrieve, list, delete, or search reusable code snippets and text fragments by name."
}

func (t *SnippetTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"save", "get", "list", "delete", "search"},
				"description": "Action to perform",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Snippet name (required for save, get, delete)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Snippet content (required for save)",
			},
			"tags": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional tags",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query (for search action, searches in name+content+tags)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *SnippetTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "save":
		return t.save(args)
	case "get":
		return t.get(args)
	case "list":
		return t.list()
	case "delete":
		return t.del(args)
	case "search":
		return t.search(args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *SnippetTool) loadSnippets() map[string]snippet {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return make(map[string]snippet)
	}
	var snippets map[string]snippet
	if err := json.Unmarshal(data, &snippets); err != nil {
		return make(map[string]snippet)
	}
	return snippets
}

func (t *SnippetTool) saveSnippets(snippets map[string]snippet) error {
	data, err := json.MarshalIndent(snippets, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.filePath, data, 0644)
}

func (t *SnippetTool) save(args map[string]interface{}) *ToolResult {
	name, _ := args["name"].(string)
	content, _ := args["content"].(string)
	if name == "" || content == "" {
		return ErrorResult("name and content are required for save")
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

	snippets := t.loadSnippets()
	now := time.Now().Format(time.RFC3339)

	_, exists := snippets[name]
	snippets[name] = snippet{
		Content:   content,
		Tags:      tags,
		CreatedAt: func() string {
			if exists {
				return snippets[name].CreatedAt
			}
			return now
		}(),
		UpdatedAt: now,
	}

	if err := t.saveSnippets(snippets); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}

	if exists {
		return SilentResult(fmt.Sprintf("Snippet '%s' updated", name))
	}
	return SilentResult(fmt.Sprintf("Snippet '%s' saved", name))
}

func (t *SnippetTool) get(args map[string]interface{}) *ToolResult {
	name, _ := args["name"].(string)
	if name == "" {
		return ErrorResult("name is required for get")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	snippets := t.loadSnippets()
	s, ok := snippets[name]
	if !ok {
		return SilentResult(fmt.Sprintf("No snippet found with name '%s'", name))
	}

	result := fmt.Sprintf("Snippet '%s':\n%s", name, s.Content)
	if len(s.Tags) > 0 {
		result += fmt.Sprintf("\nTags: %s", strings.Join(s.Tags, ", "))
	}
	return SilentResult(result)
}

func (t *SnippetTool) list() *ToolResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	snippets := t.loadSnippets()
	if len(snippets) == 0 {
		return SilentResult("No snippets saved")
	}

	var lines []string
	for name, s := range snippets {
		line := fmt.Sprintf("- %s", name)
		if len(s.Tags) > 0 {
			line += fmt.Sprintf(" [%s]", strings.Join(s.Tags, ", "))
		}
		// Show content preview
		preview := s.Content
		if len(preview) > 60 {
			preview = preview[:60] + "..."
		}
		line += fmt.Sprintf(": %s", preview)
		lines = append(lines, line)
	}

	return SilentResult(fmt.Sprintf("%d snippet(s):\n%s", len(snippets), strings.Join(lines, "\n")))
}

func (t *SnippetTool) del(args map[string]interface{}) *ToolResult {
	name, _ := args["name"].(string)
	if name == "" {
		return ErrorResult("name is required for delete")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	snippets := t.loadSnippets()
	if _, ok := snippets[name]; !ok {
		return SilentResult(fmt.Sprintf("No snippet found with name '%s'", name))
	}

	delete(snippets, name)
	t.saveSnippets(snippets)
	return SilentResult(fmt.Sprintf("Snippet '%s' deleted", name))
}

func (t *SnippetTool) search(args map[string]interface{}) *ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ErrorResult("query is required for search")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	snippets := t.loadSnippets()
	q := strings.ToLower(query)

	var matches []string
	for name, s := range snippets {
		haystack := strings.ToLower(name + " " + s.Content + " " + strings.Join(s.Tags, " "))
		if strings.Contains(haystack, q) {
			preview := s.Content
			if len(preview) > 80 {
				preview = preview[:80] + "..."
			}
			matches = append(matches, fmt.Sprintf("- %s: %s", name, preview))
		}
	}

	if len(matches) == 0 {
		return SilentResult(fmt.Sprintf("No snippets matching '%s'", query))
	}
	return SilentResult(fmt.Sprintf("Found %d snippet(s):\n%s", len(matches), strings.Join(matches, "\n")))
}
