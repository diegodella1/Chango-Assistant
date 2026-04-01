package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

type AutonomyFocus struct {
	Goal             string `json:"goal"`
	SourceTopic      string `json:"source_topic"`
	NextAction       string `json:"next_action"`
	Reason           string `json:"reason"`
	Priority         int    `json:"priority"`
	RiskLevel        string `json:"risk_level,omitempty"`
	RequiresApproval bool   `json:"requires_approval,omitempty"`
}

type AutonomyAgenda struct {
	UpdatedAt time.Time       `json:"updated_at"`
	Focus     []AutonomyFocus `json:"focus"`
}

func refreshAutonomyAgenda(workspace string) AutonomyAgenda {
	tracks := reconcileTopicTracks(workspace)
	agenda := buildAutonomyAgenda(tracks)
	saveAutonomyAgenda(workspace, agenda)
	return agenda
}

func buildAutonomyAgenda(tracks []TopicTrack) AutonomyAgenda {
	var focus []AutonomyFocus
	for _, track := range tracks {
		if track.Status != "active" {
			continue
		}
		if track.NextAction == "none" || track.NextAction == "ignore" || track.NextAction == "cooldown" {
			continue
		}
		focus = append(focus, AutonomyFocus{
			Goal:             goalLabelForTrack(track),
			SourceTopic:      track.Topic,
			NextAction:       track.NextAction,
			Reason:           track.DecisionReason,
			Priority:         track.Priority,
			RiskLevel:        track.RiskLevel,
			RequiresApproval: track.RequiresApproval,
		})
	}

	sort.Slice(focus, func(i, j int) bool {
		if focus[i].Priority == focus[j].Priority {
			return agendaActionRank(focus[i].NextAction) < agendaActionRank(focus[j].NextAction)
		}
		return focus[i].Priority > focus[j].Priority
	})
	if len(focus) > 5 {
		focus = focus[:5]
	}

	return AutonomyAgenda{
		UpdatedAt: time.Now(),
		Focus:     focus,
	}
}

func goalLabelForTrack(track TopicTrack) string {
	switch track.NextAction {
	case "learn":
		return "Construir conocimiento persistente sobre " + track.Topic
	case "task":
		return "Abrir seguimiento operativo para " + track.Topic
	case "ask_user":
		return "Pedir decisión de Diego sobre " + track.Topic
	case "monitor":
		return "Monitorear progreso de " + track.Topic
	default:
		return "Seguir observando " + track.Topic
	}
}

func agendaActionRank(action string) int {
	switch action {
	case "ask_user":
		return 0
	case "learn":
		return 1
	case "task":
		return 2
	case "monitor":
		return 3
	case "watch":
		return 4
	default:
		return 5
	}
}

func autonomyAgendaPath(workspace string) string {
	return filepath.Join(workspace, "state", "autonomy_agenda.json")
}

func saveAutonomyAgenda(workspace string, agenda AutonomyAgenda) {
	path := autonomyAgendaPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		logger.WarnCF("agent", "Failed to create autonomy agenda directory", map[string]interface{}{"error": err.Error()})
		return
	}

	data, err := json.MarshalIndent(agenda, "", "  ")
	if err != nil {
		logger.WarnCF("agent", "Failed to marshal autonomy agenda", map[string]interface{}{"error": err.Error()})
		return
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		logger.WarnCF("agent", "Failed to write autonomy agenda", map[string]interface{}{"error": err.Error()})
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.WarnCF("agent", "Failed to save autonomy agenda", map[string]interface{}{"error": err.Error()})
	}
}

func loadAutonomyAgenda(workspace string) AutonomyAgenda {
	data, err := os.ReadFile(autonomyAgendaPath(workspace))
	if err != nil {
		return AutonomyAgenda{}
	}
	var agenda AutonomyAgenda
	if err := json.Unmarshal(data, &agenda); err != nil {
		logger.WarnCF("agent", "Failed to parse autonomy agenda", map[string]interface{}{"error": err.Error()})
		return AutonomyAgenda{}
	}
	return agenda
}
