package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestUpdateTopicTracksPersistsRecurringThemes(t *testing.T) {
	workspace := t.TempDir()
	history := []providers.Message{
		{Role: "user", Content: "Quiero mejorar la autonomia del agente."},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "La autonomia y la identidad son prioridad."},
	}

	updateTopicTracks(workspace, "Seguí con autonomia, identidad y pensamiento crítico.", history)

	tracks := loadTopicTracks(workspace)
	if len(tracks) == 0 {
		t.Fatalf("expected persisted tracks")
	}

	found := false
	for _, track := range tracks {
		if track.Topic == "autonomia" {
			found = true
			if track.Status != "active" {
				t.Fatalf("expected active status, got %q", track.Status)
			}
			if track.Mentions < 1 {
				t.Fatalf("expected mentions to be incremented")
			}
		}
	}
	if !found {
		t.Fatalf("expected autonomia track, got %+v", tracks)
	}
}

func TestUpdateTopicTracksAccumulatesMentions(t *testing.T) {
	workspace := t.TempDir()
	history := []providers.Message{
		{Role: "user", Content: "Autonomia del agente."},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "Autonomia e identidad propia."},
	}

	updateTopicTracks(workspace, "Quiero mas autonomia real.", history)
	updateTopicTracks(workspace, "Seguimos con autonomia y criterio.", history)

	tracks := loadTopicTracks(workspace)
	for _, track := range tracks {
		if track.Topic == "autonomia" {
			if track.Mentions < 2 {
				t.Fatalf("expected mentions to accumulate, got %d", track.Mentions)
			}
			return
		}
	}
	t.Fatalf("autonomia track not found")
}
