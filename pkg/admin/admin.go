package admin

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/logger"
	statepkg "github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
)

//go:embed static/index.html
var staticFS embed.FS

//go:embed static/home.html
var homeFS embed.FS

//go:embed static/about.html
var aboutFS embed.FS

// Editable files whitelist (relative to workspace)
var allowedFiles = []string{
	"SOUL.md",
	"AGENTS.md",
	"IDENTITY.md",
	"USER.md",
	"HEARTBEAT.md",
	"council/cfo.md",
	"council/cto.md",
	"council/chango.md",
}

type Handler struct {
	workspacePath  string
	token          string
	tokenCreatedAt time.Time
	configPath     string
	config         *config.Config
	cronService    *cron.CronService
	lightsTool     *tools.LightsTool
	reloadFn       func() error
	version        string
	eventBus       *EventBus
}

// EventBus broadcasts real-time agent activity events to SSE clients.
type EventBus struct {
	clients map[chan AdminEvent]bool
	recent  []AdminEvent
	counts  map[string]int
	mu      sync.RWMutex
}

func newEventBus() *EventBus {
	return &EventBus{
		clients: make(map[chan AdminEvent]bool),
		recent:  make([]AdminEvent, 0, 256),
		counts:  make(map[string]int),
	}
}

type AdminEvent struct {
	Type string `json:"type"`
	At   string `json:"at"`
}

// Emit sends an event to all connected SSE clients.
func (eb *EventBus) Emit(eventType string) {
	evt := AdminEvent{
		Type: eventType,
		At:   time.Now().UTC().Format(time.RFC3339Nano),
	}

	eb.mu.Lock()
	eb.counts[eventType]++
	eb.recent = append(eb.recent, evt)
	if len(eb.recent) > 200 {
		eb.recent = eb.recent[len(eb.recent)-200:]
	}
	clients := make([]chan AdminEvent, 0, len(eb.clients))
	for ch := range eb.clients {
		clients = append(clients, ch)
	}
	eb.mu.Unlock()

	for _, ch := range clients {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (eb *EventBus) subscribe() chan AdminEvent {
	ch := make(chan AdminEvent, 32)
	eb.mu.Lock()
	eb.clients[ch] = true
	eb.mu.Unlock()
	return ch
}

func (eb *EventBus) unsubscribe(ch chan AdminEvent) {
	eb.mu.Lock()
	delete(eb.clients, ch)
	eb.mu.Unlock()
	close(ch)
}

func (eb *EventBus) snapshot() (recent []AdminEvent, counts map[string]int) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	recent = make([]AdminEvent, len(eb.recent))
	copy(recent, eb.recent)
	counts = make(map[string]int, len(eb.counts))
	for k, v := range eb.counts {
		counts[k] = v
	}
	return recent, counts
}

func New(workspacePath, token, configPath string, cfg *config.Config) *Handler {
	return &Handler{
		workspacePath:  workspacePath,
		token:          token,
		tokenCreatedAt: time.Now(),
		configPath:     configPath,
		config:         cfg,
		eventBus:       newEventBus(),
	}
}

// EmitEvent exposes the event bus for the agent loop to emit activity events.
func (h *Handler) EmitEvent(eventType string) {
	if h.eventBus != nil {
		h.eventBus.Emit(eventType)
	}
}

func (h *Handler) SetCronService(cs *cron.CronService) { h.cronService = cs }
func (h *Handler) SetLightsTool(lt *tools.LightsTool)  { h.lightsTool = lt }
func (h *Handler) SetReloadFn(fn func() error)         { h.reloadFn = fn }
func (h *Handler) SetVersion(v string)                 { h.version = v }

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/", h.serveHome)
	mux.HandleFunc("/about", h.serveAbout)
	mux.HandleFunc("/admin", h.serveSPA)
	mux.HandleFunc("/api/public/status", h.publicStatus)
	mux.HandleFunc("/api/public/brain", h.publicBrain)
	mux.HandleFunc("/api/public/events", h.sseEvents)
	mux.HandleFunc("/api/runtime", h.withAuth(h.runtimeDashboard))
	mux.HandleFunc("/api/health", h.withAuth(h.systemHealth))
	mux.HandleFunc("/api/swap/free", h.withAuth(h.freeSwap))
	mux.HandleFunc("/api/wifi/scan", h.withAuth(h.wifiScan))
	mux.HandleFunc("/api/bluetooth/scan", h.withAuth(h.bluetoothScan))
	mux.HandleFunc("/api/bluetooth/connect", h.withAuth(h.bluetoothConnect))
	mux.HandleFunc("/api/bluetooth/disconnect", h.withAuth(h.bluetoothDisconnect))
	mux.HandleFunc("/api/bluetooth/remove", h.withAuth(h.bluetoothRemove))
	mux.HandleFunc("/api/files", h.withAuth(h.listFiles))
	mux.HandleFunc("/api/files/", h.withAuth(h.handleFile))
	mux.HandleFunc("/api/vault", h.withAuth(h.vaultOverview))
	mux.HandleFunc("/api/vault/notes", h.withAuth(h.vaultNotes))
	mux.HandleFunc("/api/vault/note/", h.withAuth(h.vaultNote))
	// Settings endpoints
	mux.HandleFunc("/api/settings/providers", h.withAuth(h.settingsProviders))
	mux.HandleFunc("/api/settings/channels", h.withAuth(h.settingsChannels))
	mux.HandleFunc("/api/settings/tools", h.withAuth(h.settingsTools))
	mux.HandleFunc("/api/settings/services", h.withAuth(h.settingsServices))
	mux.HandleFunc("/api/settings/agent", h.withAuth(h.settingsAgent))
	mux.HandleFunc("/api/settings/upload/google-sa", h.withAuth(h.uploadGoogleSA))
	mux.HandleFunc("/api/settings/test/provider", h.withAuth(h.testProvider))
	mux.HandleFunc("/api/settings/briefing", h.withAuth(h.settingsBriefing))
	// Agent reload
	mux.HandleFunc("/api/agent/reload", h.withAuth(h.agentReload))
	// Cron CRUD
	mux.HandleFunc("/api/cron/jobs", h.withAuth(h.cronJobs))
	mux.HandleFunc("/api/cron/jobs/", h.withAuth(h.cronJob))
	// Logs
	mux.HandleFunc("/api/logs", h.withAuth(h.getLogs))
	// Devices / Lights
	mux.HandleFunc("/api/devices/lights", h.withAuth(h.lightsDevices))
	mux.HandleFunc("/api/devices/lights/discover", h.withAuth(h.lightsDiscover))
	mux.HandleFunc("/api/devices/lights/save", h.withAuth(h.lightsSave))
	mux.HandleFunc("/api/devices/lights/control", h.withAuth(h.lightsControl))
	mux.HandleFunc("/api/devices/lights/", h.withAuth(h.lightsDevice))
	// Updates
	mux.HandleFunc("/api/updates/check", h.withAuth(h.updatesCheck))
	mux.HandleFunc("/api/updates/apply", h.withAuth(h.updatesApply))
	// Onboarding
	mux.HandleFunc("/api/onboarding/status", h.withAuth(h.onboardingStatus))
	mux.HandleFunc("/api/onboarding/profile", h.withAuth(h.onboardingProfile))
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.token != "" {
			auth := r.Header.Get("Authorization")
			expected := "Bearer " + h.token
			if !strings.EqualFold(auth, expected) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			// Nudge: warn if token has been active for over 24 hours
			if time.Since(h.tokenCreatedAt) > 24*time.Hour {
				logger.WarnCF("admin", "Admin token is over 24h old, consider rotating", nil)
			}
		}

		// CSRF protection for state-changing methods: verify Origin matches request host
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			origin := r.Header.Get("Origin")
			if origin == "" {
				origin = r.Header.Get("Referer")
			}
			if origin != "" {
				expectedHost := r.Host
				if !strings.Contains(origin, expectedHost) {
					logger.WarnCF("admin", "CSRF check failed: origin mismatch", map[string]interface{}{
						"origin": origin,
						"host":   expectedHost,
					})
					http.Error(w, "Forbidden: origin mismatch", http.StatusForbidden)
					return
				}
			}
		}

		next(w, r)
	}
}

func (h *Handler) serveHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := homeFS.ReadFile("static/home.html")
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (h *Handler) serveAbout(w http.ResponseWriter, r *http.Request) {
	data, err := aboutFS.ReadFile("static/about.html")
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (h *Handler) serveSPA(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (h *Handler) publicStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	result := map[string]interface{}{
		"status":  "ok",
		"version": h.version,
	}

	if runtime, err := h.buildRuntimeDashboard(); err == nil {
		result["runtime"] = runtime
		if degraded, ok := runtime["degraded"].(bool); ok && degraded {
			result["status"] = "degraded"
		}
	}

	// Uptime from sentinel
	sentinelData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "sentinel.json"))
	if err == nil {
		var s map[string]interface{}
		if json.Unmarshal(sentinelData, &s) == nil {
			if up, ok := s["uptime_seconds"].(float64); ok {
				hrs := int(up) / 3600
				mins := (int(up) % 3600) / 60
				if hrs > 24 {
					d := hrs / 24
					result["uptime_short"] = fmt.Sprintf("%dd", d)
					result["uptime"] = fmt.Sprintf("%dd %dh", d, hrs%24)
				} else if hrs > 0 {
					result["uptime_short"] = fmt.Sprintf("%dh", hrs)
					result["uptime"] = fmt.Sprintf("%dh %dm", hrs, mins)
				} else {
					result["uptime_short"] = fmt.Sprintf("%dm", mins)
					result["uptime"] = fmt.Sprintf("%dm", mins)
				}
			}
		}
	}

	// Model: read from disk config (in-memory config may be mutated by auto-recovery)
	if h.configPath != "" {
		if diskCfg, err := config.LoadConfig(h.configPath); err == nil {
			result["model"] = diskCfg.Agents.Defaults.Model
			result["provider"] = diskCfg.Agents.Defaults.Provider
		}
	} else if h.config != nil && h.config.Agents.Defaults.Model != "" {
		result["model"] = h.config.Agents.Defaults.Model
	}

	// Count vault notes
	vaultDir := filepath.Join(h.workspacePath, "obsidian")
	folders := []string{"daily", "people", "preferences", "insights", "decisions", "projects", "blog", "state", "inbox"}
	totalNotes := 0
	for _, f := range folders {
		dir := filepath.Join(vaultDir, f)
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					totalNotes++
				}
			}
		}
	}
	result["notes"] = totalNotes

	// Cron jobs count
	if h.cronService != nil {
		jobsFile := filepath.Join(h.workspacePath, "cron", "jobs.json")
		if data, err := os.ReadFile(jobsFile); err == nil {
			var jobs []map[string]interface{}
			if json.Unmarshal(data, &jobs) == nil {
				count := 0
				for _, j := range jobs {
					if enabled, ok := j["enabled"].(bool); ok && enabled {
						count++
					}
				}
				result["cron_jobs"] = count
			}
		}
	}

	// --- Last 24h activity stats ---

	// Interaction count from interaction_scores.json
	scoresData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "interaction_scores.json"))
	if err == nil {
		var scores []map[string]interface{}
		if json.Unmarshal(scoresData, &scores) == nil {
			cutoff := time.Now().UTC().Add(-24 * time.Hour)
			count24h := 0
			var lastActivity string
			for _, s := range scores {
				if ts, ok := s["timestamp"].(string); ok {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						if t.After(cutoff) {
							count24h++
						}
						if ts > lastActivity {
							lastActivity = ts
						}
					}
				}
			}
			result["interactions_24h"] = count24h
			if lastActivity != "" {
				result["last_activity"] = lastActivity
			}
		}
	}

	// Telemetry: today's tokens
	telemetryData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "telemetry.json"))
	if err == nil {
		var telemetry map[string]interface{}
		if json.Unmarshal(telemetryData, &telemetry) == nil {
			today := time.Now().Format("2006-01-02")
			if days, ok := telemetry["days"].([]interface{}); ok {
				for _, d := range days {
					if dm, ok := d.(map[string]interface{}); ok {
						if dm["date"] == today {
							if totals, ok := dm["totals"].(map[string]interface{}); ok {
								if tt, ok := totals["total_tokens"].(float64); ok {
									result["tokens_today"] = int64(tt)
								}
								if calls, ok := totals["calls"].(float64); ok {
									result["calls_today"] = int(calls)
								}
							}
							break
						}
					}
				}
			}
		}
	}

	// Reasoning state: observations/escalations today
	reasoningData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "reasoning_state.json"))
	if err == nil {
		var rs map[string]interface{}
		if json.Unmarshal(reasoningData, &rs) == nil {
			if obs, ok := rs["observations_today"].(float64); ok {
				result["observations_today"] = int(obs)
			}
			if esc, ok := rs["escalations_today"].(float64); ok {
				result["escalations_today"] = int(esc)
			}
		}
	}

	// Recent event counters (from in-process event bus)
	if h.eventBus != nil {
		recent, counts := h.eventBus.snapshot()
		result["events_total"] = counts
		result["events_recent_count"] = len(recent)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(result)
}

// publicBrain returns a richer runtime snapshot for the realtime home dashboard.
func (h *Handler) publicBrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := map[string]interface{}{}
	if data, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "sentinel.json")); err == nil {
		var s map[string]interface{}
		if json.Unmarshal(data, &s) == nil {
			status["cpu_temp_c"] = s["cpu_temp_c"]
			status["ram_used_percent"] = s["ram_used_percent"]
			status["disk_used_percent"] = s["disk_used_percent"]
			if alerts, ok := s["alerts"].([]interface{}); ok {
				status["alerts_count"] = len(alerts)
			}
			status["uptime_seconds"] = s["uptime_seconds"]
		}
	}

	swapTotalMB, swapFreeMB, swapUsedPercent := readSwap()
	_ = swapTotalMB
	_ = swapFreeMB
	status["swap_used_percent"] = swapUsedPercent

	resp := map[string]interface{}{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"version":      h.version,
		"status":       status,
		"services": map[string]bool{
			"heartbeat": h.config.Heartbeat.Enabled,
			"sentinel":  h.config.Sentinel.Enabled,
			"reasoning": h.config.Reasoning.Enabled,
			"rss":       h.config.RSS.Enabled,
			"health":    h.config.Health.Enabled,
		},
	}

	// Provider/model from current disk config.
	if h.configPath != "" {
		if diskCfg, err := config.LoadConfig(h.configPath); err == nil {
			resp["provider"] = diskCfg.Agents.Defaults.Provider
			resp["model"] = diskCfg.Agents.Defaults.Model
		}
	}

	// Public summary reused by home cards.
	if ps, err := h.buildPublicStatus(); err == nil {
		resp["summary"] = ps
	}
	if runtime, err := h.buildRuntimeDashboard(); err == nil {
		resp["runtime"] = runtime
	}

	// Event trace and counters.
	if h.eventBus != nil {
		recent, counts := h.eventBus.snapshot()
		// latest first in trace
		for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
			recent[i], recent[j] = recent[j], recent[i]
		}
		if len(recent) > 50 {
			recent = recent[:50]
		}
		resp["events"] = map[string]interface{}{
			"counts": counts,
			"recent": recent,
		}

		type kv struct {
			K string
			V int
		}
		top := make([]kv, 0, len(counts))
		for k, v := range counts {
			top = append(top, kv{K: k, V: v})
		}
		sort.Slice(top, func(i, j int) bool { return top[i].V > top[j].V })
		if len(top) > 5 {
			top = top[:5]
		}
		topMap := make([]map[string]interface{}, 0, len(top))
		for _, item := range top {
			topMap = append(topMap, map[string]interface{}{"type": item.K, "count": item.V})
		}
		resp["top_events"] = topMap
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) runtimeDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	runtime, err := h.buildRuntimeDashboard()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "runtime dashboard unavailable: "+err.Error())
		return
	}
	jsonOK(w, runtime)
}

func (h *Handler) buildRuntimeDashboard() (map[string]interface{}, error) {
	result := map[string]interface{}{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"degraded":     false,
	}

	if stateData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "state.json")); err == nil {
		var st statepkg.State
		if json.Unmarshal(stateData, &st) == nil {
			activeTasks := 0
			blockedTasks := 0
			overdueTasks := 0
			blockedDetails := make([]map[string]interface{}, 0, 5)
			overdueDetails := make([]map[string]interface{}, 0, 5)
			today := time.Now().Format("2006-01-02")
			for _, task := range st.Tasks {
				switch task.Status {
				case "done", "cancelled":
					continue
				default:
					activeTasks++
				}
				if task.Status == "blocked" || task.Status == "waiting_external" || task.Status == "verification_failed" {
					blockedTasks++
					if len(blockedDetails) < 5 {
						blockedDetails = append(blockedDetails, map[string]interface{}{
							"id":       task.ID,
							"title":    task.Title,
							"status":   task.Status,
							"priority": task.Priority,
						})
					}
				}
				if task.DueDate != "" && task.DueDate < today {
					overdueTasks++
					if len(overdueDetails) < 5 {
						overdueDetails = append(overdueDetails, map[string]interface{}{
							"id":       task.ID,
							"title":    task.Title,
							"due_date": task.DueDate,
							"status":   task.Status,
						})
					}
				}
			}

			pendingReminders := 0
			for _, reminder := range st.Reminders {
				if !reminder.Fired {
					pendingReminders++
				}
			}

			result["backlog"] = map[string]interface{}{
				"active_tasks":      activeTasks,
				"blocked_tasks":     blockedTasks,
				"overdue_tasks":     overdueTasks,
				"pending_reminders": pendingReminders,
			}
			result["backlog_details"] = map[string]interface{}{
				"blocked": blockedDetails,
				"overdue": overdueDetails,
			}

			followUps := make([]map[string]interface{}, 0, 5)
			overdueFollowUps := 0
			now := time.Now().UTC()
			for _, followUp := range st.FollowUps {
				if followUp.Status == "done" || followUp.Status == "cancelled" {
					continue
				}
				if followUp.NextCheckAt != "" {
					if dueAt, err := time.Parse(time.RFC3339, followUp.NextCheckAt); err == nil && dueAt.Before(now) {
						overdueFollowUps++
					}
				}
				if len(followUps) < 5 {
					followUps = append(followUps, map[string]interface{}{
						"id":            followUp.ID,
						"title":         followUp.Title,
						"status":        followUp.Status,
						"next_check_at": followUp.NextCheckAt,
						"attempt_count": followUp.AttemptCount,
						"last_outcome":  followUp.LastOutcome,
						"reference_id":  followUp.ReferenceID,
					})
				}
			}
			result["follow_ups"] = map[string]interface{}{
				"count":          len(st.FollowUps),
				"overdue_count":  overdueFollowUps,
				"next_scheduled": followUps,
			}

			if len(st.AutonomyLog) > 0 {
				start := len(st.AutonomyLog) - 8
				if start < 0 {
					start = 0
				}
				result["autonomy"] = map[string]interface{}{
					"recent_actions": st.AutonomyLog[start:],
				}
			}
			if blockedTasks > 0 || overdueTasks > 0 {
				result["degraded"] = true
			}
			if overdueFollowUps > 0 {
				result["degraded"] = true
			}
		}
	}

	if telemetryData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "telemetry.json")); err == nil {
		var telemetry struct {
			Days []struct {
				Date      string                            `json:"date"`
				Providers map[string]map[string]interface{} `json:"providers"`
				Totals    map[string]interface{}            `json:"totals"`
			} `json:"days"`
		}
		if json.Unmarshal(telemetryData, &telemetry) == nil {
			today := time.Now().Format("2006-01-02")
			for _, day := range telemetry.Days {
				if day.Date != today {
					continue
				}
				result["provider_routes"] = day.Providers
				result["tokens_today"] = day.Totals
				for _, provider := range day.Providers {
					if streak, ok := provider["failure_streak"].(float64); ok && streak > 0 {
						result["degraded"] = true
					}
				}
				break
			}
		}
	}

	if sentinelData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "sentinel.json")); err == nil {
		var sentinel map[string]interface{}
		if json.Unmarshal(sentinelData, &sentinel) == nil {
			result["system"] = map[string]interface{}{
				"cpu_temp_c":        sentinel["cpu_temp_c"],
				"ram_used_percent":  sentinel["ram_used_percent"],
				"disk_used_percent": sentinel["disk_used_percent"],
				"uptime_seconds":    sentinel["uptime_seconds"],
			}
			if alerts, ok := sentinel["alerts"].([]interface{}); ok && len(alerts) > 0 {
				result["alerts"] = alerts
				result["degraded"] = true
			}
		}
	}

	if reasoningData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "reasoning_state.json")); err == nil {
		var reasoning map[string]interface{}
		if json.Unmarshal(reasoningData, &reasoning) == nil {
			result["reasoning"] = reasoning
		}
	}

	return result, nil
}

func (h *Handler) buildPublicStatus() (map[string]interface{}, error) {
	result := map[string]interface{}{
		"status":  "ok",
		"version": h.version,
	}

	sentinelData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "sentinel.json"))
	if err == nil {
		var s map[string]interface{}
		if json.Unmarshal(sentinelData, &s) == nil {
			if up, ok := s["uptime_seconds"].(float64); ok {
				hrs := int(up) / 3600
				mins := (int(up) % 3600) / 60
				if hrs > 24 {
					d := hrs / 24
					result["uptime_short"] = fmt.Sprintf("%dd", d)
					result["uptime"] = fmt.Sprintf("%dd %dh", d, hrs%24)
				} else if hrs > 0 {
					result["uptime_short"] = fmt.Sprintf("%dh", hrs)
					result["uptime"] = fmt.Sprintf("%dh %dm", hrs, mins)
				} else {
					result["uptime_short"] = fmt.Sprintf("%dm", mins)
					result["uptime"] = fmt.Sprintf("%dm", mins)
				}
			}
		}
	}

	if h.configPath != "" {
		if diskCfg, err := config.LoadConfig(h.configPath); err == nil {
			result["model"] = diskCfg.Agents.Defaults.Model
			result["provider"] = diskCfg.Agents.Defaults.Provider
		}
	} else if h.config != nil && h.config.Agents.Defaults.Model != "" {
		result["model"] = h.config.Agents.Defaults.Model
	}

	vaultDir := filepath.Join(h.workspacePath, "obsidian")
	folders := []string{"daily", "people", "preferences", "insights", "decisions", "projects", "blog", "state", "inbox"}
	totalNotes := 0
	for _, f := range folders {
		dir := filepath.Join(vaultDir, f)
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					totalNotes++
				}
			}
		}
	}
	result["notes"] = totalNotes

	if h.cronService != nil {
		jobsFile := filepath.Join(h.workspacePath, "cron", "jobs.json")
		if data, err := os.ReadFile(jobsFile); err == nil {
			var jobs []map[string]interface{}
			if json.Unmarshal(data, &jobs) == nil {
				count := 0
				for _, j := range jobs {
					if enabled, ok := j["enabled"].(bool); ok && enabled {
						count++
					}
				}
				result["cron_jobs"] = count
			}
		}
	}

	scoresData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "interaction_scores.json"))
	if err == nil {
		var scores []map[string]interface{}
		if json.Unmarshal(scoresData, &scores) == nil {
			cutoff := time.Now().UTC().Add(-24 * time.Hour)
			count24h := 0
			var lastActivity string
			for _, s := range scores {
				if ts, ok := s["timestamp"].(string); ok {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						if t.After(cutoff) {
							count24h++
						}
						if ts > lastActivity {
							lastActivity = ts
						}
					}
				}
			}
			result["interactions_24h"] = count24h
			if lastActivity != "" {
				result["last_activity"] = lastActivity
			}
		}
	}

	telemetryData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "telemetry.json"))
	if err == nil {
		var telemetry map[string]interface{}
		if json.Unmarshal(telemetryData, &telemetry) == nil {
			today := time.Now().Format("2006-01-02")
			if days, ok := telemetry["days"].([]interface{}); ok {
				for _, d := range days {
					if dm, ok := d.(map[string]interface{}); ok {
						if dm["date"] == today {
							if totals, ok := dm["totals"].(map[string]interface{}); ok {
								if tt, ok := totals["total_tokens"].(float64); ok {
									result["tokens_today"] = int64(tt)
								}
								if calls, ok := totals["calls"].(float64); ok {
									result["calls_today"] = int(calls)
								}
							}
							break
						}
					}
				}
			}
		}
	}

	reasoningData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "reasoning_state.json"))
	if err == nil {
		var rs map[string]interface{}
		if json.Unmarshal(reasoningData, &rs) == nil {
			if obs, ok := rs["observations_today"].(float64); ok {
				result["observations_today"] = int(obs)
			}
			if esc, ok := rs["escalations_today"].(float64); ok {
				result["escalations_today"] = int(esc)
			}
		}
	}

	return result, nil
}

// fileInfo is the JSON shape returned by the list endpoint.
type fileInfo struct {
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
}

// networkInfo represents a physical network interface.
type networkInfo struct {
	Name    string `json:"name"`
	State   string `json:"state"` // up/down
	IP      string `json:"ip"`    // first IPv4 if available
	RxBytes int64  `json:"rx_bytes"`
	TxBytes int64  `json:"tx_bytes"`
}

// bluetoothInfo represents a Bluetooth adapter.
type bluetoothInfo struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	State   string `json:"state"` // up/down
}

// extendedHealth merges sentinel data with live system info.
type extendedHealth struct {
	// Sentinel fields (embedded from JSON)
	Sentinel json.RawMessage `json:"sentinel"`
	// Swap
	SwapTotalMB int64   `json:"swap_total_mb"`
	SwapFreeMB  int64   `json:"swap_free_mb"`
	SwapUsedPct float64 `json:"swap_used_percent"`
	// Network
	Networks []networkInfo `json:"networks"`
	// Bluetooth
	Bluetooth []bluetoothInfo `json:"bluetooth"`
}

func (h *Handler) systemHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var result extendedHealth

	// Read sentinel data
	sentinelData, err := os.ReadFile(filepath.Join(h.workspacePath, "state", "sentinel.json"))
	if err == nil {
		result.Sentinel = json.RawMessage(sentinelData)
	} else {
		result.Sentinel = json.RawMessage(`{}`)
	}

	// Read swap from /proc/meminfo
	result.SwapTotalMB, result.SwapFreeMB, result.SwapUsedPct = readSwap()

	// Read network interfaces
	result.Networks = readNetworks()

	// Read bluetooth
	result.Bluetooth = readBluetooth()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// physicalInterfaces are the network interfaces we care about (not veth/bridge/docker).
var physicalInterfaces = map[string]bool{
	"eth0": true, "wlan0": true, "tailscale0": true,
}

func readSwap() (totalMB, freeMB int64, usedPct float64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "SwapTotal:") {
			totalMB = parseMemInfoKB(line) / 1024
		} else if strings.HasPrefix(line, "SwapFree:") {
			freeMB = parseMemInfoKB(line) / 1024
		}
	}
	if totalMB > 0 {
		usedPct = float64(totalMB-freeMB) / float64(totalMB) * 100
	}
	return
}

func parseMemInfoKB(line string) int64 {
	// "SwapTotal:       2097136 kB"
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(fields[1], 10, 64)
	return v
}

func readNetworks() []networkInfo {
	var nets []networkInfo

	// Read RX/TX from /proc/net/dev
	rxTx := make(map[string][2]int64) // name -> [rx, tx]
	if f, err := os.Open("/proc/net/dev"); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			if !physicalInterfaces[name] {
				continue
			}
			fields := strings.Fields(parts[1])
			if len(fields) >= 9 {
				rx, _ := strconv.ParseInt(fields[0], 10, 64)
				tx, _ := strconv.ParseInt(fields[8], 10, 64)
				rxTx[name] = [2]int64{rx, tx}
			}
		}
		f.Close()
	}

	// Read state and IP for each physical interface
	for name := range physicalInterfaces {
		operstate := "down"
		if data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/operstate", name)); err == nil {
			operstate = strings.TrimSpace(string(data))
		} else {
			continue // interface doesn't exist
		}

		ip := getInterfaceIPv4(name)

		rt := rxTx[name]
		nets = append(nets, networkInfo{
			Name:    name,
			State:   operstate,
			IP:      ip,
			RxBytes: rt[0],
			TxBytes: rt[1],
		})
	}

	return nets
}

func getInterfaceIPv4(name string) string {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return ""
}

func readBluetooth() []bluetoothInfo {
	var bt []bluetoothInfo

	entries, err := os.ReadDir("/sys/class/bluetooth")
	if err != nil {
		return bt
	}

	seen := make(map[string]bool)
	for _, e := range entries {
		// hci0, hci0:12 — only care about base adapter
		hciName := strings.SplitN(e.Name(), ":", 2)[0]
		if seen[hciName] {
			continue
		}
		seen[hciName] = true

		addr := ""
		if data, err := os.ReadFile(fmt.Sprintf("/sys/class/bluetooth/%s/address", hciName)); err == nil {
			addr = strings.TrimSpace(string(data))
		}

		// Check if adapter is up by reading type (if readable, it's present)
		state := "down"
		if data, err := os.ReadFile(fmt.Sprintf("/sys/class/bluetooth/%s/type", hciName)); err == nil && len(data) > 0 {
			state = "up"
		}

		bt = append(bt, bluetoothInfo{
			Name:    hciName,
			Address: addr,
			State:   state,
		})
	}

	return bt
}

// hostExec runs a command via nsenter in the host's mount+uts+ipc+net namespace.
// Uses /hostfs/proc/1/ns/* when --pid=host is not available (Coolify ignores it).
// IPC namespace is needed for D-Bus access (bluetoothctl, pactl, etc.).
func hostExec(timeout time.Duration, args ...string) ([]byte, error) {
	var nsArgs []string
	if _, err := os.Stat("/hostfs/proc/1/ns/mnt"); err == nil {
		// Coolify container: --pid=host not applied, use /hostfs/proc bind mount
		nsArgs = append([]string{
			"--mount=/hostfs/proc/1/ns/mnt",
			"--uts=/hostfs/proc/1/ns/uts",
			"--ipc=/hostfs/proc/1/ns/ipc",
			"--net=/hostfs/proc/1/ns/net",
			"--",
		}, args...)
	} else {
		// Fallback: assume --pid=host works
		nsArgs = append([]string{"-t", "1", "-m", "-u", "-i", "-n", "--"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, "nsenter", nsArgs...).CombinedOutput()
}

func (h *Handler) freeSwap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	logger.InfoC("admin", "Freeing swap...")
	out, err := hostExec(60*time.Second, "sh", "-c", "swapoff -a && swapon -a")
	if err != nil {
		logger.ErrorCF("admin", "Free swap failed", map[string]interface{}{"error": err.Error(), "output": string(out)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "swapoff failed: " + string(out)})
		return
	}

	logger.InfoC("admin", "Swap freed successfully")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type wifiNetwork struct {
	SSID    string `json:"ssid"`
	Signal  string `json:"signal"`
	Freq    string `json:"freq"`
	BSSID   string `json:"bssid"`
	Channel string `json:"channel"`
}

var (
	reBSS     = regexp.MustCompile(`^BSS ([0-9a-f:]+)`)
	reSSID    = regexp.MustCompile(`^\s+SSID: (.+)`)
	reSignal  = regexp.MustCompile(`^\s+signal: (.+)`)
	reFreq    = regexp.MustCompile(`^\s+freq: (\d+)`)
	reChannel = regexp.MustCompile(`^\s+DS Parameter set: channel (\d+)`)
)

func (h *Handler) wifiScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	out, err := hostExec(15*time.Second, "iw", "dev", "wlan0", "scan")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "wifi scan failed: " + string(out)})
		return
	}

	var networks []wifiNetwork
	var cur *wifiNetwork

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if m := reBSS.FindStringSubmatch(line); m != nil {
			if cur != nil && cur.SSID != "" {
				networks = append(networks, *cur)
			}
			cur = &wifiNetwork{BSSID: m[1]}
		}
		if cur == nil {
			continue
		}
		if m := reSSID.FindStringSubmatch(line); m != nil {
			cur.SSID = m[1]
		} else if m := reSignal.FindStringSubmatch(line); m != nil {
			cur.Signal = m[1]
		} else if m := reFreq.FindStringSubmatch(line); m != nil {
			cur.Freq = m[1] + " MHz"
		} else if m := reChannel.FindStringSubmatch(line); m != nil {
			cur.Channel = m[1]
		}
	}
	if cur != nil && cur.SSID != "" {
		networks = append(networks, *cur)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(networks)
}

type btDevice struct {
	Address   string `json:"address"`
	Name      string `json:"name"`
	Paired    bool   `json:"paired"`
	Connected bool   `json:"connected"`
}

var btAddrRegex = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

func (h *Handler) bluetoothScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Step 1: trigger BLE + classic scan (8 sec)
	hostExec(12*time.Second, "sh", "-c", "bluetoothctl --timeout 8 scan on >/dev/null 2>&1")

	// Step 2: list all devices with status
	out, err := hostExec(10*time.Second, "sh", "-c", `
bluetoothctl -- devices | while IFS= read -r _ addr rest; do
  info=$(bluetoothctl -- info "$addr" 2>/dev/null)
  paired=$(echo "$info" | grep -c "Paired: yes")
  connected=$(echo "$info" | grep -c "Connected: yes")
  echo "$addr|$rest|$paired|$connected"
done`)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "bluetooth scan failed: " + string(out)})
		return
	}

	var devices []btDevice
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		devices = append(devices, btDevice{
			Address:   strings.TrimSpace(parts[0]),
			Name:      strings.TrimSpace(parts[1]),
			Paired:    strings.TrimSpace(parts[2]) == "1",
			Connected: strings.TrimSpace(parts[3]) == "1",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(devices)
}

func (h *Handler) bluetoothConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct{ Address string }
	json.NewDecoder(r.Body).Decode(&req)
	if !btAddrRegex.MatchString(req.Address) {
		http.Error(w, "invalid MAC address", http.StatusBadRequest)
		return
	}
	// Pair (ignores error if already paired) then connect
	hostExec(10*time.Second, "sh", "-c", "bluetoothctl -- pair "+req.Address+" 2>/dev/null")
	out, err := hostExec(15*time.Second, "sh", "-c", "bluetoothctl -- connect "+req.Address)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": string(out)})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "connected"})
}

func (h *Handler) bluetoothDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct{ Address string }
	json.NewDecoder(r.Body).Decode(&req)
	if !btAddrRegex.MatchString(req.Address) {
		http.Error(w, "invalid MAC address", http.StatusBadRequest)
		return
	}
	out, err := hostExec(10*time.Second, "sh", "-c", "bluetoothctl -- disconnect "+req.Address)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": string(out)})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "disconnected"})
}

func (h *Handler) bluetoothRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct{ Address string }
	json.NewDecoder(r.Body).Decode(&req)
	if !btAddrRegex.MatchString(req.Address) {
		http.Error(w, "invalid MAC address", http.StatusBadRequest)
		return
	}
	out, err := hostExec(10*time.Second, "sh", "-c", "bluetoothctl -- remove "+req.Address)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": string(out)})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "removed"})
}

func (h *Handler) listFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	files := make([]fileInfo, 0, len(allowedFiles))
	for _, name := range allowedFiles {
		full := filepath.Join(h.workspacePath, name)
		_, err := os.Stat(full)
		files = append(files, fileInfo{Name: name, Exists: err == nil})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

func (h *Handler) handleFile(w http.ResponseWriter, r *http.Request) {
	// Extract filename from /api/files/{name}
	name := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if name == "" {
		http.Error(w, "Missing filename", http.StatusBadRequest)
		return
	}

	// Validate against whitelist (prevents path traversal)
	if !h.isAllowed(name) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Double-check: cleaned path must stay inside workspace
	full := filepath.Join(h.workspacePath, name)
	if !strings.HasPrefix(full, h.workspacePath) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.readFile(w, full)
	case http.MethodPut:
		h.writeFile(w, r, full)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) isAllowed(name string) bool {
	clean := filepath.Clean(name)
	for _, allowed := range allowedFiles {
		if clean == allowed {
			return true
		}
	}
	return false
}

func (h *Handler) readFile(w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty content for files that don't exist yet
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"content": ""})
			return
		}
		logger.ErrorCF("admin", "Read file error", map[string]interface{}{"error": err.Error()})
		http.Error(w, "Read error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"content": string(data)})
}

// --- Vault endpoints ---

type vaultFolderInfo struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type vaultOverviewResp struct {
	TotalNotes int               `json:"total_notes"`
	Folders    []vaultFolderInfo `json:"folders"`
}

func (h *Handler) vaultOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	vaultDir := filepath.Join(h.workspacePath, "obsidian")
	folders := []string{"daily", "people", "preferences", "insights", "decisions", "projects", "blog", "state", "inbox"}

	var resp vaultOverviewResp
	for _, f := range folders {
		dir := filepath.Join(vaultDir, f)
		entries, err := os.ReadDir(dir)
		count := 0
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					count++
				}
			}
		}
		resp.Folders = append(resp.Folders, vaultFolderInfo{Name: f, Count: count})
		resp.TotalNotes += count
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type vaultNoteInfo struct {
	Key     string   `json:"key"`
	Folder  string   `json:"folder"`
	Tags    []string `json:"tags"`
	Updated string   `json:"updated"`
	Preview string   `json:"preview"`
}

func (h *Handler) vaultNotes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	folder := r.URL.Query().Get("folder")
	if folder == "" {
		http.Error(w, "folder param required", http.StatusBadRequest)
		return
	}

	// Sanitize folder name
	folder = filepath.Clean(folder)
	if strings.Contains(folder, "..") || strings.Contains(folder, "/") {
		http.Error(w, "invalid folder", http.StatusBadRequest)
		return
	}

	dir := filepath.Join(h.workspacePath, "obsidian", folder)
	entries, err := os.ReadDir(dir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]vaultNoteInfo{})
		return
	}

	var notes []vaultNoteInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		key := strings.TrimSuffix(e.Name(), ".md")
		content := string(data)

		note := vaultNoteInfo{
			Key:    key,
			Folder: folder,
		}

		// Parse frontmatter
		body := content
		if strings.HasPrefix(content, "---\n") {
			rest := content[4:]
			if endIdx := strings.Index(rest, "\n---\n"); endIdx != -1 {
				fm := rest[:endIdx]
				body = strings.TrimSpace(rest[endIdx+5:])
				for _, line := range strings.Split(fm, "\n") {
					ci := strings.Index(line, ": ")
					if ci == -1 {
						continue
					}
					field, val := line[:ci], line[ci+2:]
					switch field {
					case "tags":
						val = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(val), "]"), "[")
						for _, t := range strings.Split(val, ",") {
							t = strings.TrimSpace(t)
							if t != "" {
								note.Tags = append(note.Tags, t)
							}
						}
					case "updated":
						note.Updated = val
					}
				}
			}
		}

		// Preview: first 120 chars of body
		preview := strings.ReplaceAll(body, "\n", " ")
		if len(preview) > 120 {
			preview = preview[:120] + "..."
		}
		note.Preview = preview
		notes = append(notes, note)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(notes)
}

func (h *Handler) vaultNote(w http.ResponseWriter, r *http.Request) {
	// /api/vault/note/{folder}/{key}
	path := strings.TrimPrefix(r.URL.Path, "/api/vault/note/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "path must be /api/vault/note/{folder}/{key}", http.StatusBadRequest)
		return
	}

	folder := filepath.Clean(parts[0])
	key := filepath.Clean(parts[1])

	if strings.Contains(folder, "..") || strings.Contains(key, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	notePath := filepath.Join(h.workspacePath, "obsidian", folder, key+".md")

	// Ensure path stays within workspace
	if !strings.HasPrefix(notePath, filepath.Join(h.workspacePath, "obsidian")) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(notePath)
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"content": string(data)})

	case http.MethodPut:
		var body struct {
			Content string `json:"content"`
		}
		limited := io.LimitReader(r.Body, 1<<20)
		if err := json.NewDecoder(limited).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		dir := filepath.Dir(notePath)
		os.MkdirAll(dir, 0755)

		tmp := notePath + ".tmp"
		if err := os.WriteFile(tmp, []byte(body.Content), 0644); err != nil {
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, notePath); err != nil {
			os.Remove(tmp)
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}

		logger.InfoCF("admin", "Vault note saved", map[string]interface{}{"folder": folder, "key": key})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	case http.MethodDelete:
		if err := os.Remove(notePath); err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "delete error", http.StatusInternalServerError)
			return
		}
		logger.InfoCF("admin", "Vault note deleted", map[string]interface{}{"folder": folder, "key": key})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) writeFile(w http.ResponseWriter, r *http.Request, path string) {
	var body struct {
		Content string `json:"content"`
	}

	// Limit to 1MB
	limited := io.LimitReader(r.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	// Ensure parent dir exists (for council/*.md)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		logger.ErrorCF("admin", "Mkdir error", map[string]interface{}{"error": err.Error()})
		http.Error(w, "Write error", http.StatusInternalServerError)
		return
	}

	// Atomic write: tmp file + rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body.Content), 0644); err != nil {
		logger.ErrorCF("admin", "Write temp file error", map[string]interface{}{"error": err.Error()})
		http.Error(w, "Write error", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		logger.ErrorCF("admin", "Rename error", map[string]interface{}{"error": err.Error()})
		http.Error(w, "Write error", http.StatusInternalServerError)
		return
	}

	logger.InfoCF("admin", "File saved", map[string]interface{}{"path": path})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// --- Agent Reload ---

func (h *Handler) agentReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	reloaded := []string{}

	// Re-read config from disk
	newCfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		logger.ErrorCF("admin", "Reload config failed", map[string]interface{}{"error": err.Error()})
		jsonErr(w, http.StatusInternalServerError, "config reload failed: "+err.Error())
		return
	}

	// Update config fields in-place (pointer is shared with all services)
	h.config.Agents = newCfg.Agents
	h.config.Providers = newCfg.Providers
	h.config.Tools = newCfg.Tools
	h.config.Heartbeat = newCfg.Heartbeat
	h.config.Sentinel = newCfg.Sentinel
	h.config.Devices = newCfg.Devices
	h.config.Council = newCfg.Council
	h.config.Channels = newCfg.Channels
	h.config.Briefing = newCfg.Briefing
	reloaded = append(reloaded, "config")

	// Reload cron jobs
	if h.reloadFn != nil {
		if err := h.reloadFn(); err != nil {
			logger.ErrorCF("admin", "Reload services failed", map[string]interface{}{"error": err.Error()})
		} else {
			reloaded = append(reloaded, "cron")
		}
	}

	logger.InfoCF("admin", "Agent reloaded", map[string]interface{}{"reloaded": reloaded})
	jsonOK(w, map[string]interface{}{"status": "ok", "reloaded": reloaded})
}

// sseEvents serves Server-Sent Events for real-time agent activity visualization.
// Events: think, tool, memory, browse, learn, cron, heartbeat
func (h *Handler) sseEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	ch := h.eventBus.subscribe()
	defer h.eventBus.unsubscribe(ch)

	// Send initial keepalive
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			payload, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}
