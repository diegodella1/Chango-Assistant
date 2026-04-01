package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/knowledge"
)

func TestFinalizeResearchTopicMarksReadyAndNormalizesMeta(t *testing.T) {
	workspace := t.TempDir()
	loader := knowledge.NewLoader(workspace)
	tool := NewLearnTool(workspace, loader, nil)

	slug := "autonomia-agentes"
	if err := loader.SaveMeta(knowledge.KnowledgeMeta{
		Slug:       slug,
		Title:      "Autonomía de agentes",
		Status:     "researching",
		CreatedAt:  knowledge.Now(),
		UpdatedAt:  knowledge.Now(),
		Version:    1,
		AutoInject: true,
	}); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}

	content := "# Autonomía\n\nLa autonomía de agentes requiere planificación, herramientas, memoria y verificación."
	knowledgePath := filepath.Join(workspace, "knowledge", slug, "KNOWLEDGE.md")
	if err := os.WriteFile(knowledgePath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile KNOWLEDGE.md: %v", err)
	}

	tool.finalizeResearchTopic("Autonomía de agentes", "entender cómo investigar mejor", slug)

	if err := loader.RefreshIndex(); err != nil {
		t.Fatalf("RefreshIndex: %v", err)
	}

	var got knowledge.KnowledgeMeta
	found := false
	for _, meta := range loader.ListAll() {
		if meta.Slug == slug {
			got = meta
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("meta for %q not found", slug)
	}

	if got.Status != "ready" {
		t.Fatalf("expected status ready, got %q", got.Status)
	}
	if got.CharCount != len(content) {
		t.Fatalf("expected char_count %d, got %d", len(content), got.CharCount)
	}
	if !got.AutoInject {
		t.Fatalf("expected auto_inject=true")
	}
	if len(got.Keywords) == 0 {
		t.Fatalf("expected normalized keywords")
	}

	sourcesPath := filepath.Join(workspace, "knowledge", slug, "sources.json")
	if _, err := os.Stat(sourcesPath); err != nil {
		t.Fatalf("expected sources.json to be created: %v", err)
	}
}

func TestFinalizeResearchTopicMarksFailedWithoutKnowledgeFile(t *testing.T) {
	workspace := t.TempDir()
	loader := knowledge.NewLoader(workspace)
	tool := NewLearnTool(workspace, loader, nil)

	slug := "tema-incompleto"
	if err := loader.SaveMeta(knowledge.KnowledgeMeta{
		Slug:       slug,
		Title:      "Tema incompleto",
		Status:     "researching",
		CreatedAt:  knowledge.Now(),
		UpdatedAt:  knowledge.Now(),
		Version:    1,
		AutoInject: true,
	}); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}

	tool.finalizeResearchTopic("Tema incompleto", "", slug)

	if err := loader.RefreshIndex(); err != nil {
		t.Fatalf("RefreshIndex: %v", err)
	}

	var got knowledge.KnowledgeMeta
	found := false
	for _, meta := range loader.ListAll() {
		if meta.Slug == slug {
			got = meta
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("meta for %q not found", slug)
	}
	if got.Status != "failed" {
		t.Fatalf("expected status failed, got %q", got.Status)
	}
}

func TestBuildResearchPromptUsesRealToolNames(t *testing.T) {
	tool := NewLearnTool(t.TempDir(), knowledge.NewLoader(t.TempDir()), nil)
	prompt := tool.buildResearchPrompt("autonomía", "investigar mejor", "overview", "autonomia")

	for _, name := range []string{"web_search", "web_fetch", "write_file", "read_file", "memory", "message"} {
		if !strings.Contains(prompt, name) {
			t.Fatalf("expected prompt to mention tool %q", name)
		}
	}
}
