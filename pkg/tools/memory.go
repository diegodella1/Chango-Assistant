package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// memoryNote is the legacy JSON note format, kept for migration.
type memoryNote struct {
	Key       string   `json:"key"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// VaultNote represents a markdown note in the obsidian vault.
type VaultNote struct {
	Key     string
	Tags    []string
	Folder  string
	Created string
	Updated string
	Links   []string
	Content string
}

// MemoryTool implements the memory tool backed by an obsidian-style vault.
type MemoryTool struct {
	vaultDir string
	mu       sync.RWMutex
	index    map[string]*VaultNote
	tfidf    *tfidfIndex
}

var vaultFolders = []string{
	"daily", "people", "preferences", "insights",
	"decisions", "projects", "blog", "state", "inbox",
}

func NewMemoryTool(workspace string) *MemoryTool {
	vaultDir := filepath.Join(workspace, "obsidian")

	// Create vault directories
	for _, f := range vaultFolders {
		os.MkdirAll(filepath.Join(vaultDir, f), 0755)
	}

	t := &MemoryTool{
		vaultDir: vaultDir,
		index:    make(map[string]*VaultNote),
		tfidf:    newTFIDFIndex(),
	}

	// Migrate from notes.json if needed
	migrated := filepath.Join(vaultDir, ".migrated")
	notesJSON := filepath.Join(workspace, "memory", "notes.json")
	if _, err := os.Stat(migrated); os.IsNotExist(err) {
		if _, err := os.Stat(notesJSON); err == nil {
			if err := migrateNotesToVault(workspace, vaultDir); err != nil {
				fmt.Fprintf(os.Stderr, "memory migration error: %v\n", err)
			}
		}
	}

	// Build in-memory index
	t.buildIndex()

	// Build TF-IDF index from all notes
	t.rebuildTFIDF()

	return t
}

func (t *MemoryTool) Name() string { return "memory" }

func (t *MemoryTool) Description() string {
	return "Obsidian-style knowledge vault. Actions: save (create/update note), recall (read by key), search (full-text + folder/tag filter), list (filter by folder/tag), delete, daily (append to today's daily note), link (find backlinks). Notes are markdown with YAML frontmatter, organized in folders: daily, people, preferences, insights, decisions, projects, blog, state, inbox."
}

func (t *MemoryTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"save", "recall", "search", "list", "delete", "daily", "link"},
				"description": "Action to perform",
			},
			"key": map[string]interface{}{
				"type":        "string",
				"description": "Note key/slug (required for save, recall, delete, link)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Note content (required for save and daily)",
			},
			"tags": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Tags for the note (for save, or filter for search/list)",
			},
			"folder": map[string]interface{}{
				"type":        "string",
				"description": "Folder (people, preferences, insights, decisions, projects, blog, state, inbox, daily). Auto-inferred from key if omitted.",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query (for search action, full-text in key+content+tags)",
			},
			"tag": map[string]interface{}{
				"type":        "string",
				"description": "Filter by single tag (for search/list)",
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
		return t.list(args)
	case "delete":
		return t.del(args)
	case "daily":
		return t.daily(args)
	case "link":
		return t.link(args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

// SaveNote is the public API for programmatic note saving (used by auto-distillation).
func (t *MemoryTool) SaveNote(key, content string, tags []string, folder string) error {
	if key == "" || content == "" {
		return fmt.Errorf("key and content are required")
	}

	slug := vaultSlugify(key)
	if folder == "" {
		folder = inferFolder(slug, tags)
	}

	now := time.Now().Format(time.RFC3339)
	links := extractWikilinks(content)

	t.mu.Lock()
	defer t.mu.Unlock()

	existing := t.index[slug]
	created := now
	if existing != nil {
		created = existing.Created
	}

	note := &VaultNote{
		Key:     slug,
		Tags:    tags,
		Folder:  folder,
		Created: created,
		Updated: now,
		Links:   links,
		Content: content,
	}

	if err := writeVaultNote(t.vaultDir, note); err != nil {
		return err
	}

	if existing != nil && existing.Folder != folder {
		oldPath := filepath.Join(t.vaultDir, existing.Folder, slug+".md")
		os.Remove(oldPath)
	}

	t.index[slug] = note
	t.tfidf.addDocument(slug, slug+" "+content+" "+strings.Join(tags, " "))
	return nil
}

// SearchNotes searches the vault using TF-IDF and returns matching notes with content.
// Used by the memory context builder for relevance-based injection.
func (t *MemoryTool) SearchNotes(query string, maxResults int) []VaultNote {
	t.mu.RLock()
	defer t.mu.RUnlock()

	results := t.tfidf.search(query, maxResults)
	var notes []VaultNote
	for _, r := range results {
		if note, ok := t.index[r.Key]; ok {
			notes = append(notes, *note)
		}
	}
	return notes
}

// --- Actions ---

func (t *MemoryTool) save(args map[string]interface{}) *ToolResult {
	key, _ := args["key"].(string)
	content, _ := args["content"].(string)
	if key == "" || content == "" {
		return ErrorResult("key and content are required for save")
	}

	slug := vaultSlugify(key)
	tags := extractStringSlice(args, "tags")
	folder, _ := args["folder"].(string)
	if folder == "" {
		folder = inferFolder(slug, tags)
	}

	now := time.Now().Format(time.RFC3339)
	links := extractWikilinks(content)

	t.mu.Lock()
	defer t.mu.Unlock()

	existing := t.index[slug]
	created := now
	if existing != nil {
		created = existing.Created
	}

	note := &VaultNote{
		Key:     slug,
		Tags:    tags,
		Folder:  folder,
		Created: created,
		Updated: now,
		Links:   links,
		Content: content,
	}

	if err := writeVaultNote(t.vaultDir, note); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}

	// If folder changed, remove old file
	if existing != nil && existing.Folder != folder {
		oldPath := filepath.Join(t.vaultDir, existing.Folder, slug+".md")
		os.Remove(oldPath)
	}

	t.index[slug] = note
	t.tfidf.addDocument(slug, slug+" "+content+" "+strings.Join(tags, " "))

	verb := "saved"
	if existing != nil {
		verb = "updated"
	}
	return SilentResult(fmt.Sprintf("Note '%s' %s in %s/", slug, verb, folder))
}

func (t *MemoryTool) recall(args map[string]interface{}) *ToolResult {
	key, _ := args["key"].(string)
	if key == "" {
		return ErrorResult("key is required for recall")
	}

	slug := vaultSlugify(key)

	t.mu.RLock()
	note := t.index[slug]
	t.mu.RUnlock()

	if note == nil {
		return SilentResult(fmt.Sprintf("No note found with key '%s'", slug))
	}

	result := fmt.Sprintf("Key: %s\nFolder: %s\nContent: %s", note.Key, note.Folder, note.Content)
	if len(note.Tags) > 0 {
		result += fmt.Sprintf("\nTags: %s", strings.Join(note.Tags, ", "))
	}
	if len(note.Links) > 0 {
		result += fmt.Sprintf("\nLinks: %s", strings.Join(note.Links, ", "))
	}
	return SilentResult(result)
}

func (t *MemoryTool) search(args map[string]interface{}) *ToolResult {
	query, _ := args["query"].(string)
	folder, _ := args["folder"].(string)
	tag, _ := args["tag"].(string)

	if query == "" && folder == "" && tag == "" {
		return ErrorResult("query, folder, or tag is required for search")
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	// When there's a text query, use TF-IDF ranking
	if query != "" {
		results := t.tfidf.search(query, 20)
		var matches []string
		for _, r := range results {
			note := t.index[r.Key]
			if note == nil {
				continue
			}
			if folder != "" && note.Folder != folder {
				continue
			}
			if tag != "" && !containsTag(note.Tags, tag) {
				continue
			}
			matches = append(matches, fmt.Sprintf("- [%s] %s (%.0f%%): %s", note.Folder, note.Key, r.Score*100, truncate(note.Content, 100)))
		}
		if len(matches) == 0 {
			return SilentResult("No notes found")
		}
		return SilentResult(fmt.Sprintf("Found %d note(s) ranked by relevance:\n%s", len(matches), strings.Join(matches, "\n")))
	}

	// Fallback: folder/tag filter only
	var matches []string
	for _, note := range t.index {
		if folder != "" && note.Folder != folder {
			continue
		}
		if tag != "" && !containsTag(note.Tags, tag) {
			continue
		}
		matches = append(matches, fmt.Sprintf("- [%s] %s: %s", note.Folder, note.Key, truncate(note.Content, 100)))
	}

	sort.Strings(matches)

	if len(matches) == 0 {
		return SilentResult("No notes found")
	}
	return SilentResult(fmt.Sprintf("Found %d note(s):\n%s", len(matches), strings.Join(matches, "\n")))
}

func (t *MemoryTool) list(args map[string]interface{}) *ToolResult {
	folder, _ := args["folder"].(string)
	tag, _ := args["tag"].(string)

	t.mu.RLock()
	defer t.mu.RUnlock()

	var lines []string
	for _, note := range t.index {
		if folder != "" && note.Folder != folder {
			continue
		}
		if tag != "" && !containsTag(note.Tags, tag) {
			continue
		}
		line := fmt.Sprintf("- [%s] %s", note.Folder, note.Key)
		if len(note.Tags) > 0 {
			line += fmt.Sprintf(" [%s]", strings.Join(note.Tags, ", "))
		}
		lines = append(lines, line)
	}

	sort.Strings(lines)

	if len(lines) == 0 {
		return SilentResult("No notes found")
	}
	return SilentResult(fmt.Sprintf("%d note(s):\n%s", len(lines), strings.Join(lines, "\n")))
}

func (t *MemoryTool) del(args map[string]interface{}) *ToolResult {
	key, _ := args["key"].(string)
	if key == "" {
		return ErrorResult("key is required for delete")
	}

	slug := vaultSlugify(key)

	t.mu.Lock()
	defer t.mu.Unlock()

	note := t.index[slug]
	if note == nil {
		return SilentResult(fmt.Sprintf("No note found with key '%s'", slug))
	}

	notePath := filepath.Join(t.vaultDir, note.Folder, slug+".md")
	os.Remove(notePath)
	delete(t.index, slug)
	t.tfidf.removeDocument(slug)

	return SilentResult(fmt.Sprintf("Note '%s' deleted from %s/", slug, note.Folder))
}

func (t *MemoryTool) daily(args map[string]interface{}) *ToolResult {
	content, _ := args["content"].(string)
	if content == "" {
		return ErrorResult("content is required for daily")
	}

	now := time.Now()
	dateStr := now.Format("2006-01-02")
	dailyPath := filepath.Join(t.vaultDir, "daily", dateStr+".md")
	timeHeader := fmt.Sprintf("## %s", now.Format("15:04"))

	t.mu.Lock()
	defer t.mu.Unlock()

	existing, _ := os.ReadFile(dailyPath)

	var newContent string
	if len(existing) == 0 {
		// New daily note
		newContent = fmt.Sprintf("---\nkey: %s\ntags: [daily]\nfolder: daily\ncreated: %s\nupdated: %s\nlinks: []\n---\n\n# %s\n\n%s\n%s\n",
			dateStr, now.Format(time.RFC3339), now.Format(time.RFC3339), dateStr, timeHeader, content)
	} else {
		// Append with time header
		newContent = string(existing) + fmt.Sprintf("\n%s\n%s\n", timeHeader, content)
	}

	tmpPath := dailyPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(newContent), 0644); err != nil {
		return ErrorResult(fmt.Sprintf("failed to write daily: %v", err))
	}
	if err := os.Rename(tmpPath, dailyPath); err != nil {
		os.Remove(tmpPath)
		return ErrorResult(fmt.Sprintf("failed to save daily: %v", err))
	}

	// Update index
	links := extractWikilinks(newContent)
	t.index[dateStr] = &VaultNote{
		Key:     dateStr,
		Tags:    []string{"daily"},
		Folder:  "daily",
		Created: now.Format(time.RFC3339),
		Updated: now.Format(time.RFC3339),
		Links:   links,
		Content: newContent,
	}
	t.tfidf.addDocument(dateStr, dateStr+" daily "+newContent)

	return SilentResult(fmt.Sprintf("Appended to daily note %s", dateStr))
}

func (t *MemoryTool) link(args map[string]interface{}) *ToolResult {
	key, _ := args["key"].(string)
	if key == "" {
		return ErrorResult("key is required for link")
	}

	slug := vaultSlugify(key)

	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.index[slug] == nil {
		return SilentResult(fmt.Sprintf("No note found with key '%s'", slug))
	}

	var backlinks []string
	for _, note := range t.index {
		for _, link := range note.Links {
			if link == slug {
				backlinks = append(backlinks, fmt.Sprintf("- [%s] %s", note.Folder, note.Key))
				break
			}
		}
	}

	sort.Strings(backlinks)

	if len(backlinks) == 0 {
		return SilentResult(fmt.Sprintf("No backlinks to '%s'", slug))
	}
	return SilentResult(fmt.Sprintf("%d backlink(s) to '%s':\n%s", len(backlinks), slug, strings.Join(backlinks, "\n")))
}

// --- Vault helpers ---

var (
	reNonAlnum  = regexp.MustCompile(`[^a-z0-9-]+`)
	reMultiDash = regexp.MustCompile(`-{2,}`)
	reWikilink  = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
)

// vaultSlugify normalizes a key to a filesystem-safe slug.
func vaultSlugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer(
		"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u",
		"ñ", "n", "ü", "u",
		"_", "-", ".", "-", ":", "-", " ", "-",
	)
	s = replacer.Replace(s)
	s = reNonAlnum.ReplaceAllString(s, "-")
	s = reMultiDash.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "untitled"
	}
	return s
}

// inferFolder auto-routes a key to the appropriate vault folder.
func inferFolder(key string, tags []string) string {
	prefixes := []struct {
		patterns []string
		folder   string
	}{
		{[]string{"person-", "person_", "friends-", "friends_"}, "people"},
		{[]string{"preference", "prefs-", "prefs_", "style-", "style_"}, "preferences"},
		{[]string{"insight-", "insight_"}, "insights"},
		{[]string{"decision-", "decision_", "pattern-", "pattern_"}, "decisions"},
		{[]string{"blog-", "blog_", "editorial-", "editorial_", "post-", "post_", "svs-", "svs_"}, "blog"},
		{[]string{"last-", "last_", "heartbeat"}, "state"},
		{[]string{"project-", "project_"}, "projects"},
		{[]string{"daily-", "daily_", "eod-", "eod_", "morning-context"}, "daily"},
	}

	for _, p := range prefixes {
		for _, prefix := range p.patterns {
			if strings.HasPrefix(key, prefix) {
				return p.folder
			}
		}
	}

	// Check tags for hints
	tagSet := make(map[string]bool)
	for _, t := range tags {
		tagSet[strings.ToLower(t)] = true
	}
	if tagSet["preference"] || tagSet["correction"] {
		return "preferences"
	}
	if tagSet["pattern"] {
		return "decisions"
	}
	if tagSet["technical"] {
		return "insights"
	}
	if tagSet["network"] || tagSet["friends"] || tagSet["people"] {
		return "people"
	}

	return "inbox"
}

// extractWikilinks returns all [[wikilink]] targets from content.
func extractWikilinks(content string) []string {
	matches := reWikilink.FindAllStringSubmatch(content, -1)
	var links []string
	seen := make(map[string]bool)
	for _, m := range matches {
		slug := vaultSlugify(m[1])
		if !seen[slug] {
			links = append(links, slug)
			seen[slug] = true
		}
	}
	return links
}

// writeVaultNote writes a note as a markdown file with frontmatter (atomic write).
func writeVaultNote(vaultDir string, note *VaultNote) error {
	dir := filepath.Join(vaultDir, note.Folder)
	os.MkdirAll(dir, 0755)

	notePath := filepath.Join(dir, note.Key+".md")
	tmpPath := notePath + ".tmp"

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("key: %s\n", note.Key))
	b.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(note.Tags, ", ")))
	b.WriteString(fmt.Sprintf("folder: %s\n", note.Folder))
	b.WriteString(fmt.Sprintf("created: %s\n", note.Created))
	b.WriteString(fmt.Sprintf("updated: %s\n", note.Updated))
	if len(note.Links) > 0 {
		b.WriteString(fmt.Sprintf("links: [%s]\n", strings.Join(note.Links, ", ")))
	} else {
		b.WriteString("links: []\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(note.Content)
	if !strings.HasSuffix(note.Content, "\n") {
		b.WriteString("\n")
	}

	if err := os.WriteFile(tmpPath, []byte(b.String()), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, notePath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// parseVaultNote reads and parses a markdown file with frontmatter.
func parseVaultNote(path string) (*VaultNote, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	content := string(data)

	// Split by frontmatter delimiters
	if !strings.HasPrefix(content, "---\n") {
		// No frontmatter, treat whole thing as content
		base := strings.TrimSuffix(filepath.Base(path), ".md")
		return &VaultNote{
			Key:     base,
			Folder:  filepath.Base(filepath.Dir(path)),
			Content: content,
		}, nil
	}

	rest := content[4:] // skip opening "---\n"
	endIdx := strings.Index(rest, "\n---\n")
	if endIdx == -1 {
		return nil, fmt.Errorf("malformed frontmatter in %s", path)
	}

	frontmatter := rest[:endIdx]
	body := strings.TrimSpace(rest[endIdx+5:]) // skip "\n---\n"

	note := &VaultNote{
		Content: body,
		Folder:  filepath.Base(filepath.Dir(path)),
	}

	// Parse frontmatter lines
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		colonIdx := strings.Index(line, ": ")
		if colonIdx == -1 {
			continue
		}
		field := line[:colonIdx]
		value := line[colonIdx+2:]

		switch field {
		case "key":
			note.Key = value
		case "tags":
			note.Tags = parseBracketList(value)
		case "folder":
			note.Folder = value
		case "created":
			note.Created = value
		case "updated":
			note.Updated = value
		case "links":
			note.Links = parseBracketList(value)
		}
	}

	if note.Key == "" {
		note.Key = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	return note, nil
}

// buildIndex walks the vault and populates the in-memory index.
func (t *MemoryTool) buildIndex() {
	t.mu.Lock()
	defer t.mu.Unlock()

	filepath.Walk(t.vaultDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		note, err := parseVaultNote(path)
		if err != nil {
			return nil
		}

		t.index[note.Key] = note
		return nil
	})
}

// rebuildTFIDF populates the TF-IDF index from all notes in the vault index.
func (t *MemoryTool) rebuildTFIDF() {
	t.mu.RLock()
	defer t.mu.RUnlock()

	for key, note := range t.index {
		text := key + " " + note.Content + " " + strings.Join(note.Tags, " ")
		t.tfidf.addDocument(key, text)
	}
}

// --- Utility helpers ---

func parseBracketList(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil
	}
	var items []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

func extractStringSlice(args map[string]interface{}, key string) []string {
	raw, ok := args[key].([]interface{})
	if !ok {
		return nil
	}
	var result []string
	for _, v := range raw {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func containsTag(tags []string, tag string) bool {
	tag = strings.ToLower(tag)
	for _, t := range tags {
		if strings.ToLower(t) == tag {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
