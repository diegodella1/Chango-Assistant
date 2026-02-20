package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// Feature labels for tracking token usage by purpose.
const (
	FeatureChat      = "chat"
	FeatureHeartbeat = "heartbeat"
	FeatureSummarize = "summarize"
	FeatureCron      = "cron"
)

// FeatureBucket tracks token usage for a single feature.
type FeatureBucket struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	Calls            int64 `json:"calls"`
}

// DayBucket tracks token usage for a single day.
type DayBucket struct {
	Date     string                    `json:"date"` // "2006-01-02"
	Features map[string]*FeatureBucket `json:"features"`
	Totals   FeatureBucket             `json:"totals"`
}

// TelemetryData is the on-disk format.
type TelemetryData struct {
	Days []*DayBucket `json:"days"`
}

// Tracker tracks token usage per feature per day.
type Tracker struct {
	mu       sync.Mutex
	data     *TelemetryData
	filePath string
	dirty    bool
}

// NewTracker creates a tracker that persists to workspace/state/telemetry.json.
func NewTracker(workspace string) *Tracker {
	fp := filepath.Join(workspace, "state", "telemetry.json")
	t := &Tracker{
		filePath: fp,
		data:     &TelemetryData{},
	}
	t.load()
	return t
}

// Start begins periodic flushing every 60 seconds.
func (t *Tracker) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.Flush()
			}
		}
	}()
}

// Stop performs a final flush.
func (t *Tracker) Stop() {
	t.Flush()
}

// Record adds token usage for the given feature. Hot path, mutex-only, no I/O.
func (t *Tracker) Record(feature string, prompt, completion, total int) {
	if total == 0 && prompt == 0 && completion == 0 {
		return
	}

	today := time.Now().Format("2006-01-02")

	t.mu.Lock()
	defer t.mu.Unlock()

	bucket := t.getOrCreateDay(today)
	fb, ok := bucket.Features[feature]
	if !ok {
		fb = &FeatureBucket{}
		bucket.Features[feature] = fb
	}

	fb.PromptTokens += int64(prompt)
	fb.CompletionTokens += int64(completion)
	fb.TotalTokens += int64(total)
	fb.Calls++

	bucket.Totals.PromptTokens += int64(prompt)
	bucket.Totals.CompletionTokens += int64(completion)
	bucket.Totals.TotalTokens += int64(total)
	bucket.Totals.Calls++

	t.dirty = true
}

// GetToday returns today's bucket (copy). Returns nil if no data yet.
func (t *Tracker) GetToday() *DayBucket {
	return t.GetDay(time.Now().Format("2006-01-02"))
}

// GetDay returns the bucket for a specific date (copy). Returns nil if not found.
func (t *Tracker) GetDay(date string) *DayBucket {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, d := range t.data.Days {
		if d.Date == date {
			return copyDayBucket(d)
		}
	}
	return nil
}

// GetLastNDays returns buckets for the last n days (most recent first).
func (t *Tracker) GetLastNDays(n int) []*DayBucket {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make([]*DayBucket, 0, n)
	// Days are stored oldest-first, iterate backwards
	for i := len(t.data.Days) - 1; i >= 0 && len(result) < n; i-- {
		result = append(result, copyDayBucket(t.data.Days[i]))
	}
	return result
}

// Flush writes data to disk if dirty. Prunes entries older than 30 days.
func (t *Tracker) Flush() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.dirty {
		return
	}

	t.prune(30)
	t.dirty = false

	data, err := json.MarshalIndent(t.data, "", "  ")
	if err != nil {
		logger.ErrorCF("telemetry", "Failed to marshal", map[string]interface{}{"error": err.Error()})
		return
	}

	dir := filepath.Dir(t.filePath)
	os.MkdirAll(dir, 0755)

	tmpPath := t.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		logger.ErrorCF("telemetry", "Failed to write tmp", map[string]interface{}{"error": err.Error()})
		return
	}
	if err := os.Rename(tmpPath, t.filePath); err != nil {
		logger.ErrorCF("telemetry", "Failed to rename", map[string]interface{}{"error": err.Error()})
	}
}

func (t *Tracker) load() {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return // File doesn't exist yet, start fresh
	}

	var td TelemetryData
	if err := json.Unmarshal(data, &td); err != nil {
		logger.WarnCF("telemetry", "Failed to parse telemetry data, starting fresh",
			map[string]interface{}{"error": err.Error()})
		return
	}
	t.data = &td
}

func (t *Tracker) getOrCreateDay(date string) *DayBucket {
	for _, d := range t.data.Days {
		if d.Date == date {
			return d
		}
	}
	bucket := &DayBucket{
		Date:     date,
		Features: make(map[string]*FeatureBucket),
	}
	t.data.Days = append(t.data.Days, bucket)
	return bucket
}

func (t *Tracker) prune(keepDays int) {
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")
	kept := make([]*DayBucket, 0, len(t.data.Days))
	for _, d := range t.data.Days {
		if d.Date >= cutoff {
			kept = append(kept, d)
		}
	}
	t.data.Days = kept
}

func copyDayBucket(src *DayBucket) *DayBucket {
	cp := &DayBucket{
		Date:     src.Date,
		Totals:   src.Totals,
		Features: make(map[string]*FeatureBucket, len(src.Features)),
	}
	for k, v := range src.Features {
		fb := *v
		cp.Features[k] = &fb
	}
	return cp
}

// FormatDayBucket returns a human-readable summary of a day bucket.
func FormatDayBucket(b *DayBucket) string {
	if b == nil {
		return "No data available."
	}

	result := fmt.Sprintf("Date: %s\n", b.Date)
	result += fmt.Sprintf("Total: %d tokens (%d prompt + %d completion) in %d calls\n",
		b.Totals.TotalTokens, b.Totals.PromptTokens, b.Totals.CompletionTokens, b.Totals.Calls)

	if len(b.Features) > 0 {
		result += "\nBy feature:\n"
		for name, fb := range b.Features {
			result += fmt.Sprintf("  %s: %d tokens (%d prompt + %d completion) in %d calls\n",
				name, fb.TotalTokens, fb.PromptTokens, fb.CompletionTokens, fb.Calls)
		}
	}
	return result
}
