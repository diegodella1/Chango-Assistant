package agent

import "testing"

func TestResponseRepairHint_DetectsInternalLeakage(t *testing.T) {
	hint := responseRepairHint("de hardware digo", "### Agenda for this Conversation\n\n### Steps to Take")
	if hint == "" {
		t.Fatal("expected repair hint for leaked internal agenda")
	}
}

func TestResponseRepairHint_DetectsGenericGreetingOnNonGreeting(t *testing.T) {
	hint := responseRepairHint("que sabes de la mision artemis 2?", "Hello! How can I assist you today?")
	if hint == "" {
		t.Fatal("expected repair hint for generic greeting fallback")
	}
}

func TestResponseRepairHint_AllowsGreetingReplyToGreeting(t *testing.T) {
	hint := responseRepairHint("hola chango", "Hello! How can I assist you today?")
	if hint != "" {
		t.Fatalf("did not expect repair hint, got %q", hint)
	}
}

func TestResponseRepairHint_DetectsLiveDataDenial(t *testing.T) {
	hint := responseRepairHint("Que temperatura tiene la cpu ahora?", "I don't have access to real-time information about your computer's temperature.")
	if hint == "" {
		t.Fatal("expected repair hint for hardware denial")
	}
}

func TestShouldPersistAssistantResponse_RejectsPoisoningFallbacks(t *testing.T) {
	if shouldPersistAssistantResponse("que sabes de artemis 2", "Hello! How can I assist you today?", "") {
		t.Fatal("expected generic fallback greeting to be rejected from persistence")
	}
	if shouldPersistAssistantResponse("hola", "I've completed processing but have no response to give.", "I've completed processing but have no response to give.") {
		t.Fatal("expected default fallback to be rejected from persistence")
	}
	if !shouldPersistAssistantResponse("hola", "Todo bien. Decime.", "") {
		t.Fatal("expected natural short response to be persisted")
	}
}
