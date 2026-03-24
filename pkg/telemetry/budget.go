package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// TokenBudget enforces daily token limits for total and background processes.
type TokenBudget struct {
	DailyLimit    int64
	BackgroundMax int64 // max tokens for background processes (cron, heartbeat, consciousness)
	mu            sync.Mutex
	todayUsed     int64
	bgUsed        int64
	lastReset     time.Time
	workspace     string
}

// budgetState is the on-disk format for token budget persistence.
type budgetState struct {
	Date      string `json:"date"`       // "2006-01-02"
	TodayUsed int64  `json:"today_used"`
	BgUsed    int64  `json:"bg_used"`
}

// NewTokenBudget creates a budget that persists to workspace/state/token_budget.json.
func NewTokenBudget(workspace string, dailyLimit, bgMax int64) *TokenBudget {
	tb := &TokenBudget{
		DailyLimit:    dailyLimit,
		BackgroundMax: bgMax,
		workspace:     workspace,
		lastReset:     time.Now(),
	}
	tb.load()
	return tb
}

// CanSpend checks if spending the given tokens is within budget.
func (tb *TokenBudget) CanSpend(tokens int64, isBackground bool) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.maybeReset()

	if tb.todayUsed+tokens > tb.DailyLimit {
		return false
	}
	if isBackground && tb.bgUsed+tokens > tb.BackgroundMax {
		return false
	}
	return true
}

// Record records token usage against the budget.
func (tb *TokenBudget) Record(tokens int64, isBackground bool) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.maybeReset()

	tb.todayUsed += tokens
	if isBackground {
		tb.bgUsed += tokens
	}

	tb.save()
}

// GetStatus returns current budget usage.
func (tb *TokenBudget) GetStatus() (todayUsed, bgUsed, dailyLimit, bgMax int64) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.maybeReset()

	return tb.todayUsed, tb.bgUsed, tb.DailyLimit, tb.BackgroundMax
}

// maybeReset resets counters at midnight ART. Must be called under lock.
func (tb *TokenBudget) maybeReset() {
	loc, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		loc = time.FixedZone("ART", -3*60*60)
	}

	now := time.Now().In(loc)
	lastDate := tb.lastReset.In(loc).Format("2006-01-02")
	todayDate := now.Format("2006-01-02")

	if lastDate != todayDate {
		logger.InfoCF("telemetry", "Token budget reset for new day", map[string]interface{}{
			"previous_total": tb.todayUsed,
			"previous_bg":    tb.bgUsed,
		})
		tb.todayUsed = 0
		tb.bgUsed = 0
		tb.lastReset = now
		tb.save()
	}
}

func (tb *TokenBudget) filePath() string {
	return filepath.Join(tb.workspace, "state", "token_budget.json")
}

func (tb *TokenBudget) load() {
	data, err := os.ReadFile(tb.filePath())
	if err != nil {
		return
	}

	var s budgetState
	if err := json.Unmarshal(data, &s); err != nil {
		logger.WarnCF("telemetry", "Failed to parse token budget, starting fresh",
			map[string]interface{}{"error": err.Error()})
		return
	}

	loc, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		loc = time.FixedZone("ART", -3*60*60)
	}
	todayDate := time.Now().In(loc).Format("2006-01-02")

	// Only restore if same day
	if s.Date == todayDate {
		tb.todayUsed = s.TodayUsed
		tb.bgUsed = s.BgUsed
	}
}

func (tb *TokenBudget) save() {
	loc, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		loc = time.FixedZone("ART", -3*60*60)
	}

	s := budgetState{
		Date:      time.Now().In(loc).Format("2006-01-02"),
		TodayUsed: tb.todayUsed,
		BgUsed:    tb.bgUsed,
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}

	dir := filepath.Dir(tb.filePath())
	os.MkdirAll(dir, 0755)

	tmpPath := tb.filePath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return
	}
	os.Rename(tmpPath, tb.filePath())
}
