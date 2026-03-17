package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type changelogEntry struct {
	Timestamp string `json:"timestamp"`
	Action    string `json:"action"`
	Section   string `json:"section,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// SelfTool allows the agent to read and modify its own AGENTS.md prompt.
type SelfTool struct {
	agentsPath    string
	changelogPath string
	mu            sync.Mutex
}

func NewSelfTool(workspace string) *SelfTool {
	return &SelfTool{
		agentsPath:    filepath.Join(workspace, "AGENTS.md"),
		changelogPath: filepath.Join(workspace, "state", "self_changelog.json"),
	}
}

func (t *SelfTool) Name() string { return "self" }

func (t *SelfTool) Description() string {
	return "Read, modify, or rollback your own operating instructions (AGENTS.md). Supports section-based editing with automatic backup and changelog."
}

func (t *SelfTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"read_prompt", "update_section", "append_section", "rollback_prompt", "changelog"},
				"description": "Action to perform",
			},
			"section": map[string]interface{}{
				"type":        "string",
				"description": "Section title to update (for update_section, matched by substring against ## N) Title)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "New content for the section (for update_section and append_section)",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Title for new section (for append_section)",
			},
			"reason": map[string]interface{}{
				"type":        "string",
				"description": "Why this change is being made (required for update_section and append_section)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *SelfTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "read_prompt":
		return t.readPrompt()
	case "update_section":
		return t.updateSection(args)
	case "append_section":
		return t.appendSection(args)
	case "rollback_prompt":
		return t.rollbackPrompt()
	case "changelog":
		return t.showChangelog()
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *SelfTool) readPrompt() *ToolResult {
	data, err := os.ReadFile(t.agentsPath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read AGENTS.md: %v", err))
	}
	return SilentResult(string(data))
}

// sectionRegex matches section headers like "## 3) Communication Rules"
var sectionRegex = regexp.MustCompile(`(?m)^## \d+\) .+$`)

func (t *SelfTool) updateSection(args map[string]interface{}) *ToolResult {
	section, _ := args["section"].(string)
	content, _ := args["content"].(string)
	reason, _ := args["reason"].(string)

	if section == "" || content == "" {
		return ErrorResult("section and content are required for update_section")
	}
	if reason == "" {
		return ErrorResult("reason is required for update_section")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := os.ReadFile(t.agentsPath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read AGENTS.md: %v", err))
	}
	text := string(data)

	// Find the target section header
	lowerSection := strings.ToLower(section)
	headers := sectionRegex.FindAllStringIndex(text, -1)
	targetIdx := -1
	var targetHeader string

	for i, loc := range headers {
		header := text[loc[0]:loc[1]]
		if strings.Contains(strings.ToLower(header), lowerSection) {
			targetIdx = i
			targetHeader = header
			break
		}
	}

	if targetIdx == -1 {
		return ErrorResult(fmt.Sprintf("section matching '%s' not found in AGENTS.md", section))
	}

	// Determine section boundaries
	sectionStart := headers[targetIdx][1] // end of header line
	var sectionEnd int
	if targetIdx+1 < len(headers) {
		// Look for the `---` separator before the next section
		nextStart := headers[targetIdx+1][0]
		sepIdx := strings.LastIndex(text[sectionStart:nextStart], "\n---\n")
		if sepIdx >= 0 {
			sectionEnd = sectionStart + sepIdx
		} else {
			sectionEnd = nextStart
		}
	} else {
		sectionEnd = len(text)
	}

	// Create backup
	if err := t.createBackup(data); err != nil {
		return ErrorResult(fmt.Sprintf("failed to create backup: %v", err))
	}

	// Build new content: header + new content
	newSection := "\n\n" + strings.TrimSpace(content) + "\n"
	newText := text[:sectionStart] + newSection + "\n---\n" + text[sectionEnd:]

	// Remove potential duplicate separators
	newText = strings.ReplaceAll(newText, "\n---\n\n---\n", "\n---\n")

	// Sanity check
	if err := t.sanityCheck(newText); err != nil {
		return ErrorResult(fmt.Sprintf("sanity check failed, change aborted: %v", err))
	}

	// Atomic write
	if err := t.atomicWrite(newText); err != nil {
		return ErrorResult(fmt.Sprintf("failed to write AGENTS.md: %v", err))
	}

	// Log to changelog
	t.appendChangelog(changelogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Action:    "update_section",
		Section:   targetHeader,
		Reason:    reason,
		Summary:   fmt.Sprintf("Updated section '%s'", targetHeader),
	})

	return SilentResult(fmt.Sprintf("Section '%s' updated successfully. Backup created.", targetHeader))
}

func (t *SelfTool) appendSection(args map[string]interface{}) *ToolResult {
	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	reason, _ := args["reason"].(string)

	if title == "" || content == "" {
		return ErrorResult("title and content are required for append_section")
	}
	if reason == "" {
		return ErrorResult("reason is required for append_section")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := os.ReadFile(t.agentsPath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read AGENTS.md: %v", err))
	}
	text := string(data)

	// Find the highest section number
	headers := sectionRegex.FindAllString(text, -1)
	maxNum := 0
	for _, h := range headers {
		var n int
		fmt.Sscanf(h, "## %d)", &n)
		if n > maxNum {
			maxNum = n
		}
	}
	newNum := maxNum + 1

	// Create backup
	if err := t.createBackup(data); err != nil {
		return ErrorResult(fmt.Sprintf("failed to create backup: %v", err))
	}

	// Append new section
	newSection := fmt.Sprintf("\n## %d) %s\n\n%s\n", newNum, title, strings.TrimSpace(content))
	newText := strings.TrimRight(text, "\n\t ") + "\n\n---\n" + newSection

	// Sanity check
	if err := t.sanityCheck(newText); err != nil {
		return ErrorResult(fmt.Sprintf("sanity check failed, change aborted: %v", err))
	}

	// Atomic write
	if err := t.atomicWrite(newText); err != nil {
		return ErrorResult(fmt.Sprintf("failed to write AGENTS.md: %v", err))
	}

	sectionName := fmt.Sprintf("## %d) %s", newNum, title)
	t.appendChangelog(changelogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Action:    "append_section",
		Section:   sectionName,
		Reason:    reason,
		Summary:   fmt.Sprintf("Added new section '%s'", sectionName),
	})

	return SilentResult(fmt.Sprintf("Section '%s' appended successfully. Backup created.", sectionName))
}

func (t *SelfTool) rollbackPrompt() *ToolResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	bakPath := t.agentsPath + ".bak"
	data, err := os.ReadFile(bakPath)
	if err != nil {
		return ErrorResult("no backup found to rollback from")
	}

	// Sanity check the backup
	if err := t.sanityCheck(string(data)); err != nil {
		return ErrorResult(fmt.Sprintf("backup file failed sanity check: %v", err))
	}

	// Write the backup as current
	if err := t.atomicWrite(string(data)); err != nil {
		return ErrorResult(fmt.Sprintf("failed to restore from backup: %v", err))
	}

	t.appendChangelog(changelogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Action:    "rollback_prompt",
		Reason:    "Restored from .bak",
		Summary:   "Rolled back AGENTS.md to previous backup",
	})

	return SilentResult("AGENTS.md restored from backup successfully.")
}

func (t *SelfTool) showChangelog() *ToolResult {
	data, err := os.ReadFile(t.changelogPath)
	if err != nil {
		return SilentResult("No changelog entries yet.")
	}

	var entries []changelogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return ErrorResult(fmt.Sprintf("failed to parse changelog: %v", err))
	}

	if len(entries) == 0 {
		return SilentResult("No changelog entries yet.")
	}

	// Show last 10
	start := 0
	if len(entries) > 10 {
		start = len(entries) - 10
	}

	var lines []string
	for _, e := range entries[start:] {
		line := fmt.Sprintf("[%s] %s", e.Timestamp, e.Action)
		if e.Section != "" {
			line += fmt.Sprintf(" — %s", e.Section)
		}
		if e.Reason != "" {
			line += fmt.Sprintf(" | Reason: %s", e.Reason)
		}
		lines = append(lines, line)
	}

	return SilentResult(fmt.Sprintf("Last %d changelog entries:\n%s", len(lines), strings.Join(lines, "\n")))
}

// createBackup rotates backups: .bak.2 <- .bak.1 <- .bak <- current
func (t *SelfTool) createBackup(currentData []byte) error {
	bak := t.agentsPath + ".bak"
	bak1 := bak + ".1"
	bak2 := bak + ".2"

	// Rotate: .bak.1 -> .bak.2
	if _, err := os.Stat(bak1); err == nil {
		os.Rename(bak1, bak2)
	}
	// Rotate: .bak -> .bak.1
	if _, err := os.Stat(bak); err == nil {
		os.Rename(bak, bak1)
	}
	// Write current as .bak
	return os.WriteFile(bak, currentData, 0644)
}

// sanityCheck verifies the resulting file has the minimum expected structure.
func (t *SelfTool) sanityCheck(content string) error {
	// Must have at least 5 section headers
	matches := sectionRegex.FindAllString(content, -1)
	if len(matches) < 5 {
		return fmt.Errorf("expected at least 5 sections, found %d", len(matches))
	}
	// Must have --- separators
	if !strings.Contains(content, "---") {
		return fmt.Errorf("missing --- separators")
	}
	return nil
}

// atomicWrite writes content to AGENTS.md via tmp+rename.
func (t *SelfTool) atomicWrite(content string) error {
	tmpPath := t.agentsPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, t.agentsPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// appendChangelog adds an entry to the changelog file.
func (t *SelfTool) appendChangelog(entry changelogEntry) {
	var entries []changelogEntry

	data, err := os.ReadFile(t.changelogPath)
	if err == nil {
		json.Unmarshal(data, &entries)
	}

	entries = append(entries, entry)

	// Keep max 50 entries
	if len(entries) > 50 {
		entries = entries[len(entries)-50:]
	}

	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}

	// Ensure state directory exists
	os.MkdirAll(filepath.Dir(t.changelogPath), 0755)
	os.WriteFile(t.changelogPath, out, 0644)
}
