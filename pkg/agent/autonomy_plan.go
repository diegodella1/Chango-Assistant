package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

type GoalStep struct {
	Title      string `json:"title"`
	ActionType string `json:"action_type"`
	Status     string `json:"status"`
}

type GoalNode struct {
	Goal             string     `json:"goal"`
	SourceTopic      string     `json:"source_topic"`
	Priority         int        `json:"priority"`
	NextAction       string     `json:"next_action"`
	ExecutableStep   string     `json:"executable_step"`
	Status           string     `json:"status"`
	Reason           string     `json:"reason"`
	RiskLevel        string     `json:"risk_level,omitempty"`
	RequiresApproval bool       `json:"requires_approval,omitempty"`
	BlockedBy        string     `json:"blocked_by,omitempty"`
	DependsOn        []string   `json:"depends_on,omitempty"`
	Subgoals         []GoalStep `json:"subgoals,omitempty"`
}

type AutonomyPlan struct {
	UpdatedAt          time.Time  `json:"updated_at"`
	PrimaryGoal        *GoalNode  `json:"primary_goal,omitempty"`
	PrimaryGoalReason  string     `json:"primary_goal_reason,omitempty"`
	CurrentStep        string     `json:"current_step,omitempty"`
	Supporting         []GoalNode `json:"supporting,omitempty"`
}

func refreshAutonomyPlan(workspace string) AutonomyPlan {
	agenda := loadAutonomyAgenda(workspace)
	plan := buildAutonomyPlan(agenda)
	saveAutonomyPlan(workspace, plan)
	return plan
}

func buildAutonomyPlan(agenda AutonomyAgenda) AutonomyPlan {
	plan := AutonomyPlan{UpdatedAt: time.Now()}
	if len(agenda.Focus) == 0 {
		return plan
	}

	nodes := make([]GoalNode, 0, len(agenda.Focus))
	for _, focus := range agenda.Focus {
		node := GoalNode{
			Goal:             focus.Goal,
			SourceTopic:      focus.SourceTopic,
			Priority:         focus.Priority,
			NextAction:       focus.NextAction,
			ExecutableStep:   executableStepForFocus(focus),
			Status:           goalStatusForFocus(focus),
			Reason:           focus.Reason,
			RiskLevel:        focus.RiskLevel,
			RequiresApproval: focus.RequiresApproval,
			BlockedBy:        blockedByForFocus(focus),
			DependsOn:        dependenciesForFocus(focus),
			Subgoals:         subgoalsForFocus(focus),
		}
		nodes = append(nodes, node)
	}

	primaryIdx, reason := selectPrimaryGoal(nodes)
	plan.PrimaryGoalReason = reason
	plan.PrimaryGoal = &nodes[primaryIdx]
	plan.CurrentStep = nodes[primaryIdx].ExecutableStep
	for i, node := range nodes {
		if i == primaryIdx {
			continue
		}
		plan.Supporting = append(plan.Supporting, node)
	}
	return plan
}

func executableStepForFocus(focus AutonomyFocus) string {
	switch focus.NextAction {
	case "ask_user":
		return "Mandar pedido de aprobación a Diego"
	case "learn":
		return "Arrancar research overview y guardar knowledge persistente"
	case "task":
		return "Crear o retomar task operativo asociado"
	case "monitor":
		return "Verificar progreso del trabajo ya iniciado"
	default:
		return "Seguir observando señales antes de actuar"
	}
}

func goalStatusForFocus(focus AutonomyFocus) string {
	switch focus.NextAction {
	case "ask_user":
		return "blocked"
	case "monitor":
		return "in_progress"
	default:
		return "ready"
	}
}

func blockedByForFocus(focus AutonomyFocus) string {
	if focus.RequiresApproval || focus.NextAction == "ask_user" {
		return "approval_from_diego"
	}
	return ""
}

func dependenciesForFocus(focus AutonomyFocus) []string {
	switch focus.NextAction {
	case "ask_user":
		return nil
	case "task":
		if focus.RequiresApproval {
			return []string{"approval_from_diego"}
		}
		return []string{"task_created:" + focus.SourceTopic}
	case "monitor":
		if focus.RequiresApproval {
			return []string{"approval_from_diego"}
		}
		return []string{"existing_work:" + focus.SourceTopic}
	case "learn":
		return []string{"research_slot_available"}
	default:
		return nil
	}
}

func subgoalsForFocus(focus AutonomyFocus) []GoalStep {
	switch focus.NextAction {
	case "ask_user":
		return []GoalStep{
			{Title: "Explicar riesgo y contexto", ActionType: "ask_user", Status: "pending"},
			{Title: "Esperar decisión de Diego", ActionType: "approval", Status: "blocked"},
		}
	case "learn":
		return []GoalStep{
			{Title: "Buscar fuentes confiables", ActionType: "learn", Status: "pending"},
			{Title: "Sintetizar knowledge persistente", ActionType: "learn", Status: "pending"},
			{Title: "Verificar que el topic quedó ready", ActionType: "verify", Status: "pending"},
		}
	case "task":
		return []GoalStep{
			{Title: "Abrir task de seguimiento", ActionType: "task", Status: "pending"},
			{Title: "Tomar próximo paso ejecutable", ActionType: "task", Status: "pending"},
			{Title: "Verificar avance o cierre", ActionType: "verify", Status: "pending"},
		}
	case "monitor":
		return []GoalStep{
			{Title: "Chequear estado actual", ActionType: "monitor", Status: "pending"},
			{Title: "Resolver o replanificar según resultado", ActionType: "verify", Status: "pending"},
		}
	default:
		return []GoalStep{
			{Title: "Recolectar más evidencia", ActionType: "watch", Status: "pending"},
		}
	}
}

func selectPrimaryGoal(nodes []GoalNode) (int, string) {
	if len(nodes) == 0 {
		return 0, ""
	}

	bestReady := -1
	bestActive := -1
	bestBlocked := 0
	for i, node := range nodes {
		switch node.Status {
		case "ready":
			if bestReady == -1 || node.Priority > nodes[bestReady].Priority {
				bestReady = i
			}
		case "in_progress":
			if bestActive == -1 || node.Priority > nodes[bestActive].Priority {
				bestActive = i
			}
		case "blocked":
			if node.Priority > nodes[bestBlocked].Priority {
				bestBlocked = i
			}
		}
	}

	if bestReady >= 0 {
		return bestReady, "selected highest-priority executable goal"
	}
	if bestActive >= 0 {
		return bestActive, "selected highest-priority in-progress goal"
	}
	return bestBlocked, "all goals blocked, keeping highest-priority blocked goal visible"
}

func autonomyPlanPath(workspace string) string {
	return filepath.Join(workspace, "state", "autonomy_plan.json")
}

func saveAutonomyPlan(workspace string, plan AutonomyPlan) {
	path := autonomyPlanPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		logger.WarnCF("agent", "Failed to create autonomy plan directory", map[string]interface{}{"error": err.Error()})
		return
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		logger.WarnCF("agent", "Failed to marshal autonomy plan", map[string]interface{}{"error": err.Error()})
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		logger.WarnCF("agent", "Failed to write autonomy plan", map[string]interface{}{"error": err.Error()})
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.WarnCF("agent", "Failed to save autonomy plan", map[string]interface{}{"error": err.Error()})
	}
}

func loadAutonomyPlan(workspace string) AutonomyPlan {
	data, err := os.ReadFile(autonomyPlanPath(workspace))
	if err != nil {
		return AutonomyPlan{}
	}
	var plan AutonomyPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		logger.WarnCF("agent", "Failed to parse autonomy plan", map[string]interface{}{"error": err.Error()})
		return AutonomyPlan{}
	}
	return plan
}
