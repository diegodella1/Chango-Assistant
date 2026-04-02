package agent

import "testing"

func TestBuildAutonomyPlanCreatesPrimaryAndSupportingGoals(t *testing.T) {
	agenda := AutonomyAgenda{
		Focus: []AutonomyFocus{
			{
				Goal:        "Construir conocimiento persistente sobre autonomia",
				SourceTopic: "autonomia",
				NextAction:  "learn",
				Reason:      "recurrent topic",
				Priority:    5,
			},
			{
				Goal:        "Monitorear progreso de deploy",
				SourceTopic: "deploy",
				NextAction:  "monitor",
				Reason:      "work in progress",
				Priority:    3,
			},
		},
	}

	plan := buildAutonomyPlan(agenda)
	if plan.PrimaryGoal == nil {
		t.Fatalf("expected primary goal")
	}
	if plan.PrimaryGoal.SourceTopic != "autonomia" {
		t.Fatalf("expected autonomia as primary, got %q", plan.PrimaryGoal.SourceTopic)
	}
	if len(plan.Supporting) != 1 {
		t.Fatalf("expected 1 supporting goal, got %d", len(plan.Supporting))
	}
	if plan.PrimaryGoal.ExecutableStep == "" {
		t.Fatalf("expected executable step")
	}
	if plan.CurrentStep == "" {
		t.Fatalf("expected current step")
	}
}

func TestBuildAutonomyPlanMarksApprovalAsBlocked(t *testing.T) {
	agenda := AutonomyAgenda{
		Focus: []AutonomyFocus{
			{
				Goal:             "Pedir decisión de Diego sobre deploy",
				SourceTopic:      "deploy",
				NextAction:       "ask_user",
				Reason:           "high-risk action requires approval",
				Priority:         4,
				RequiresApproval: true,
				RiskLevel:        "high",
			},
		},
	}

	plan := buildAutonomyPlan(agenda)
	if plan.PrimaryGoal == nil {
		t.Fatalf("expected primary goal")
	}
	if plan.PrimaryGoal.Status != "waiting_external" {
		t.Fatalf("expected waiting_external status, got %q", plan.PrimaryGoal.Status)
	}
	if plan.PrimaryGoal.BlockedBy != "approval_from_diego" {
		t.Fatalf("expected approval block, got %q", plan.PrimaryGoal.BlockedBy)
	}
}

func TestBuildAutonomyPlanSwitchesFocusToExecutableGoal(t *testing.T) {
	agenda := AutonomyAgenda{
		Focus: []AutonomyFocus{
			{
				Goal:             "Pedir decisión de Diego sobre deploy",
				SourceTopic:      "deploy",
				NextAction:       "ask_user",
				Reason:           "high-risk action requires approval",
				Priority:         5,
				RequiresApproval: true,
				RiskLevel:        "high",
			},
			{
				Goal:        "Construir conocimiento persistente sobre autonomia",
				SourceTopic: "autonomia",
				NextAction:  "learn",
				Reason:      "recurrent topic",
				Priority:    4,
			},
		},
	}

	plan := buildAutonomyPlan(agenda)
	if plan.PrimaryGoal == nil {
		t.Fatalf("expected primary goal")
	}
	if plan.PrimaryGoal.SourceTopic != "autonomia" {
		t.Fatalf("expected focus switch to executable goal, got %q", plan.PrimaryGoal.SourceTopic)
	}
	if plan.PrimaryGoalReason == "" {
		t.Fatalf("expected primary goal reason")
	}
	if len(plan.Supporting) != 1 || plan.Supporting[0].SourceTopic != "deploy" {
		t.Fatalf("expected blocked deploy as supporting goal")
	}
}

func TestBuildAutonomyPlanAddsDependencies(t *testing.T) {
	agenda := AutonomyAgenda{
		Focus: []AutonomyFocus{
			{
				Goal:        "Monitorear progreso de deploy",
				SourceTopic: "deploy",
				NextAction:  "monitor",
				Reason:      "work in progress",
				Priority:    3,
			},
		},
	}

	plan := buildAutonomyPlan(agenda)
	if plan.PrimaryGoal == nil {
		t.Fatalf("expected primary goal")
	}
	if len(plan.PrimaryGoal.DependsOn) == 0 {
		t.Fatalf("expected dependencies")
	}
}
