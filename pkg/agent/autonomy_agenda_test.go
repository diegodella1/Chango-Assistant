package agent

import "testing"

func TestBuildAutonomyAgendaSortsByPriorityAndAction(t *testing.T) {
	agenda := buildAutonomyAgenda([]TopicTrack{
		{Topic: "docs", Status: "active", Priority: 3, NextAction: "task", DecisionReason: "actionable"},
		{Topic: "deploy", Status: "active", Priority: 3, NextAction: "ask_user", DecisionReason: "high risk"},
		{Topic: "autonomia", Status: "active", Priority: 5, NextAction: "learn", DecisionReason: "missing knowledge"},
	})

	if len(agenda.Focus) != 3 {
		t.Fatalf("expected 3 focus items, got %d", len(agenda.Focus))
	}
	if agenda.Focus[0].SourceTopic != "autonomia" {
		t.Fatalf("expected highest priority item first, got %q", agenda.Focus[0].SourceTopic)
	}
	if agenda.Focus[1].SourceTopic != "deploy" {
		t.Fatalf("expected ask_user to beat task at same priority, got %q", agenda.Focus[1].SourceTopic)
	}
}

func TestBuildAutonomyAgendaSkipsIgnoredAndResolvedTracks(t *testing.T) {
	agenda := buildAutonomyAgenda([]TopicTrack{
		{Topic: "noise", Status: "active", Priority: 1, NextAction: "ignore"},
		{Topic: "done", Status: "resolved", Priority: 5, NextAction: "none"},
		{Topic: "real", Status: "active", Priority: 4, NextAction: "learn", DecisionReason: "recurrent"},
	})

	if len(agenda.Focus) != 1 {
		t.Fatalf("expected 1 focus item, got %d", len(agenda.Focus))
	}
	if agenda.Focus[0].SourceTopic != "real" {
		t.Fatalf("expected only real topic, got %q", agenda.Focus[0].SourceTopic)
	}
}
