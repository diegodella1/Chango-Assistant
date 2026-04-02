package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/state"
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
	UpdatedAt         time.Time  `json:"updated_at"`
	PrimaryGoal       *GoalNode  `json:"primary_goal,omitempty"`
	PrimaryGoalReason string     `json:"primary_goal_reason,omitempty"`
	CurrentStep       string     `json:"current_step,omitempty"`
	Supporting        []GoalNode `json:"supporting,omitempty"`
}

func refreshAutonomyPlan(workspace string) AutonomyPlan {
	agenda := loadAutonomyAgenda(workspace)
	plan := buildAutonomyPlan(agenda)
	saveAutonomyPlan(workspace, plan)
	syncAutonomyOperationalState(workspace, plan)
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
		return "waiting_external"
	case "monitor":
		return "in_progress"
	default:
		return "pending"
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
	bestWaiting := -1
	bestBlocked := 0
	for i, node := range nodes {
		switch node.Status {
		case "pending":
			if bestReady == -1 || node.Priority > nodes[bestReady].Priority {
				bestReady = i
			}
		case "in_progress":
			if bestActive == -1 || node.Priority > nodes[bestActive].Priority {
				bestActive = i
			}
		case "waiting_external":
			if bestWaiting == -1 || node.Priority > nodes[bestWaiting].Priority {
				bestWaiting = i
			}
		case "blocked", "verification_failed":
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
	if bestWaiting >= 0 {
		return bestWaiting, "selected highest-priority waiting goal"
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

func syncAutonomyOperationalState(workspace string, plan AutonomyPlan) {
	sm := state.NewManager(workspace)
	now := time.Now().UTC()

	existingTasks := sm.GetTasks()
	manualTasks := make([]state.TaskRecord, 0, len(existingTasks))
	for _, task := range existingTasks {
		if !strings.HasPrefix(task.ID, "autonomy_goal:") {
			manualTasks = append(manualTasks, task)
		}
	}

	goals := make([]GoalNode, 0, len(plan.Supporting)+1)
	if plan.PrimaryGoal != nil {
		goals = append(goals, *plan.PrimaryGoal)
	}
	goals = append(goals, plan.Supporting...)

	autonomyTasks := make([]state.TaskRecord, 0, len(goals))
	autonomyFollowUps := make([]state.FollowUpRecord, 0, len(goals))
	for _, goal := range goals {
		taskID := "autonomy_goal:" + slugGoal(goal.Goal)
		createdAt := now.Format(time.RFC3339)
		for _, existing := range existingTasks {
			if existing.ID == taskID && existing.CreatedAt != "" {
				createdAt = existing.CreatedAt
				break
			}
		}

		autonomyTasks = append(autonomyTasks, state.TaskRecord{
			ID:          taskID,
			Title:       goal.Goal,
			Description: goal.Reason,
			Status:      goal.Status,
			Priority:    priorityBand(goal.Priority),
			Notes:       goal.ExecutableStep,
			GoalID:      goal.SourceTopic,
			Tags:        []string{"autonomy", goal.SourceTopic},
			CreatedAt:   createdAt,
			UpdatedAt:   now.Format(time.RFC3339),
		})

		if followUp := buildAutonomyFollowUp(goal, now, taskID); followUp != nil {
			autonomyFollowUps = append(autonomyFollowUps, *followUp)
		}
	}

	if err := sm.ReplaceTasks(append(manualTasks, autonomyTasks...)); err != nil {
		logger.WarnCF("agent", "Failed to sync autonomy tasks", map[string]interface{}{"error": err.Error()})
	}

	existingFollowUps := sm.GetFollowUps()
	manualFollowUps := make([]state.FollowUpRecord, 0, len(existingFollowUps))
	for _, followUp := range existingFollowUps {
		if !strings.HasPrefix(followUp.ID, "autonomy_followup:") {
			manualFollowUps = append(manualFollowUps, followUp)
		}
	}
	if err := sm.ReplaceFollowUps(append(manualFollowUps, autonomyFollowUps...)); err != nil {
		logger.WarnCF("agent", "Failed to sync autonomy follow-ups", map[string]interface{}{"error": err.Error()})
	}
}

func buildAutonomyFollowUp(goal GoalNode, now time.Time, referenceID string) *state.FollowUpRecord {
	status := strings.TrimSpace(goal.Status)
	if status == "" || status == "done" || status == "cancelled" {
		return nil
	}

	var delay time.Duration
	switch status {
	case "waiting_external":
		delay = 12 * time.Hour
	case "blocked", "verification_failed":
		delay = 6 * time.Hour
	case "in_progress":
		delay = 2 * time.Hour
	default:
		delay = 4 * time.Hour
	}

	return &state.FollowUpRecord{
		ID:          "autonomy_followup:" + slugGoal(goal.Goal),
		Title:       goal.Goal,
		NextCheckAt: now.Add(delay).Format(time.RFC3339),
		Reason:      goal.ExecutableStep,
		Status:      status,
		ReferenceID: referenceID,
		CreatedAt:   now.Format(time.RFC3339),
		UpdatedAt:   now.Format(time.RFC3339),
	}
}

func priorityBand(priority int) string {
	switch {
	case priority >= 8:
		return "high"
	case priority >= 5:
		return "medium"
	default:
		return "low"
	}
}

func slugGoal(goal string) string {
	goal = strings.ToLower(strings.TrimSpace(goal))
	if goal == "" {
		return "unnamed"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range goal {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "unnamed"
	}
	return result
}
