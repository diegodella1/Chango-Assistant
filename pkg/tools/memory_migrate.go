package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// migrateNotesToVault migrates notes.json to the obsidian vault format.
// It's called once when NewMemoryTool detects notes.json exists and .migrated doesn't.
func migrateNotesToVault(workspace, vaultDir string) error {
	notesFile := filepath.Join(workspace, "memory", "notes.json")

	data, err := os.ReadFile(notesFile)
	if err != nil {
		return fmt.Errorf("reading notes.json: %w", err)
	}

	var notes []memoryNote
	if err := json.Unmarshal(data, &notes); err != nil {
		return fmt.Errorf("parsing notes.json: %w", err)
	}

	migrated := 0
	for _, n := range notes {
		slug := vaultSlugify(n.Key)
		folder := inferFolder(n.Key, n.Tags)

		created := n.CreatedAt
		if created == "" {
			created = time.Now().Format(time.RFC3339)
		}
		updated := n.UpdatedAt
		if updated == "" {
			updated = created
		}

		links := extractWikilinks(n.Content)

		note := &VaultNote{
			Key:     slug,
			Tags:    n.Tags,
			Folder:  folder,
			Created: created,
			Updated: updated,
			Links:   links,
			Content: n.Content,
		}

		if err := writeVaultNote(vaultDir, note); err != nil {
			return fmt.Errorf("writing note %s: %w", slug, err)
		}
		migrated++
	}

	// Migrate daily notes from memory/YYYYMM/YYYYMMDD.md → obsidian/daily/YYYY-MM-DD.md
	memoryDir := filepath.Join(workspace, "memory")
	dailyDir := filepath.Join(vaultDir, "daily")
	os.MkdirAll(dailyDir, 0755)

	filepath.Walk(memoryDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := info.Name()
		// Match YYYYMMDD.md pattern
		if len(base) == 11 && strings.HasSuffix(base, ".md") {
			dateStr := strings.TrimSuffix(base, ".md")
			if t, err := time.Parse("20060102", dateStr); err == nil {
				newName := t.Format("2006-01-02") + ".md"
				dstPath := filepath.Join(dailyDir, newName)
				if _, err := os.Stat(dstPath); os.IsNotExist(err) {
					content, _ := os.ReadFile(path)
					os.WriteFile(dstPath, content, 0644)
				}
			}
		}
		return nil
	})

	// Write migration marker
	marker := filepath.Join(vaultDir, ".migrated")
	markerContent := fmt.Sprintf("Migrated %d notes from notes.json at %s\n", migrated, time.Now().Format(time.RFC3339))
	os.WriteFile(marker, []byte(markerContent), 0644)

	return nil
}
