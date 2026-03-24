// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MemoryStore manages persistent memory for the agent.
// - Long-term memory: obsidian/preferences/ (all notes)
// - Daily notes: obsidian/daily/YYYY-MM-DD.md
type MemoryStore struct {
	workspace string
	vaultDir  string
}

// NewMemoryStore creates a new MemoryStore with the given workspace path.
func NewMemoryStore(workspace string) *MemoryStore {
	vaultDir := filepath.Join(workspace, "obsidian")
	os.MkdirAll(filepath.Join(vaultDir, "daily"), 0755)
	os.MkdirAll(filepath.Join(vaultDir, "preferences"), 0755)

	return &MemoryStore{
		workspace: workspace,
		vaultDir:  vaultDir,
	}
}

// getTodayFile returns the path to today's daily note (obsidian/daily/YYYY-MM-DD.md).
func (ms *MemoryStore) getTodayFile() string {
	return filepath.Join(ms.vaultDir, "daily", time.Now().Format("2006-01-02")+".md")
}

// ReadLongTerm reads all preference notes and returns them concatenated.
func (ms *MemoryStore) ReadLongTerm() string {
	prefsDir := filepath.Join(ms.vaultDir, "preferences")
	entries, err := os.ReadDir(prefsDir)
	if err != nil {
		// Fallback: try legacy MEMORY.md
		legacyPath := filepath.Join(ms.workspace, "memory", "MEMORY.md")
		if data, err := os.ReadFile(legacyPath); err == nil {
			return string(data)
		}
		return ""
	}

	var parts []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(prefsDir, e.Name()))
		if err != nil {
			continue
		}
		// Extract body (skip frontmatter)
		content := string(data)
		body := extractBody(content)
		if body != "" {
			key := strings.TrimSuffix(e.Name(), ".md")
			parts = append(parts, fmt.Sprintf("- **%s**: %s", key, body))
		}
	}
	return strings.Join(parts, "\n")
}

// WriteLongTerm is kept for compatibility but now writes to preferences/memory-core.md
func (ms *MemoryStore) WriteLongTerm(content string) error {
	path := filepath.Join(ms.vaultDir, "preferences", "memory-core.md")
	now := time.Now().Format(time.RFC3339)
	md := fmt.Sprintf("---\nkey: memory-core\ntags: [core]\nfolder: preferences\ncreated: %s\nupdated: %s\nlinks: []\n---\n\n%s\n", now, now, content)
	return os.WriteFile(path, []byte(md), 0644)
}

// ReadToday reads today's daily note.
func (ms *MemoryStore) ReadToday() string {
	if data, err := os.ReadFile(ms.getTodayFile()); err == nil {
		return string(data)
	}
	return ""
}

// AppendToday appends content to today's daily note.
func (ms *MemoryStore) AppendToday(content string) error {
	todayFile := ms.getTodayFile()
	now := time.Now()

	existing, _ := os.ReadFile(todayFile)

	var newContent string
	if len(existing) == 0 {
		dateStr := now.Format("2006-01-02")
		newContent = fmt.Sprintf("---\nkey: %s\ntags: [daily]\nfolder: daily\ncreated: %s\nupdated: %s\nlinks: []\n---\n\n# %s\n\n## %s\n%s\n",
			dateStr, now.Format(time.RFC3339), now.Format(time.RFC3339), dateStr, now.Format("15:04"), content)
	} else {
		newContent = string(existing) + fmt.Sprintf("\n## %s\n%s\n", now.Format("15:04"), content)
	}

	return os.WriteFile(todayFile, []byte(newContent), 0644)
}

// GetRecentDailyNotes returns daily notes from the last N days.
func (ms *MemoryStore) GetRecentDailyNotes(days int) string {
	var notes []string

	for i := 0; i < days; i++ {
		date := time.Now().AddDate(0, 0, -i)
		filePath := filepath.Join(ms.vaultDir, "daily", date.Format("2006-01-02")+".md")

		if data, err := os.ReadFile(filePath); err == nil {
			body := extractBody(string(data))
			if body != "" {
				notes = append(notes, body)
			}
		}
	}

	if len(notes) == 0 {
		return ""
	}

	var result string
	for i, note := range notes {
		if i > 0 {
			result += "\n\n---\n\n"
		}
		result += note
	}
	return result
}

// maxMemoryChars is the maximum character length for the memory context.
// With 128k+ context models, 6000 was too restrictive. 12000 allows richer memory injection
// while still leaving plenty of room for history, tools, and system prompt.
const maxMemoryChars = 12000

// GetMemoryContext returns formatted memory context for the agent prompt.
func (ms *MemoryStore) GetMemoryContext() string {
	var parts []string

	// Long-term preferences
	longTerm := ms.ReadLongTerm()
	if longTerm != "" {
		parts = append(parts, "## Preferences & Knowledge\n\n"+longTerm)
	}

	// Key notes from other folders (insights, decisions)
	for _, folder := range []string{"insights", "decisions"} {
		folderNotes := ms.readFolderSummary(folder, 10)
		if folderNotes != "" {
			title := strings.ToUpper(folder[:1]) + folder[1:]
			parts = append(parts, fmt.Sprintf("## %s\n\n%s", title, folderNotes))
		}
	}

	// Recent daily notes (last 3 days)
	recentNotes := ms.GetRecentDailyNotes(3)
	if recentNotes != "" {
		parts = append(parts, "## Recent Daily Notes\n\n"+recentNotes)
	}

	if len(parts) == 0 {
		return ""
	}

	var result string
	for i, part := range parts {
		if i > 0 {
			result += "\n\n---\n\n"
		}
		result += part
	}
	result = fmt.Sprintf("# Memory (Obsidian Vault)\n\n%s", result)

	if len(result) > maxMemoryChars {
		result = result[:maxMemoryChars] + "\n\n[...memory truncated for context efficiency]"
	}

	return result
}

// readFolderSummary reads up to N notes from a folder and returns a summary.
func (ms *MemoryStore) readFolderSummary(folder string, max int) string {
	dir := filepath.Join(ms.vaultDir, folder)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	// Sort by mod time descending (most recent first)
	sort.Slice(entries, func(i, j int) bool {
		fi, _ := entries[i].Info()
		fj, _ := entries[j].Info()
		if fi == nil || fj == nil {
			return false
		}
		return fi.ModTime().After(fj.ModTime())
	})

	var lines []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if len(lines) >= max {
			break
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		body := extractBody(string(data))
		if body != "" {
			key := strings.TrimSuffix(e.Name(), ".md")
			// Truncate long bodies
			if len(body) > 150 {
				body = body[:150] + "..."
			}
			lines = append(lines, fmt.Sprintf("- **%s**: %s", key, strings.ReplaceAll(body, "\n", " ")))
		}
	}
	return strings.Join(lines, "\n")
}

// extractBody returns the content after frontmatter (skips ---...--- block).
func extractBody(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return strings.TrimSpace(content)
	}
	rest := content[4:]
	endIdx := strings.Index(rest, "\n---\n")
	if endIdx == -1 {
		return strings.TrimSpace(content)
	}
	return strings.TrimSpace(rest[endIdx+5:])
}
