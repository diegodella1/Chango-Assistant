package agent

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestDetectRecurringTopicsFindsRepeatedUserTheme(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "Quiero mejorar la autonomia del agente y su memoria."},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "La autonomia me importa mas que PDFs o documentos."},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "Tambien quiero pensamiento critico y autonomia real."},
	}

	topics := detectRecurringTopics("Seguí con la autonomia y la identidad propia", history, 3)
	if len(topics) == 0 {
		t.Fatalf("expected recurring topics, got none")
	}

	found := false
	for _, topic := range topics {
		if topic == "autonomia" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected autonomia to be detected, got %v", topics)
	}
}

func TestShouldChallengeUserFlagsRiskyApprovalRequest(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "Estamos tocando producción y despliegues."},
		{Role: "assistant", Content: "ok"},
	}

	if !shouldChallengeUser("¿Te parece bien si lo saco a producción rápido sin revisar?", history) {
		t.Fatalf("expected risky approval request to require challenge")
	}
}

func TestBuildAutonomyHintIncludesRecurringTopicsAndChallenge(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "La autonomia del agente importa mucho."},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "Quiero autonomia y pensamiento critico."},
	}

	hint := buildAutonomyHint("¿Te parece bien sacar esto a producción rápido para ganar autonomia?", history)
	if !strings.Contains(hint, "Recurring topics") {
		t.Fatalf("expected recurring topics in hint, got %q", hint)
	}
	if !strings.Contains(hint, "Critical thinking mode") {
		t.Fatalf("expected challenge bias in hint, got %q", hint)
	}
}

func TestNormalizeIdentityBoilerplateRemovesGenericAIDisclaimer(t *testing.T) {
	got := normalizeIdentityBoilerplate("Como modelo de lenguaje, no puedo saber eso.")
	if strings.Contains(strings.ToLower(got), "modelo de lenguaje") {
		t.Fatalf("expected generic identity boilerplate to be removed, got %q", got)
	}
	if !strings.Contains(got, "Chango") {
		t.Fatalf("expected identity-normalized response to mention Chango, got %q", got)
	}
}
