package attention

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/state"
)

// Concern represents something that deserves attention without being asked.
type Concern struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`       // stale_project, overdue_task, pattern, opportunity, upcoming_event
	Priority  int       `json:"priority"`   // 1-5, 5 = urgent
	Summary   string    `json:"summary"`
	Context   string    `json:"context"`    // what triggered this concern
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
