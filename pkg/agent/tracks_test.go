package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/knowledge"
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

func TestEvaluateTopicTrackPromotesToLearnWhenKnowledgeMissing(t *testing.T) {
	workspace := t.TempDir()
	track := TopicTrack{
		Topic:       "autonomia",
		Mentions:    3,
		Status:      "active",
		LastMessage: "Quiero investigar autonomia real del agente.",
	}

	evaluateTopicTrack(workspace, &track)

	if track.NextAction != "learn" {
		t.Fatalf("expected learn, got %q", track.NextAction)
	}
}

func TestEvaluateTopicTrackPromotesToTaskWhenActionable(t *testing.T) {
	workspace := t.TempDir()
	track := TopicTrack{
		Topic:       "documentacion",
		Mentions:    2,
		Status:      "active",
		LastMessage: "Hay que revisar y ordenar la documentacion del proyecto.",
	}

	evaluateTopicTrack(workspace, &track)

	if track.NextAction != "task" {
		t.Fatalf("expected task, got %q", track.NextAction)
	}
}

func TestEvaluateTopicTrackRequiresApprovalForHighRisk(t *testing.T) {
	workspace := t.TempDir()
	track := TopicTrack{
		Topic:       "deploy",
		Mentions:    2,
		Status:      "active",
		LastMessage: "Hay que desplegar esto en producción y tocar config.",
	}

	evaluateTopicTrack(workspace, &track)

	if track.NextAction != "ask_user" {
		t.Fatalf("expected ask_user, got %q", track.NextAction)
	}
	if !track.RequiresApproval {
		t.Fatalf("expected requires_approval=true")
	}
}

func TestEvaluateTopicTrackRespectsCooldown(t *testing.T) {
	workspace := t.TempDir()
	track := TopicTrack{
		Topic:         "autonomia",
		Mentions:      5,
		Status:        "active",
		CooldownUntil: time.Now().Add(2 * time.Hour),
	}

	evaluateTopicTrack(workspace, &track)

	if track.NextAction != "cooldown" {
		t.Fatalf("expected cooldown, got %q", track.NextAction)
	}
}

func TestCreateTrackTaskAvoidsDuplicates(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "tasks"), 0755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}
	data, _ := json.Marshal([]map[string]any{
		{
			"title":  "Seguimiento autonomo: deploy",
			"status": "pending",
			"tags":   []string{"autonomy-track", "topic:deploy"},
		},
	})
	if err := os.WriteFile(filepath.Join(workspace, "tasks", "tasks.json"), data, 0644); err != nil {
		t.Fatalf("write tasks: %v", err)
	}

	created := createTrackTask(workspace, TopicTrack{Topic: "deploy", Priority: 4})
	if created {
		t.Fatalf("expected duplicate task creation to be skipped")
	}
}

func TestDecideTrackPolicyIgnoresWeakSignals(t *testing.T) {
	track := &TopicTrack{
		Topic:      "idea",
		Mentions:   1,
		Confidence: 0.4,
	}

	action, _ := decideTrackPolicy(track)
	if action != "ignore" {
		t.Fatalf("expected ignore, got %q", action)
	}
}

func TestReconcileTopicTracksResolvesReadyKnowledge(t *testing.T) {
	workspace := t.TempDir()
	now := time.Now()
	saveTopicTracks(workspace, []TopicTrack{
		{
			Topic:          "autonomia",
			Status:         "active",
			NextAction:     "monitor",
			KnowledgeStatus: "researching",
			Mentions:       4,
			FirstSeenAt:    now,
			LastSeenAt:     now,
		},
	})

	loader := knowledge.NewLoader(workspace)
	if err := loader.SaveMeta(knowledge.KnowledgeMeta{
		Slug:       "autonomia",
		Title:      "autonomia",
		Status:     "ready",
		CreatedAt:  knowledge.Now(),
		UpdatedAt:  knowledge.Now(),
		Version:    1,
		AutoInject: true,
	}); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}

	tracks := reconcileTopicTracks(workspace)
	if len(tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(tracks))
	}
	if tracks[0].Status != "resolved" {
		t.Fatalf("expected resolved status, got %q", tracks[0].Status)
	}
	if tracks[0].NextAction != "none" {
		t.Fatalf("expected next_action none, got %q", tracks[0].NextAction)
	}
}

func TestReconcileTopicTracksResolvesDoneTask(t *testing.T) {
	workspace := t.TempDir()
	now := time.Now()
	saveTopicTracks(workspace, []TopicTrack{
		{
			Topic:       "deploy",
			Status:      "active",
			NextAction:  "monitor",
			TaskStatus:  "pending",
			Mentions:    3,
			FirstSeenAt: now,
			LastSeenAt:  now,
		},
	})

	if err := os.MkdirAll(filepath.Join(workspace, "tasks"), 0755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}
	data, _ := json.Marshal([]map[string]any{
		{
			"title":  "Seguimiento autonomo: deploy",
			"status": "done",
			"tags":   []string{"autonomy-track", "topic:deploy"},
		},
	})
	if err := os.WriteFile(filepath.Join(workspace, "tasks", "tasks.json"), data, 0644); err != nil {
		t.Fatalf("write tasks: %v", err)
	}

	tracks := reconcileTopicTracks(workspace)
	if len(tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(tracks))
	}
	if tracks[0].Status != "resolved" {
		t.Fatalf("expected resolved status, got %q", tracks[0].Status)
	}
	if tracks[0].TaskStatus != "done" {
		t.Fatalf("expected task status done, got %q", tracks[0].TaskStatus)
	}
}
