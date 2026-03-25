package reasoning

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/telemetry"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// AgentLoopInterface decouples the reasoning service from the full agent loop.
type AgentLoopInterface interface {
	ProcessHeartbeat(ctx context.Context, content, channel, chatID string) (string, error)
}

// TriageResult is the structured output from the local model triage.
type TriageResult struct {
	Score       int    `json:"score"`
	Observation string `json:"observation"`
	Action      string `json:"action"` // none|notify|investigate|execute
	Reason      string `json:"reason"`
}

// reasoningState tracks daily counters and last run time.
type reasoningState struct {
	LastRun            time.Time    `json:"last_run"`
	ObservationsToday  int          `json:"observations_today"`
	EscalationsToday   int          `json:"escalations_today"`
	LastResetDate      string       `json:"last_reset_date"`
	PendingEscalation  *TriageResult `json:"pending_escalation,omitempty"`
	PendingSnapshot    string        `json:"pending_snapshot,omitempty"`
}

// Service implements the background reasoning loop.
type Service struct {
	cfg         config.ReasoningConfig
	bus         *bus.MessageBus
	stateMgr    *state.Manager
	workspace   string
	local       providers.LLMProvider
	memoryTool  *tools.MemoryTool
	agentLoop   AgentLoopInterface
	tokenBudget *telemetry.TokenBudget
	onEvent     func(string) // callback for SSE visualization events
	runState    reasoningState
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewService creates a new background reasoning service.
func NewService(
	cfg config.ReasoningConfig,
	workspace string,
	stateMgr *state.Manager,
	local providers.LLMProvider,
	memoryTool *tools.MemoryTool,
	tokenBudget *telemetry.TokenBudget,
) *Service {
	if cfg.IntervalMinutes <= 0 {
		cfg.IntervalMinutes = 30
	}
	if cfg.EscalationThreshold <= 0 {
		cfg.EscalationThreshold = 7
	}
	if cfg.MaxObservationsPerDay <= 0 {
		cfg.MaxObservationsPerDay = 48
	}
	return &Service{
		cfg:         cfg,
		stateMgr:    stateMgr,
		workspace:   workspace,
		local:       local,
		memoryTool:  memoryTool,
		tokenBudget: tokenBudget,
	}
}

// SetBus sets the message bus for notifications.
func (s *Service) SetBus(b *bus.MessageBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = b
}

// SetAgentLoop sets the agent loop for cloud escalation (Phase 2).
func (s *Service) SetAgentLoop(al AgentLoopInterface) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentLoop = al
}

// SetEventCallback sets a function for emitting visualization events (SSE).
func (s *Service) SetEventCallback(fn func(string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onEvent = fn
}

func (s *Service) emitEvent(eventType string) {
	s.mu.Lock()
	fn := s.onEvent
	s.mu.Unlock()
	if fn != nil {
		fn(eventType)
	}
}

// Start begins the background reasoning loop.
func (s *Service) Start(ctx context.Context) {
	if !s.cfg.Enabled {
		logger.InfoC("reasoning", "Background reasoning service disabled")
		return
	}
	if s.local == nil {
		logger.WarnC("reasoning", "No local LLM provider available, reasoning service disabled")
		return
	}

	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	s.loadState()

	logger.InfoC("reasoning", "Background reasoning service started")

	interval := time.Duration(s.cfg.IntervalMinutes) * time.Minute

	// Run first cycle after a short warmup (give llama-server time to load)
	select {
	case <-s.ctx.Done():
		return
	case <-time.After(45 * time.Second):
	}
	s.runCycle()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.runCycle()
		}
	}
}

// Stop stops the reasoning service.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	logger.InfoC("reasoning", "Background reasoning service stopped")
}

// runCycle executes one reasoning cycle: gather state → local triage → optional cloud escalation.
func (s *Service) runCycle() {
	s.mu.Lock()
	// Reset daily counters at midnight
	today := time.Now().Format("2006-01-02")
	if today != s.runState.LastResetDate {
		s.runState.ObservationsToday = 0
		s.runState.EscalationsToday = 0
		s.runState.LastResetDate = today
	}

	if s.runState.ObservationsToday >= s.cfg.MaxObservationsPerDay {
		s.mu.Unlock()
		logger.DebugC("reasoning", "Daily observation limit reached, skipping cycle")
		return
	}
	s.mu.Unlock()

	// Retry pending escalation from previous cycle
	s.mu.Lock()
	pendingTriage := s.runState.PendingEscalation
	pendingSnapshot := s.runState.PendingSnapshot
	s.mu.Unlock()
	if pendingTriage != nil && pendingSnapshot != "" {
		logger.InfoC("reasoning", "Retrying pending escalation from previous cycle")
		s.escalateToCloud(pendingSnapshot, pendingTriage)
	}

	// Phase 1: gather state (pure Go, 0 tokens)
	snapshot := s.gatherState()
	if snapshot == "" {
		return
	}

	// Phase 2: local triage (60s — Qwen 1.5B on Pi 5 can be slow)
	s.emitEvent("reasoning")
	ctx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
	defer cancel()

	triage, err := s.triageLocal(ctx, snapshot)
	if err != nil {
		logger.WarnCF("reasoning", "Local triage failed", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Update counters
	s.mu.Lock()
	s.runState.ObservationsToday++
	s.runState.LastRun = time.Now()
	s.mu.Unlock()

	// Save observation to vault
	s.saveObservation(triage)

	logger.InfoCF("reasoning", "Triage complete", map[string]interface{}{
		"score":  triage.Score,
		"action": triage.Action,
	})

	// Phase 3: cloud escalation if score exceeds threshold
	if triage.Score >= s.cfg.EscalationThreshold {
		s.escalateToCloud(snapshot, triage)
	}

	s.saveState()
}

// gatherState collects system state without any LLM calls.
func (s *Service) gatherState() string {
	var parts []string

	// System metrics from sentinel
	sentinelPath := filepath.Join(s.workspace, "state", "sentinel.json")
	if data, err := os.ReadFile(sentinelPath); err == nil {
		var sentinel struct {
			CPUTempC       float64 `json:"cpu_temp_c"`
			RAMAvailableMB int     `json:"ram_available_mb"`
			RAMUsedPercent float64 `json:"ram_used_percent"`
			DiskFreeGB     float64 `json:"disk_free_gb"`
			DiskUsedPct    float64 `json:"disk_used_percent"`
		}
		if json.Unmarshal(data, &sentinel) == nil {
			parts = append(parts, fmt.Sprintf(
				"Sistema: CPU %.0f°C, RAM %.0f%% usada (%dMB libre), disco %.0f%% (%.0fGB libre)",
				sentinel.CPUTempC, sentinel.RAMUsedPercent, sentinel.RAMAvailableMB,
				sentinel.DiskUsedPct, sentinel.DiskFreeGB,
			))
		}
	}

	// Active concerns from attention service
	attentionPath := filepath.Join(s.workspace, "state", "attention.json")
	if data, err := os.ReadFile(attentionPath); err == nil {
		var attn struct {
			Concerns []struct {
				Summary  string `json:"summary"`
				Priority int    `json:"priority"`
				Resolved bool   `json:"resolved"`
			} `json:"concerns"`
		}
		if json.Unmarshal(data, &attn) == nil {
			var active []string
			for _, c := range attn.Concerns {
				if !c.Resolved {
					active = append(active, fmt.Sprintf("- [P%d] %s", c.Priority, c.Summary))
				}
			}
			if len(active) > 0 {
				if len(active) > 5 {
					active = active[:5]
				}
				parts = append(parts, "Preocupaciones activas:\n"+strings.Join(active, "\n"))
			}
		}
	}

	// Pending and overdue tasks
	tasksPath := filepath.Join(s.workspace, "tasks", "tasks.json")
	if data, err := os.ReadFile(tasksPath); err == nil {
		var tasks []struct {
			Title    string `json:"title"`
			Status   string `json:"status"`
			Priority string `json:"priority"`
			DueDate  string `json:"due_date"`
		}
		if json.Unmarshal(data, &tasks) == nil {
			today := time.Now().Format("2006-01-02")
			var pending, overdue []string
			for _, t := range tasks {
				if t.Status == "done" || t.Status == "cancelled" {
					continue
				}
				label := fmt.Sprintf("- [%s] %s", t.Priority, t.Title)
				if t.DueDate != "" && t.DueDate < today {
					overdue = append(overdue, label)
				} else {
					pending = append(pending, label)
				}
			}
			if len(overdue) > 0 {
				if len(overdue) > 5 {
					overdue = overdue[:5]
				}
				parts = append(parts, fmt.Sprintf("Tareas VENCIDAS (%d):\n%s", len(overdue), strings.Join(overdue, "\n")))
			}
			if len(pending) > 0 {
				if len(pending) > 5 {
					pending = pending[:5]
				}
				parts = append(parts, fmt.Sprintf("Tareas pendientes (%d):\n%s", len(pending), strings.Join(pending, "\n")))
			}
		}
	}

	// Last interaction time
	if lastChannel := s.stateMgr.GetLastChannel(); lastChannel != "" {
		lastUpdate := s.stateMgr.GetTimestamp()
		since := time.Since(lastUpdate)
		parts = append(parts, fmt.Sprintf("Última interacción: hace %s (canal: %s)", formatDuration(since), lastChannel))
	}

	// Time of day context
	now := time.Now()
	parts = append(parts, fmt.Sprintf("Hora actual: %s", now.Format("15:04")))

	// Recent vault notes (last 3 insights/observations)
	if s.memoryTool != nil {
		recentInsights := s.memoryTool.ListNotesByFolder("insights", 3)
		if len(recentInsights) > 0 {
			var notes []string
			for _, n := range recentInsights {
				preview := n.Content
				if len(preview) > 100 {
					preview = preview[:100] + "..."
				}
				notes = append(notes, fmt.Sprintf("- [%s] %s: %s", n.Updated, n.Key, preview))
			}
			parts = append(parts, "Insights recientes:\n"+strings.Join(notes, "\n"))
		}

		recentObs := s.memoryTool.ListNotesByFolder("observations", 3)
		if len(recentObs) > 0 {
			var notes []string
			for _, n := range recentObs {
				preview := n.Content
				if len(preview) > 80 {
					preview = preview[:80] + "..."
				}
				notes = append(notes, fmt.Sprintf("- %s", preview))
			}
			parts = append(parts, "Observaciones recientes:\n"+strings.Join(notes, "\n"))
		}

		// Pending action verifications
		recentActions := s.memoryTool.ListNotesByFolder("actions", 10)
		var pendingActions []string
		for _, n := range recentActions {
			isPending := false
			for _, tag := range n.Tags {
				if tag == "pending-verification" {
					isPending = true
					break
				}
			}
			if isPending {
				preview := n.Content
				if len(preview) > 100 {
					preview = preview[:100] + "..."
				}
				pendingActions = append(pendingActions, fmt.Sprintf("- [%s] %s", n.Key, preview))
			}
		}
		if len(pendingActions) > 0 {
			if len(pendingActions) > 3 {
				pendingActions = pendingActions[:3]
			}
			parts = append(parts, "Acciones pendientes de verificación:\n"+strings.Join(pendingActions, "\n"))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

const triagePrompt = `Sos el módulo de razonamiento de fondo de Chango, un agente AI autónomo en un Raspberry Pi 5. Tu dueño es Diego.
Analizá este snapshot del estado actual y calificá qué tan interesante o urgente es.

ESTADO:
%s

Guía de scores:
- 0-3: Nada notable, rutina
- 4-6: Levemente interesante, vale anotar
- 7-8: Patrón significativo o preocupación, escalar para análisis profundo
- 9-10: Urgente, necesita atención inmediata

Si hay tareas VENCIDAS, subí el score (+2 por 1 vencida, +4 por 3+ vencidas).
Si hay acciones pendientes de verificación hace >2h, subí el score (+2).

Ejemplos:
- Diego inactivo 2hs, nada pendiente, sistema estable → score 1
- RAM subió 15%% en los últimos checks → score 5
- Tarea vencida hace 3 días + Diego la mencionó hoy → score 8
- Disco al 95%% + deploy programado → score 9
- Patrón repetitivo detectado en observaciones anteriores → score 7
- 3 tareas vencidas sin atención → score 7
- Acción ejecutada hace 3h sin verificar → score 6
- Goal con sub-task completable ahora → score 7

Respondé SOLO con JSON válido, sin markdown ni explicaciones:
{"score": N, "observation": "qué notás", "action": "none|notify|investigate|execute", "reason": "por qué este score"}`

const escalationPrompt = `ESCALACIÓN DEL RAZONAMIENTO DE FONDO (score %d/10)

Observación del triage local: %s
Acción recomendada: %s
Razón: %s

Snapshot completo del estado:
%s

INSTRUCCIONES — Actuá según la acción recomendada:

Si action=notify:
→ Mandá un mensaje breve a Diego con lo relevante (tool: message).

Si action=investigate:
→ Investigá usando tus tools (memory search, web_search, tasks list, gmail, etc.)
→ Guardá lo que encontraste como insight: memory(action='save', key='insight-FECHA', folder='insights')

Si action=execute:
→ Ejecutá la acción necesaria: completar/actualizar tasks, guardar notas en memory, mandar email, etc.
→ Notificá a Diego qué hiciste (tool: message)
→ Registrá la acción: memory(action='save', key='action-FECHA-HORA', folder='actions', tags=['pending-verification'], content='Acción: [qué hiciste]. Resultado esperado: [qué debería pasar]. Verificar: [cómo confirmar].')

PLANNING AUTÓNOMO:
Si hay un goal con sub-tasks pendientes, evaluá cuál es el siguiente paso lógico:
1. Listá los sub-tasks del goal: tasks(action='list')
2. Identificá el siguiente sub-task accionable (status=pending, sin dependencias bloqueantes)
3. Si podés completarlo sin confirmación de Diego → hacelo ahora
4. Si requiere confirmación (email externo, gasto, cambio visible) → mandá a Diego el plan: "Quiero hacer X porque Y. ¿Procedo?"
5. Actualizá el status del task: tasks(action='update', id='...', status='in_progress' o 'done')

INVESTIGACIÓN PROACTIVA:
Si en el snapshot o en insights recientes detectás un tema que se mencionó 2+ veces pero no hay knowledge guardado:
→ Investigá brevemente: web_search + memory search
→ Guardá un resumen: memory(action='save', key='knowledge-TEMA', folder='insights', tags=['proactive-research'])
→ No notifiques a Diego por esto (es background learning silencioso)

VERIFICACIÓN DE ACCIONES PREVIAS:
Si hay "Acciones pendientes de verificación" en el snapshot:
→ Verificá cada una: ¿se logró el resultado esperado? (gmail list, tasks list, memory search)
→ Actualizá la nota: memory(action='save', key='action-...', folder='actions', tags=['verified'], content='Verificado: [resultado]')

LÍMITES DE SEGURIDAD:
- NO mandes emails a personas externas sin confirmación de Diego
- NO borres archivos, tasks, ni datos
- NO modifiques código ni hagas deploys
- SÍ podés: actualizar tasks, guardar en memory, mandar mensaje a Diego, investigar, leer emails/calendar

Siempre guardá un insight de lo que analizaste/hiciste.`

// triageLocal sends the state snapshot to the local model and parses the structured response.
func (s *Service) triageLocal(ctx context.Context, snapshot string) (*TriageResult, error) {
	prompt := fmt.Sprintf(triagePrompt, snapshot)

	msgs := []providers.Message{
		{Role: "user", Content: prompt},
	}

	resp, err := s.local.Chat(ctx, msgs, nil, "", map[string]interface{}{
		"max_tokens":  256,
		"temperature": 0.3,
	})
	if err != nil {
		return nil, fmt.Errorf("local LLM call failed: %w", err)
	}

	return parseTriageResponse(resp.Content)
}

// parseTriageResponse extracts a TriageResult from the model's response, with fallbacks.
func parseTriageResponse(raw string) (*TriageResult, error) {
	// Strip markdown code fences if present
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result TriageResult
	if err := json.Unmarshal([]byte(raw), &result); err == nil {
		// Clamp score to valid range
		if result.Score < 0 {
			result.Score = 0
		}
		if result.Score > 10 {
			result.Score = 10
		}
		return &result, nil
	}

	// Fallback: try to extract score with regex
	scoreRe := regexp.MustCompile(`"score"\s*:\s*(\d+)`)
	if m := scoreRe.FindStringSubmatch(raw); len(m) > 1 {
		score := 0
		fmt.Sscanf(m[1], "%d", &score)
		return &TriageResult{
			Score:       score,
			Observation: "Parse parcial — JSON malformado del modelo local",
			Action:      "none",
			Reason:      raw,
		}, nil
	}

	// Complete failure: return score 0 to avoid false escalation
	return &TriageResult{
		Score:       0,
		Observation: "No se pudo parsear la respuesta del modelo local",
		Action:      "none",
		Reason:      raw,
	}, nil
}

// saveObservation writes a triage result as an observation note in the vault.
func (s *Service) saveObservation(triage *TriageResult) {
	if s.memoryTool == nil {
		return
	}

	now := time.Now()
	key := fmt.Sprintf("reasoning-%s", now.Format("2006-01-02-1504"))
	content := fmt.Sprintf("Score: %d/10 | Action: %s\n%s\nReason: %s",
		triage.Score, triage.Action, triage.Observation, triage.Reason)

	tags := []string{"reasoning", "observation", fmt.Sprintf("score-%d", triage.Score)}
	if err := s.memoryTool.SaveNote(key, content, tags, "observations"); err != nil {
		logger.WarnCF("reasoning", "Failed to save observation", map[string]interface{}{
			"error": err.Error(),
		})
	}
}

// escalateToCloud sends the triage result + full context to the cloud LLM via ProcessHeartbeat.
// Retries once after 30s on failure; saves as pending if both attempts fail.
func (s *Service) escalateToCloud(snapshot string, triage *TriageResult) {
	s.mu.Lock()
	al := s.agentLoop
	budget := s.tokenBudget
	s.mu.Unlock()

	if al == nil {
		logger.WarnC("reasoning", "No agent loop available for cloud escalation")
		s.savePendingEscalation(snapshot, triage)
		return
	}

	if budget != nil && !budget.CanSpend(2000, true) {
		logger.InfoC("reasoning", "Token budget exceeded, saving escalation as pending")
		s.savePendingEscalation(snapshot, triage)
		return
	}

	lastChannel := s.stateMgr.GetLastChannel()
	channel, chatID := parseChannel(lastChannel)
	if channel == "" {
		channel, chatID = "telegram", "2111601777"
	}

	prompt := fmt.Sprintf(escalationPrompt,
		triage.Score, triage.Observation, triage.Action, triage.Reason, snapshot)

	// Attempt 1
	ctx1, cancel1 := context.WithTimeout(s.ctx, 60*time.Second)
	response, err := al.ProcessHeartbeat(ctx1, prompt, channel, chatID)
	cancel1()

	if err != nil {
		logger.WarnCF("reasoning", "Cloud escalation attempt 1 failed, retrying in 30s", map[string]interface{}{
			"error": err.Error(),
		})

		select {
		case <-s.ctx.Done():
			s.savePendingEscalation(snapshot, triage)
			return
		case <-time.After(30 * time.Second):
		}

		// Attempt 2
		ctx2, cancel2 := context.WithTimeout(s.ctx, 60*time.Second)
		response, err = al.ProcessHeartbeat(ctx2, prompt, channel, chatID)
		cancel2()

		if err != nil {
			logger.WarnCF("reasoning", "Cloud escalation attempt 2 failed, saving as pending", map[string]interface{}{
				"error": err.Error(),
			})
			s.savePendingEscalation(snapshot, triage)
			return
		}
	}

	// Success
	s.clearPendingEscalation()
	s.saveInsight(triage, response)

	s.mu.Lock()
	s.runState.EscalationsToday++
	s.mu.Unlock()

	logger.InfoCF("reasoning", "Cloud escalation complete", map[string]interface{}{
		"score":    triage.Score,
		"response": truncate(response, 100),
	})
}

func (s *Service) savePendingEscalation(snapshot string, triage *TriageResult) {
	s.mu.Lock()
	s.runState.PendingEscalation = triage
	s.runState.PendingSnapshot = snapshot
	s.mu.Unlock()
	s.saveState()
}

func (s *Service) clearPendingEscalation() {
	s.mu.Lock()
	s.runState.PendingEscalation = nil
	s.runState.PendingSnapshot = ""
	s.mu.Unlock()
}

// saveInsight writes a cloud escalation result as an insight note.
func (s *Service) saveInsight(triage *TriageResult, cloudResponse string) {
	if s.memoryTool == nil {
		return
	}

	now := time.Now()
	key := fmt.Sprintf("reasoning-insight-%s", now.Format("2006-01-02-1504"))
	content := fmt.Sprintf("## Triage Local\nScore: %d/10 — %s\n\n## Análisis Cloud\n%s",
		triage.Score, triage.Observation, cloudResponse)

	tags := []string{"reasoning", "insight", "escalated", fmt.Sprintf("score-%d", triage.Score)}
	if err := s.memoryTool.SaveNote(key, content, tags, "insights"); err != nil {
		logger.WarnCF("reasoning", "Failed to save insight", map[string]interface{}{
			"error": err.Error(),
		})
	}
}

// State persistence

func (s *Service) statePath() string {
	return filepath.Join(s.workspace, "state", "reasoning_state.json")
}

func (s *Service) loadState() {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	json.Unmarshal(data, &s.runState)
}

func (s *Service) saveState() {
	s.mu.Lock()
	data, err := json.MarshalIndent(s.runState, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return
	}
	// Atomic write
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, s.statePath())
}

// Helpers

func parseChannel(lastChannel string) (channel, chatID string) {
	parts := strings.SplitN(lastChannel, ":", 2)
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1]
	}
	return "", ""
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "menos de un minuto"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutos", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%d horas", hours)
	}
	return fmt.Sprintf("%dh %dmin", hours, mins)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
