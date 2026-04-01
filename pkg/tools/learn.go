package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/knowledge"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// LearnTool enables deep learning on arbitrary topics via web research.
type LearnTool struct {
	workspace      string
	knowledgeLoader *knowledge.Loader
	subagentMgr    *SubagentManager
	channel        string
	chatID         string
}

func NewLearnTool(workspace string, loader *knowledge.Loader, subagentMgr *SubagentManager) *LearnTool {
	return &LearnTool{
		workspace:       workspace,
		knowledgeLoader: loader,
		subagentMgr:     subagentMgr,
	}
}

func (t *LearnTool) Name() string { return "learn" }

func (t *LearnTool) Description() string {
	return "Research and learn about a topic in depth. Spawns a research subagent that investigates via web search, synthesizes knowledge, and stores it for future reference. Actions: start, status, list, refresh."
}

func (t *LearnTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"start", "status", "list", "refresh"},
				"description": "Action to perform",
			},
			"topic": map[string]interface{}{
				"type":        "string",
				"description": "Topic to research (required for start/refresh)",
			},
			"purpose": map[string]interface{}{
				"type":        "string",
				"description": "Why this topic matters or what to focus on (optional, helps guide research)",
			},
			"depth": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"overview", "detailed", "deep"},
				"description": "Research depth: overview (quick), detailed (default), deep (thorough)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *LearnTool) SetContext(channel, chatID string) {
	t.channel = channel
	t.chatID = chatID
}

func (t *LearnTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "start":
		return t.start(ctx, args)
	case "status":
		return t.status(args)
	case "list":
		return t.list()
	case "refresh":
		return t.refresh(ctx, args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *LearnTool) start(ctx context.Context, args map[string]interface{}) *ToolResult {
	topic, _ := args["topic"].(string)
	if topic == "" {
		return ErrorResult("topic is required for start")
	}

	purpose, _ := args["purpose"].(string)
	depth, _ := args["depth"].(string)
	if depth == "" {
		depth = "detailed"
	}

	slug := slugify(topic)

	// Check if already exists and is ready
	if content, err := t.knowledgeLoader.LoadContent(slug); err == nil && content != "" {
		return SilentResult(fmt.Sprintf("Topic '%s' already has knowledge. Use action 'refresh' to update it.", topic))
	}

	// Create META.json with status "researching"
	meta := knowledge.KnowledgeMeta{
		Slug:        slug,
		Title:       topic,
		Description: purpose,
		Keywords:    generateKeywords(topic),
		Status:      "researching",
		CreatedAt:   knowledge.Now(),
		UpdatedAt:   knowledge.Now(),
		Version:     1,
		AutoInject:  true,
	}
	if err := t.knowledgeLoader.SaveMeta(meta); err != nil {
		return ErrorResult(fmt.Sprintf("failed to create topic directory: %v", err))
	}

	// Build research prompt
	prompt := t.buildResearchPrompt(topic, purpose, depth, slug)

	// Spawn research subagent
	if t.subagentMgr == nil {
		return ErrorResult("subagent manager not available")
	}

	_, err := t.subagentMgr.Spawn(ctx, prompt, fmt.Sprintf("learn:%s", slug), t.channel, t.chatID, func(callbackCtx context.Context, result *ToolResult) {
		t.finalizeResearchTopic(topic, purpose, slug)
	})
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to spawn research subagent: %v", err))
	}

	return AsyncResult(fmt.Sprintf("Investigando '%s'... Te aviso cuando termine.", topic))
}

func (t *LearnTool) status(args map[string]interface{}) *ToolResult {
	topic, _ := args["topic"].(string)
	if topic == "" {
		return ErrorResult("topic is required for status")
	}
	slug := slugify(topic)

	all := t.knowledgeLoader.ListAll()
	for _, meta := range all {
		if meta.Slug == slug {
			return SilentResult(fmt.Sprintf("Topic '%s': status=%s, version=%d, chars=%d, updated=%s",
				meta.Title, meta.Status, meta.Version, meta.CharCount, meta.UpdatedAt))
		}
	}
	return SilentResult(fmt.Sprintf("Topic '%s' not found", topic))
}

func (t *LearnTool) list() *ToolResult {
	all := t.knowledgeLoader.ListAll()
	if len(all) == 0 {
		return SilentResult("No knowledge topics found. Use learn(action='start', topic='...') to start learning.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Knowledge topics (%d):\n", len(all)))
	for _, meta := range all {
		sb.WriteString(fmt.Sprintf("- %s [%s] (%d chars, v%d)\n", meta.Title, meta.Status, meta.CharCount, meta.Version))
	}
	return SilentResult(sb.String())
}

func (t *LearnTool) refresh(ctx context.Context, args map[string]interface{}) *ToolResult {
	topic, _ := args["topic"].(string)
	if topic == "" {
		return ErrorResult("topic is required for refresh")
	}

	slug := slugify(topic)
	all := t.knowledgeLoader.ListAll()
	found := false
	for _, meta := range all {
		if meta.Slug == slug {
			found = true
			break
		}
	}
	if !found {
		return ErrorResult(fmt.Sprintf("topic '%s' not found. Use action 'start' first.", topic))
	}

	// Re-start research
	args["action"] = "start"
	// Force re-research by temporarily removing knowledge content
	purpose, _ := args["purpose"].(string)
	depth, _ := args["depth"].(string)
	if depth == "" {
		depth = "detailed"
	}

	prompt := t.buildResearchPrompt(topic, purpose, depth, slug)

	if t.subagentMgr == nil {
		return ErrorResult("subagent manager not available")
	}

	_, err := t.subagentMgr.Spawn(ctx, prompt, fmt.Sprintf("learn-refresh:%s", slug), t.channel, t.chatID, func(callbackCtx context.Context, result *ToolResult) {
		t.finalizeResearchTopic(topic, purpose, slug)
	})
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to spawn research subagent: %v", err))
	}

	return AsyncResult(fmt.Sprintf("Refreshing knowledge on '%s'...", topic))
}

func (t *LearnTool) buildResearchPrompt(topic, purpose, depth, slug string) string {
	searchCount := 3
	fetchCount := 2
	wordRange := "500-1000"
	switch depth {
	case "overview":
		searchCount = 2
		fetchCount = 1
		wordRange = "300-500"
	case "deep":
		searchCount = 5
		fetchCount = 3
		wordRange = "1000-2000"
	}

	knowledgePath := filepath.Join(t.workspace, "knowledge", slug)

	purposeSection := ""
	if purpose != "" {
		purposeSection = fmt.Sprintf("\nFocus: %s\n", purpose)
	}

	return fmt.Sprintf(`[AUTONOMOUS RESEARCH] Learn about: %s
%s
Instructions:
1. RESEARCH: Use web_search tool to find %d diverse, authoritative sources about this topic.
2. DEEP READ: Use web_fetch tool to read %d of the most promising pages in full.
3. SYNTHESIZE: Write a comprehensive knowledge document (%s words) covering:
   - Key concepts and definitions
   - Important facts, dates, or figures
   - Common patterns or principles
   - Practical applications or implications
   - Notable controversies or open questions
4. SAVE: Use write_file tool to save the synthesized knowledge to: %s/KNOWLEDGE.md
   Format: Markdown with clear headers, bullet points where appropriate.
5. UPDATE META: Use write_file tool to update %s/META.json with the best metadata you can.
   Importante: el sistema normaliza status/char_count/keywords al terminar, así que priorizá que exista un JSON válido.
6. SOURCES: Use write_file tool to save source URLs to %s/sources.json as a JSON array of objects with "url" and "title" fields.
7. NOTIFY: Use message tool to send a short summary (2-3 lines) to the user about what you learned.

Usá exactamente estos nombres de tools cuando haga falta: web_search, web_fetch, write_file, read_file, memory, message.

Write in Spanish. Be thorough but concise. Focus on actionable knowledge.`,
		topic, purposeSection, searchCount, fetchCount, wordRange,
		knowledgePath, knowledgePath, knowledgePath)
}

func (t *LearnTool) finalizeResearchTopic(topic, purpose, slug string) {
	if t.knowledgeLoader == nil {
		return
	}

	meta := knowledge.KnowledgeMeta{
		Slug:        slug,
		Title:       topic,
		Description: purpose,
		Keywords:    generateKeywords(topic),
		Status:      "researching",
		CreatedAt:   knowledge.Now(),
		UpdatedAt:   knowledge.Now(),
		Version:     1,
		AutoInject:  true,
	}

	for _, existing := range t.knowledgeLoader.ListAll() {
		if existing.Slug == slug {
			meta = existing
			break
		}
	}

	knowledgePath := filepath.Join(t.workspace, "knowledge", slug, "KNOWLEDGE.md")
	content, err := os.ReadFile(knowledgePath)
	if err != nil || strings.TrimSpace(string(content)) == "" {
		meta.Status = "failed"
		meta.UpdatedAt = knowledge.Now()
		if saveErr := t.knowledgeLoader.SaveMeta(meta); saveErr != nil {
			logger.WarnCF("learn", "Failed to persist failed research metadata",
				map[string]interface{}{"topic": topic, "slug": slug, "error": saveErr.Error()})
			return
		}
		_ = t.knowledgeLoader.RefreshIndex()
		logger.WarnCF("learn", "Research finished without knowledge content",
			map[string]interface{}{"topic": topic, "slug": slug})
		return
	}

	meta.Title = topic
	meta.Description = purpose
	meta.Status = "ready"
	meta.UpdatedAt = knowledge.Now()
	meta.CharCount = len(content)
	meta.AutoInject = true
	if meta.CreatedAt == "" {
		meta.CreatedAt = meta.UpdatedAt
	}
	if meta.Version <= 0 {
		meta.Version = 1
	}
	meta.Keywords = mergeKeywords(meta.Keywords, generateKeywords(topic), extractKeywords(string(content), 8))

	if saveErr := t.knowledgeLoader.SaveMeta(meta); saveErr != nil {
		logger.WarnCF("learn", "Failed to persist finalized research metadata",
			map[string]interface{}{"topic": topic, "slug": slug, "error": saveErr.Error()})
		return
	}

	sourcesPath := filepath.Join(t.workspace, "knowledge", slug, "sources.json")
	if _, err := os.Stat(sourcesPath); err != nil {
		emptySources, _ := json.Marshal([]map[string]string{})
		if writeErr := os.WriteFile(sourcesPath, emptySources, 0644); writeErr != nil {
			logger.WarnCF("learn", "Failed to create empty sources.json",
				map[string]interface{}{"topic": topic, "slug": slug, "error": writeErr.Error()})
		}
	}

	if err := t.knowledgeLoader.RefreshIndex(); err != nil {
		logger.WarnCF("learn", "Research completed but failed to refresh index",
			map[string]interface{}{"topic": topic, "slug": slug, "error": err.Error()})
		return
	}
	logger.InfoCF("learn", "Research completed, metadata normalized",
		map[string]interface{}{"topic": topic, "slug": slug, "char_count": meta.CharCount})
}

// slugify converts a topic name to a URL-safe slug.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Replace accented characters
	replacer := strings.NewReplacer(
		"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u",
		"ñ", "n", "ü", "u",
	)
	s = replacer.Replace(s)
	// Replace non-alphanumeric with hyphens
	reg := regexp.MustCompile(`[^a-z0-9]+`)
	s = reg.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// generateKeywords extracts keywords from a topic string.
func generateKeywords(topic string) []string {
	words := strings.Fields(strings.ToLower(topic))
	var keywords []string
	stopWords := map[string]bool{
		"de": true, "la": true, "el": true, "los": true, "las": true,
		"en": true, "del": true, "al": true, "un": true, "una": true,
		"y": true, "o": true, "a": true, "por": true, "con": true,
		"sobre": true, "para": true, "the": true, "of": true, "and": true,
		"in": true, "on": true, "for": true, "to": true, "is": true,
	}
	for _, w := range words {
		w = strings.Trim(w, ".,!?¿¡;:\"'()[]{}…")
		if len(w) >= 3 && !stopWords[w] {
			keywords = append(keywords, w)
		}
	}
	return keywords
}

func extractKeywords(content string, limit int) []string {
	if limit <= 0 {
		limit = 8
	}

	stopWords := map[string]bool{
		"de": true, "la": true, "el": true, "los": true, "las": true,
		"en": true, "del": true, "al": true, "un": true, "una": true,
		"y": true, "o": true, "a": true, "por": true, "con": true,
		"sobre": true, "para": true, "que": true, "como": true,
		"the": true, "of": true, "and": true, "in": true, "on": true,
		"for": true, "to": true, "is": true, "are": true,
	}

	content = strings.ToLower(content)
	wordRe := regexp.MustCompile(`[a-z0-9áéíóúñü]{4,}`)
	counts := map[string]int{}
	for _, word := range wordRe.FindAllString(content, -1) {
		if stopWords[word] {
			continue
		}
		counts[word]++
	}

	var keywords []string
	for len(keywords) < limit {
		bestWord := ""
		bestCount := 0
		for word, count := range counts {
			if count > bestCount {
				bestWord = word
				bestCount = count
			}
		}
		if bestWord == "" {
			break
		}
		keywords = append(keywords, bestWord)
		delete(counts, bestWord)
	}
	return keywords
}

func mergeKeywords(groups ...[]string) []string {
	seen := make(map[string]bool)
	var merged []string
	for _, group := range groups {
		for _, word := range group {
			word = strings.TrimSpace(strings.ToLower(word))
			if len(word) < 3 || seen[word] {
				continue
			}
			seen[word] = true
			merged = append(merged, word)
		}
	}
	return merged
}
