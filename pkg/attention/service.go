package attention

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/state"
)

// Concern represents something that deserves attention without being asked.
type Concern struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`     // stale_project, overdue_task, pattern, opportunity, upcoming_event
	Priority  int       `json:"priority"` // 1-5, 5 = urgent
	Summary   string    `json:"summary"`
	Context   string    `json:"context"` // what triggered this concern
	CreatedAt time.Time `json:"created_at"`
	Resolved  bool      `json:"resolved"`
}

// attentionState is the JSON structure persisted to attention.json.
type attentionState struct {
	LastCheck time.Time `json:"last_check"`
	Concerns  []Concern `json:"concerns"`
}

// Service periodically scans agent state and generates concerns.
type Service struct {
	workspace string
	bus       *bus.MessageBus
	state     *state.Manager
	local     providers.LLMProvider
	concerns  []Concern
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewService creates a new attention manager service.
func NewService(workspace string, stateMgr *state.Manager) *Service {
	return &Service{
		workspace: workspace,
		state:     stateMgr,
	}
}

// SetLocalProvider sets the local LLM provider for zero-cost suggestions.
func (s *Service) SetLocalProvider(p providers.LLMProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.local = p
}

// SetBus sets the message bus for sending urgent alerts.
func (s *Service) SetBus(b *bus.MessageBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = b
}

// Start begins the attention check loop (every 2 hours).
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	logger.InfoC("attention", "Attention manager started (interval: 2h)")

	// Load existing concerns from disk
	s.loadConcerns()

	// Run first check after a short delay (let other services start)
	timer := time.NewTimer(30 * time.Second)
	select {
	case <-s.ctx.Done():
		timer.Stop()
		return
	case <-timer.C:
	}

	s.check()

	ticker := time.NewTicker(2 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.check()
		}
	}
}

// Stop stops the attention manager.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	logger.InfoC("attention", "Attention manager stopped")
}

// GetConcerns returns active (unresolved) concerns.
func (s *Service) GetConcerns() []Concern {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var active []Concern
	for _, c := range s.concerns {
		if !c.Resolved {
			active = append(active, c)
		}
	}
	return active
}

// check is the core scan logic.
func (s *Service) check() {
	logger.DebugCF("attention", "Running attention check", nil)

	var newConcerns []Concern

	// 1. Upcoming events in next 2 hours
	if events := s.checkUpcomingEvents(); len(events) > 0 {
		newConcerns = append(newConcerns, events...)
	}

	// 2. Stale projects (not updated in 5+ days)
	if stale := s.checkStaleProjects(); len(stale) > 0 {
		newConcerns = append(newConcerns, stale...)
	}

	// 3. Overdue tasks
	if overdue := s.checkOverdueTasks(); len(overdue) > 0 {
		newConcerns = append(newConcerns, overdue...)
	}

	// 4. Interaction patterns (high correction rate)
	if patterns := s.checkInteractionPatterns(); len(patterns) > 0 {
		newConcerns = append(newConcerns, patterns...)
	}

	// 5. Auto-deploy detection (pure Go, zero tokens)
	s.checkAutoDeploy()

	// 6. Git project tracker (stale repos in ~/Documents/)
	if gitStale := s.checkGitProjects(); len(gitStale) > 0 {
		newConcerns = append(newConcerns, gitStale...)
	}

	// Merge with existing: keep unresolved old ones that are still valid,
	// mark resolved if their type+context combo isn't in new set,
	// add genuinely new ones.
	s.mergeConcerns(newConcerns)

	// Persist
	s.saveConcerns()

	// Send proactive message for P5 urgent concerns
	s.alertUrgent()

	active := s.GetConcerns()
	logger.InfoCF("attention", "Attention check complete", map[string]interface{}{
		"active_concerns": len(active),
	})
}

// checkUpcomingEvents reads agenda.json for events in the next 2 hours.
func (s *Service) checkUpcomingEvents() []Concern {
	agendaPath := filepath.Join(s.workspace, "state", "agenda.json")
	data, err := os.ReadFile(agendaPath)
	if err != nil {
		return nil
	}

	var agenda struct {
		Events []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Start string `json:"start"`
		} `json:"events"`
	}
	if err := json.Unmarshal(data, &agenda); err != nil {
		logger.DebugCF("attention", "Failed to parse agenda.json", map[string]interface{}{"error": err.Error()})
		return nil
	}

	now := time.Now()
	twoHoursLater := now.Add(2 * time.Hour)
	var concerns []Concern

	for _, ev := range agenda.Events {
		t, err := parseFlexTime(ev.Start)
		if err != nil {
			continue
		}
		if t.After(now) && t.Before(twoHoursLater) {
			concerns = append(concerns, Concern{
				ID:        fmt.Sprintf("event-%s", ev.ID),
				Type:      "upcoming_event",
				Priority:  4,
				Summary:   fmt.Sprintf("Evento próximo: %s", ev.Title),
				Context:   fmt.Sprintf("Empieza a las %s", t.Format("15:04")),
				CreatedAt: now,
			})
		}
	}
	return concerns
}

// checkStaleProjects scans obsidian/projects/ for notes not updated in 5+ days.
func (s *Service) checkStaleProjects() []Concern {
	projectsDir := filepath.Join(s.workspace, "obsidian", "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}

	now := time.Now()
	threshold := 5 * 24 * time.Hour
	var concerns []Concern

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		age := now.Sub(info.ModTime())
		if age > threshold {
			name := strings.TrimSuffix(entry.Name(), ".md")
			name = strings.TrimPrefix(name, "project-")
			days := int(age.Hours() / 24)

			concerns = append(concerns, Concern{
				ID:        fmt.Sprintf("stale-%s", entry.Name()),
				Type:      "stale_project",
				Priority:  2,
				Summary:   fmt.Sprintf("Proyecto '%s' sin actualizar hace %d días", name, days),
				Context:   fmt.Sprintf("Última modificación: %s", info.ModTime().Format("2006-01-02")),
				CreatedAt: now,
			})
		}
	}
	return concerns
}

// checkOverdueTasks reads tasks.json for overdue items.
func (s *Service) checkOverdueTasks() []Concern {
	tasksPath := filepath.Join(s.workspace, "state", "tasks.json")
	data, err := os.ReadFile(tasksPath)
	if err != nil {
		return nil
	}

	var tasks []struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		DueDate string `json:"due_date"`
		Done    bool   `json:"done"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(data, &tasks); err != nil {
		// Try wrapped format
		var wrapped struct {
			Tasks json.RawMessage `json:"tasks"`
		}
		if err2 := json.Unmarshal(data, &wrapped); err2 != nil {
			return nil
		}
		if err2 := json.Unmarshal(wrapped.Tasks, &tasks); err2 != nil {
			return nil
		}
	}

	now := time.Now()
	var concerns []Concern

	for _, task := range tasks {
		if task.Done || task.Status == "done" || task.Status == "completed" {
			continue
		}
		if task.DueDate == "" {
			continue
		}
		due, err := parseFlexTime(task.DueDate)
		if err != nil {
			continue
		}
		if due.Before(now) {
			overdueDays := int(now.Sub(due).Hours() / 24)
			priority := 3
			if overdueDays > 3 {
				priority = 4
			}
			if overdueDays > 7 {
				priority = 5
			}

			concerns = append(concerns, Concern{
				ID:        fmt.Sprintf("overdue-%s", task.ID),
				Type:      "overdue_task",
				Priority:  priority,
				Summary:   fmt.Sprintf("Tarea vencida: %s (%d días)", task.Title, overdueDays),
				Context:   fmt.Sprintf("Vencimiento: %s", due.Format("2006-01-02")),
				CreatedAt: now,
			})
		}
	}
	return concerns
}

// checkInteractionPatterns reads interaction_scores.json for correction rate.
func (s *Service) checkInteractionPatterns() []Concern {
	scoresPath := filepath.Join(s.workspace, "state", "interaction_scores.json")
	data, err := os.ReadFile(scoresPath)
	if err != nil {
		return nil
	}

	var scores struct {
		TotalInteractions int     `json:"total_interactions"`
		Corrections       int     `json:"corrections"`
		CorrectionRate    float64 `json:"correction_rate"`
	}
	if err := json.Unmarshal(data, &scores); err != nil {
		return nil
	}

	if scores.TotalInteractions < 10 {
		// Not enough data
		return nil
	}

	rate := scores.CorrectionRate
	if rate == 0 && scores.TotalInteractions > 0 {
		rate = float64(scores.Corrections) / float64(scores.TotalInteractions) * 100
	}

	if rate > 30 {
		return []Concern{{
			ID:        "pattern-correction-rate",
			Type:      "pattern",
			Priority:  3,
			Summary:   fmt.Sprintf("Tasa de corrección alta: %.0f%%", rate),
			Context:   fmt.Sprintf("%d correcciones en %d interacciones", scores.Corrections, scores.TotalInteractions),
			CreatedAt: time.Now(),
		}}
	}

	return nil
}

// mergeConcerns integrates new concerns with existing ones.
func (s *Service) mergeConcerns(newConcerns []Concern) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Build lookup of new concern IDs
	newByID := make(map[string]Concern, len(newConcerns))
	for _, c := range newConcerns {
		newByID[c.ID] = c
	}

	// Keep existing unresolved concerns that are still in new set, mark others resolved
	for i, existing := range s.concerns {
		if existing.Resolved {
			continue
		}
		if _, stillActive := newByID[existing.ID]; !stillActive {
			s.concerns[i].Resolved = true
		} else {
			// Update priority/summary from fresh scan
			s.concerns[i].Priority = newByID[existing.ID].Priority
			s.concerns[i].Summary = newByID[existing.ID].Summary
			s.concerns[i].Context = newByID[existing.ID].Context
			delete(newByID, existing.ID)
		}
	}

	// Add genuinely new concerns
	for _, c := range newByID {
		s.concerns = append(s.concerns, c)
	}

	// Prune old resolved concerns (keep last 50 max)
	if len(s.concerns) > 50 {
		// Keep unresolved + most recent resolved
		var unresolved, resolved []Concern
		for _, c := range s.concerns {
			if c.Resolved {
				resolved = append(resolved, c)
			} else {
				unresolved = append(unresolved, c)
			}
		}
		keep := 50 - len(unresolved)
		if keep < 0 {
			keep = 0
		}
		if len(resolved) > keep {
			resolved = resolved[len(resolved)-keep:]
		}
		s.concerns = append(unresolved, resolved...)
	}
}

// saveConcerns persists concerns to workspace/state/attention.json.
func (s *Service) saveConcerns() {
	s.mu.RLock()
	st := attentionState{
		LastCheck: time.Now(),
		Concerns:  s.concerns,
	}
	s.mu.RUnlock()

	stateDir := filepath.Join(s.workspace, "state")
	os.MkdirAll(stateDir, 0755)

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		logger.ErrorCF("attention", "Failed to marshal concerns", map[string]interface{}{"error": err.Error()})
		return
	}

	filePath := filepath.Join(stateDir, "attention.json")
	tmpPath := filePath + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		logger.ErrorCF("attention", "Failed to write attention state", map[string]interface{}{"error": err.Error()})
		return
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		logger.ErrorCF("attention", "Failed to rename attention state", map[string]interface{}{"error": err.Error()})
	}
}

// loadConcerns reads existing concerns from disk.
func (s *Service) loadConcerns() {
	filePath := filepath.Join(s.workspace, "state", "attention.json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}

	var st attentionState
	if err := json.Unmarshal(data, &st); err != nil {
		logger.ErrorCF("attention", "Failed to parse attention.json", map[string]interface{}{"error": err.Error()})
		return
	}

	s.mu.Lock()
	s.concerns = st.Concerns
	s.mu.Unlock()
}

// alertUrgent sends a proactive message for P5 concerns via bus.
func (s *Service) alertUrgent() {
	s.mu.RLock()
	var urgent []Concern
	for _, c := range s.concerns {
		if !c.Resolved && c.Priority >= 5 {
			urgent = append(urgent, c)
		}
	}
	msgBus := s.bus
	s.mu.RUnlock()

	if len(urgent) == 0 || msgBus == nil {
		return
	}

	lastChannel := s.state.GetLastChannel()
	if lastChannel == "" {
		return
	}

	platform, userID := parseLastChannel(lastChannel)
	if platform == "" || userID == "" || constants.IsInternalChannel(platform) {
		return
	}

	var lines []string
	for _, c := range urgent {
		lines = append(lines, fmt.Sprintf("• %s", c.Summary))
	}

	msg := fmt.Sprintf("🚨 Atención urgente:\n%s", strings.Join(lines, "\n"))
	msgBus.PublishOutbound(bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: msg,
	})

	logger.InfoCF("attention", "Urgent concern alert sent", map[string]interface{}{
		"count": len(urgent),
		"to":    platform,
	})
}

// parseLastChannel splits "platform:userID" into parts.
func parseLastChannel(lastChannel string) (platform, userID string) {
	parts := strings.SplitN(lastChannel, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

func (s *Service) notifyAutoDeployFailure(stage, detail string) {
	s.mu.RLock()
	msgBus := s.bus
	s.mu.RUnlock()
	if msgBus == nil {
		return
	}

	lastChannel := s.state.GetLastChannel()
	if lastChannel == "" {
		return
	}
	platform, userID := parseLastChannel(lastChannel)
	if platform == "" || userID == "" || constants.IsInternalChannel(platform) {
		return
	}

	detail = strings.TrimSpace(detail)
	if len(detail) > 500 {
		detail = detail[:500] + "…"
	}

	content := fmt.Sprintf("⚠️ Auto-deploy falló en %s.", stage)
	if detail != "" {
		content += "\n" + detail
	}
	content += "\nVoy a necesitar intervención manual para corregirlo."

	msgBus.PublishOutbound(bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: content,
	})
}

// deployState tracks the last deploy time.
type deployState struct {
	LastDeploy time.Time `json:"last_deploy"`
}

// selfChangelog tracks self-tool modifications.
type selfChangelog struct {
	Entries []struct {
		File      string    `json:"file"`
		Timestamp time.Time `json:"timestamp"`
	} `json:"entries"`
}

// checkAutoDeploy detects when AGENTS.md was modified by the self tool
// and auto-deploys if build checks pass. Pure Go exec, zero tokens.
func (s *Service) checkAutoDeploy() {
	agentsPath := filepath.Join(s.workspace, "AGENTS.md")
	info, err := os.Stat(agentsPath)
	if err != nil {
		return
	}

	// Load last deploy time
	deployPath := filepath.Join(s.workspace, "state", "last_deploy.json")
	var deploy deployState
	if data, err := os.ReadFile(deployPath); err == nil {
		json.Unmarshal(data, &deploy)
	}

	// Check if AGENTS.md was modified after last deploy
	if !info.ModTime().After(deploy.LastDeploy) {
		return
	}

	// Check if modification was by the self tool
	changelogPath := filepath.Join(s.workspace, "state", "self_changelog.json")
	changelogData, err := os.ReadFile(changelogPath)
	if err != nil {
		return
	}

	var changelog selfChangelog
	if err := json.Unmarshal(changelogData, &changelog); err != nil {
		return
	}

	// Look for a self_changelog entry for AGENTS.md after last deploy
	selfModified := false
	for _, entry := range changelog.Entries {
		if strings.Contains(entry.File, "AGENTS.md") && entry.Timestamp.After(deploy.LastDeploy) {
			selfModified = true
			break
		}
	}

	if !selfModified {
		return
	}

	logger.InfoC("attention", "AGENTS.md modified by self tool, running build checks...")

	// Find the project root (parent of workspace)
	projectRoot := filepath.Dir(s.workspace)
	// The workspace is typically ~/.picoclaw/workspace, but the Go source is the picoclaw project
	// We need the source tree. Check if go.mod exists at common locations.
	sourceRoot := ""
	candidates := []string{
		"/home/diego/Documents/picoclaw",
		projectRoot,
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "go.mod")); err == nil {
			sourceRoot = c
			break
		}
	}
	if sourceRoot == "" {
		logger.DebugCF("attention", "Cannot find Go source root for auto-deploy", nil)
		return
	}

	// Run go build
	buildCmd := exec.CommandContext(s.ctx, "go", "build", "./...")
	buildCmd.Dir = sourceRoot
	if output, err := buildCmd.CombinedOutput(); err != nil {
		logger.ErrorCF("attention", "Auto-deploy build failed", map[string]interface{}{
			"error":  err.Error(),
			"output": string(output),
		})
		s.notifyAutoDeployFailure("go build", strings.TrimSpace(string(output)))
		return
	}

	// Run go vet
	vetCmd := exec.CommandContext(s.ctx, "go", "vet", "./...")
	vetCmd.Dir = sourceRoot
	if output, err := vetCmd.CombinedOutput(); err != nil {
		logger.ErrorCF("attention", "Auto-deploy vet failed", map[string]interface{}{
			"error":  err.Error(),
			"output": string(output),
		})
		s.notifyAutoDeployFailure("go vet", strings.TrimSpace(string(output)))
		return
	}

	logger.InfoC("attention", "Build checks passed, auto-committing and deploying...")

	// Auto-commit
	gitAddCmd := exec.CommandContext(s.ctx, "git", "add", "-A")
	gitAddCmd.Dir = sourceRoot
	if _, err := gitAddCmd.CombinedOutput(); err != nil {
		logger.ErrorCF("attention", "Auto-deploy git add failed", map[string]interface{}{"error": err.Error()})
		s.notifyAutoDeployFailure("git add", err.Error())
		return
	}

	commitCmd := exec.CommandContext(s.ctx, "git", "commit", "-m", "chore: auto-deploy AGENTS.md update (self-modified)")
	commitCmd.Dir = sourceRoot
	if output, err := commitCmd.CombinedOutput(); err != nil {
		// If nothing to commit, that's fine
		if !strings.Contains(string(output), "nothing to commit") {
			logger.ErrorCF("attention", "Auto-deploy commit failed", map[string]interface{}{
				"error":  err.Error(),
				"output": string(output),
			})
			s.notifyAutoDeployFailure("git commit", strings.TrimSpace(string(output)))
			return
		}
	}

	// Push to fork remote
	pushCmd := exec.CommandContext(s.ctx, "git", "push", "fork", "main")
	pushCmd.Dir = sourceRoot
	if output, err := pushCmd.CombinedOutput(); err != nil {
		logger.ErrorCF("attention", "Auto-deploy push failed", map[string]interface{}{
			"error":  err.Error(),
			"output": string(output),
		})
		s.notifyAutoDeployFailure("git push", strings.TrimSpace(string(output)))
		return
	}

	// Trigger Coolify deploy via script (if available)
	deployScript := filepath.Join(sourceRoot, "scripts", "deploy.sh")
	if _, err := os.Stat(deployScript); err == nil {
		deployExec := exec.CommandContext(s.ctx, "bash", deployScript)
		deployExec.Dir = sourceRoot
		if output, err := deployExec.CombinedOutput(); err != nil {
			logger.ErrorCF("attention", "Auto-deploy Coolify trigger failed", map[string]interface{}{
				"error":  err.Error(),
				"output": string(output),
			})
			s.notifyAutoDeployFailure("deploy trigger", strings.TrimSpace(string(output)))
		} else {
			logger.InfoC("attention", "Coolify deploy triggered successfully")
		}
	}

	// Save deploy time
	deploy.LastDeploy = time.Now()
	if data, err := json.MarshalIndent(deploy, "", "  "); err == nil {
		stateDir := filepath.Join(s.workspace, "state")
		os.MkdirAll(stateDir, 0755)
		tmpPath := deployPath + ".tmp"
		if err := os.WriteFile(tmpPath, data, 0644); err == nil {
			os.Rename(tmpPath, deployPath)
		}
	}

	logger.InfoC("attention", "Auto-deploy complete")
}

// checkGitProjects scans ~/Documents/ for git repos with stale commits (>7 days).
func (s *Service) checkGitProjects() []Concern {
	documentsDir := "/home/diego/Documents"
	entries, err := os.ReadDir(documentsDir)
	if err != nil {
		return nil
	}

	now := time.Now()
	threshold := 7 * 24 * time.Hour
	var concerns []Concern

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		gitDir := filepath.Join(documentsDir, entry.Name(), ".git")
		if _, err := os.Stat(gitDir); err != nil {
			continue
		}

		// Get last commit date
		projectPath := filepath.Join(documentsDir, entry.Name())
		cmd := exec.CommandContext(s.ctx, "git", "-C", projectPath, "log", "--oneline", "-1", "--format=%ci")
		output, err := cmd.Output()
		if err != nil {
			continue
		}

		dateStr := strings.TrimSpace(string(output))
		if dateStr == "" {
			continue
		}

		commitTime, err := time.Parse("2006-01-02 15:04:05 -0700", dateStr)
		if err != nil {
			continue
		}

		age := now.Sub(commitTime)
		if age <= threshold {
			continue
		}

		days := int(age.Hours() / 24)
		summary := fmt.Sprintf("Proyecto '%s' sin commits hace %d dias", entry.Name(), days)

		// Use Qwen local for a suggestion (~30 tokens, zero cloud cost)
		s.mu.RLock()
		local := s.local
		s.mu.RUnlock()

		suggestion := ""
		if local != nil {
			prompt := fmt.Sprintf("Project %s inactive %d days. Suggest next step in <10 words.", entry.Name(), days)
			msgs := []providers.Message{
				{Role: "user", Content: prompt},
			}
			resp, err := local.Chat(s.ctx, msgs, nil, local.GetDefaultModel(), map[string]interface{}{
				"max_tokens":  30,
				"temperature": 0.3,
			})
			if err == nil && resp.Content != "" {
				suggestion = strings.TrimSpace(resp.Content)
			}
		}

		contextStr := fmt.Sprintf("Ultimo commit: %s", commitTime.Format("2006-01-02"))
		if suggestion != "" {
			contextStr += " | " + suggestion
		}

		concerns = append(concerns, Concern{
			ID:        fmt.Sprintf("stale-git-%s", entry.Name()),
			Type:      "stale_project",
			Priority:  2,
			Summary:   summary,
			Context:   contextStr,
			CreatedAt: now,
		})
	}

	return concerns
}

// parseFlexTime tries multiple time formats.
func parseFlexTime(s string) (time.Time, error) {
	loc, _ := time.LoadLocation("America/Argentina/Buenos_Aires")
	if loc == nil {
		loc = time.FixedZone("ART", -3*3600)
	}

	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}

	for _, f := range formats {
		if t, err := time.ParseInLocation(f, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time: %s", s)
}
