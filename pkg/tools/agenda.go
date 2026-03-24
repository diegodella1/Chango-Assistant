package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var argTZ *time.Location

func init() {
	loc, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		// Fallback: UTC-3
		argTZ = time.FixedZone("ART", -3*60*60)
	} else {
		argTZ = loc
	}
}

type AgendaEvent struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Start     string `json:"start"`               // ISO 8601
	End       string `json:"end,omitempty"`        // ISO 8601, defaults to start+1h
	Location  string `json:"location,omitempty"`
	Notes     string `json:"notes,omitempty"`
	Recurring string `json:"recurring,omitempty"`  // daily, weekly, monthly, or empty
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type AgendaTool struct {
	filePath string
	mu       sync.Mutex
}

func NewAgendaTool(workspace string) *AgendaTool {
	dir := filepath.Join(workspace, "state")
	os.MkdirAll(dir, 0755)
	return &AgendaTool{
		filePath: filepath.Join(dir, "agenda.json"),
	}
}

func (t *AgendaTool) Name() string { return "agenda" }

func (t *AgendaTool) Description() string {
	return "Personal calendar/agenda. Create, list, update, delete events. Supports recurring events, natural date input (mañana 15:00, tomorrow 3pm), and date range queries. Timezone: Argentina (ART, UTC-3)."
}

func (t *AgendaTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"today", "list", "create", "update", "delete", "upcoming"},
				"description": "Action to perform",
			},
			"id": map[string]interface{}{
				"type":        "string",
				"description": "Event ID (required for update, delete)",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Event title (required for create)",
			},
			"start": map[string]interface{}{
				"type":        "string",
				"description": "Start date/time. Accepts: ISO 8601, YYYY-MM-DD, YYYY-MM-DD HH:MM, 'mañana 15:00', 'tomorrow 3pm'. Required for create.",
			},
			"end": map[string]interface{}{
				"type":        "string",
				"description": "End date/time (optional, defaults to start + 1 hour)",
			},
			"location": map[string]interface{}{
				"type":        "string",
				"description": "Event location (optional)",
			},
			"notes": map[string]interface{}{
				"type":        "string",
				"description": "Additional notes (optional)",
			},
			"recurring": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"daily", "weekly", "monthly", ""},
				"description": "Recurrence pattern (optional)",
			},
			"from": map[string]interface{}{
				"type":        "string",
				"description": "Start of date range for list action (default: today)",
			},
			"to": map[string]interface{}{
				"type":        "string",
				"description": "End of date range for list action (default: from + 7 days)",
			},
			"hours": map[string]interface{}{
				"type":        "number",
				"description": "Hours ahead for upcoming action (default: 4)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *AgendaTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)
	switch action {
	case "today":
		return t.today()
	case "list":
		return t.list(args)
	case "create":
		return t.create(args)
	case "update":
		return t.update(args)
	case "delete":
		return t.del(args)
	case "upcoming":
		return t.upcoming(args)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

// --- Storage ---

func (t *AgendaTool) loadEvents() ([]AgendaEvent, error) {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var events []AgendaEvent
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (t *AgendaTool) saveEvents(events []AgendaEvent) error {
	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: write to tmp then rename
	tmpPath := t.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, t.filePath)
}

func generateAgendaID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// --- Time parsing ---

// parseAgendaTime parses multiple date/time formats and returns a time.Time in ART.
func parseAgendaTime(input string) (time.Time, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return time.Time{}, fmt.Errorf("empty date/time")
	}

	now := time.Now().In(argTZ)

	// Natural language: "mañana HH:MM" or "mañana"
	if m := matchNatural(input, `(?i)^ma[ñn]ana\s*(\d{1,2}[:.]\d{2})?$`); m != nil {
		tomorrow := now.AddDate(0, 0, 1)
		return applyTimeToDate(tomorrow, m[1])
	}

	// Natural language: "tomorrow HH:MM" or "tomorrow Hpm/am"
	if m := matchNatural(input, `(?i)^tomorrow\s*(.*)$`); m != nil {
		tomorrow := now.AddDate(0, 0, 1)
		return applyTimeToDate(tomorrow, m[1])
	}

	// Natural language: "hoy HH:MM" or "today HH:MM"
	if m := matchNatural(input, `(?i)^(hoy|today)\s*(.*)$`); m != nil {
		return applyTimeToDate(now, m[2])
	}

	// Natural language: "pasado mañana HH:MM"
	if m := matchNatural(input, `(?i)^pasado\s+ma[ñn]ana\s*(.*)$`); m != nil {
		dayAfter := now.AddDate(0, 0, 2)
		return applyTimeToDate(dayAfter, m[1])
	}

	// Try ISO 8601 with timezone (RFC3339)
	if t, err := time.Parse(time.RFC3339, input); err == nil {
		return t.In(argTZ), nil
	}

	// Try "2006-01-02T15:04:05"
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", input, argTZ); err == nil {
		return t, nil
	}

	// Try "2006-01-02 15:04"
	if t, err := time.ParseInLocation("2006-01-02 15:04", input, argTZ); err == nil {
		return t, nil
	}

	// Try "2006-01-02 15:04:05"
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", input, argTZ); err == nil {
		return t, nil
	}

	// Try date-only: "2006-01-02" → defaults to 00:00
	if t, err := time.ParseInLocation("2006-01-02", input, argTZ); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("could not parse date/time: %q", input)
}

func matchNatural(input, pattern string) []string {
	re := regexp.MustCompile(pattern)
	m := re.FindStringSubmatch(input)
	if m == nil {
		return nil
	}
	return m
}

// applyTimeToDate applies a time string (HH:MM, H:MM, Hpm, HHpm, etc.) to a date.
// If timeStr is empty, defaults to 09:00.
func applyTimeToDate(date time.Time, timeStr string) (time.Time, error) {
	timeStr = strings.TrimSpace(timeStr)
	if timeStr == "" {
		return time.Date(date.Year(), date.Month(), date.Day(), 9, 0, 0, 0, argTZ), nil
	}

	// Try "3pm", "3PM", "3 pm", "15:00", "3:30pm", etc.
	timeStr = strings.ToLower(strings.ReplaceAll(timeStr, ".", ":"))

	// Match "HH:MM" or "H:MM"
	if m := regexp.MustCompile(`^(\d{1,2}):(\d{2})$`).FindStringSubmatch(timeStr); m != nil {
		h := parseInt(m[1])
		min := parseInt(m[2])
		return time.Date(date.Year(), date.Month(), date.Day(), h, min, 0, 0, argTZ), nil
	}

	// Match "H:MMam/pm" or "HH:MMam/pm"
	if m := regexp.MustCompile(`^(\d{1,2}):(\d{2})\s*(am|pm)$`).FindStringSubmatch(timeStr); m != nil {
		h := parseInt(m[1])
		min := parseInt(m[2])
		if m[3] == "pm" && h < 12 {
			h += 12
		}
		if m[3] == "am" && h == 12 {
			h = 0
		}
		return time.Date(date.Year(), date.Month(), date.Day(), h, min, 0, 0, argTZ), nil
	}

	// Match "Hpm", "Ham", "HHpm", "HHam"
	if m := regexp.MustCompile(`^(\d{1,2})\s*(am|pm)$`).FindStringSubmatch(timeStr); m != nil {
		h := parseInt(m[1])
		if m[2] == "pm" && h < 12 {
			h += 12
		}
		if m[2] == "am" && h == 12 {
			h = 0
		}
		return time.Date(date.Year(), date.Month(), date.Day(), h, 0, 0, 0, argTZ), nil
	}

	// Try just a number as hour
	if m := regexp.MustCompile(`^(\d{1,2})$`).FindStringSubmatch(timeStr); m != nil {
		h := parseInt(m[1])
		return time.Date(date.Year(), date.Month(), date.Day(), h, 0, 0, 0, argTZ), nil
	}

	return time.Time{}, fmt.Errorf("could not parse time: %q", timeStr)
}

func parseInt(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// --- Actions ---

func (t *AgendaTool) today() *ToolResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	events, err := t.loadEvents()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load agenda: %v", err))
	}

	now := time.Now().In(argTZ)
	todayStr := now.Format("2006-01-02")

	matching := t.eventsForDateRange(events, todayStr, todayStr)
	if len(matching) == 0 {
		return SilentResult(fmt.Sprintf("No events for today (%s)", todayStr))
	}

	sortEventsByStart(matching)
	var lines []string
	for _, e := range matching {
		lines = append(lines, formatAgendaEvent(e))
	}
	return SilentResult(fmt.Sprintf("Today (%s) — %d event(s):\n%s", todayStr, len(matching), strings.Join(lines, "\n")))
}

func (t *AgendaTool) list(args map[string]interface{}) *ToolResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	events, err := t.loadEvents()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load agenda: %v", err))
	}

	now := time.Now().In(argTZ)

	fromStr, _ := args["from"].(string)
	toStr, _ := args["to"].(string)

	var fromDate, toDate string
	if fromStr != "" {
		ft, err := parseAgendaTime(fromStr)
		if err != nil {
			return ErrorResult(fmt.Sprintf("invalid 'from': %v", err))
		}
		fromDate = ft.Format("2006-01-02")
	} else {
		fromDate = now.Format("2006-01-02")
	}

	if toStr != "" {
		tt, err := parseAgendaTime(toStr)
		if err != nil {
			return ErrorResult(fmt.Sprintf("invalid 'to': %v", err))
		}
		toDate = tt.Format("2006-01-02")
	} else {
		fd, _ := time.ParseInLocation("2006-01-02", fromDate, argTZ)
		toDate = fd.AddDate(0, 0, 7).Format("2006-01-02")
	}

	matching := t.eventsForDateRange(events, fromDate, toDate)
	if len(matching) == 0 {
		return SilentResult(fmt.Sprintf("No events from %s to %s", fromDate, toDate))
	}

	sortEventsByStart(matching)
	var lines []string
	for _, e := range matching {
		lines = append(lines, formatAgendaEvent(e))
	}
	return SilentResult(fmt.Sprintf("Events %s to %s — %d event(s):\n%s", fromDate, toDate, len(matching), strings.Join(lines, "\n")))
}

func (t *AgendaTool) create(args map[string]interface{}) *ToolResult {
	title, _ := args["title"].(string)
	if title == "" {
		return ErrorResult("title is required for create")
	}
	startStr, _ := args["start"].(string)
	if startStr == "" {
		return ErrorResult("start is required for create")
	}

	startTime, err := parseAgendaTime(startStr)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid start: %v", err))
	}

	var endTime time.Time
	endStr, _ := args["end"].(string)
	if endStr != "" {
		endTime, err = parseAgendaTime(endStr)
		if err != nil {
			return ErrorResult(fmt.Sprintf("invalid end: %v", err))
		}
	} else {
		endTime = startTime.Add(1 * time.Hour)
	}

	location, _ := args["location"].(string)
	notes, _ := args["notes"].(string)
	recurring, _ := args["recurring"].(string)
	if recurring != "" && recurring != "daily" && recurring != "weekly" && recurring != "monthly" {
		return ErrorResult("recurring must be: daily, weekly, monthly, or empty")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	events, err := t.loadEvents()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load agenda: %v", err))
	}

	now := time.Now().In(argTZ).Format(time.RFC3339)
	event := AgendaEvent{
		ID:        generateAgendaID(),
		Title:     title,
		Start:     startTime.Format(time.RFC3339),
		End:       endTime.Format(time.RFC3339),
		Location:  location,
		Notes:     notes,
		Recurring: recurring,
		CreatedAt: now,
		UpdatedAt: now,
	}

	events = append(events, event)
	if err := t.saveEvents(events); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}

	result := fmt.Sprintf("Event created: %s\nID: %s\nStart: %s\nEnd: %s",
		event.Title, event.ID,
		startTime.Format("2006-01-02 15:04"),
		endTime.Format("2006-01-02 15:04"))
	if location != "" {
		result += fmt.Sprintf("\nLocation: %s", location)
	}
	if recurring != "" {
		result += fmt.Sprintf("\nRecurring: %s", recurring)
	}
	return SilentResult(result)
}

func (t *AgendaTool) update(args map[string]interface{}) *ToolResult {
	id, _ := args["id"].(string)
	if id == "" {
		return ErrorResult("id is required for update")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	events, err := t.loadEvents()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load agenda: %v", err))
	}

	found := false
	for i, e := range events {
		if e.ID == id {
			if title, ok := args["title"].(string); ok && title != "" {
				events[i].Title = title
			}
			if startStr, ok := args["start"].(string); ok && startStr != "" {
				st, err := parseAgendaTime(startStr)
				if err != nil {
					return ErrorResult(fmt.Sprintf("invalid start: %v", err))
				}
				events[i].Start = st.Format(time.RFC3339)
			}
			if endStr, ok := args["end"].(string); ok && endStr != "" {
				et, err := parseAgendaTime(endStr)
				if err != nil {
					return ErrorResult(fmt.Sprintf("invalid end: %v", err))
				}
				events[i].End = et.Format(time.RFC3339)
			}
			if loc, ok := args["location"].(string); ok {
				events[i].Location = loc
			}
			if notes, ok := args["notes"].(string); ok {
				events[i].Notes = notes
			}
			events[i].UpdatedAt = time.Now().In(argTZ).Format(time.RFC3339)
			found = true
			break
		}
	}

	if !found {
		return SilentResult(fmt.Sprintf("No event found with ID '%s'", id))
	}

	if err := t.saveEvents(events); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}
	return SilentResult(fmt.Sprintf("Event '%s' updated", id))
}

func (t *AgendaTool) del(args map[string]interface{}) *ToolResult {
	id, _ := args["id"].(string)
	if id == "" {
		return ErrorResult("id is required for delete")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	events, err := t.loadEvents()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load agenda: %v", err))
	}

	found := false
	var filtered []AgendaEvent
	for _, e := range events {
		if e.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, e)
	}

	if !found {
		return SilentResult(fmt.Sprintf("No event found with ID '%s'", id))
	}

	if err := t.saveEvents(filtered); err != nil {
		return ErrorResult(fmt.Sprintf("failed to save: %v", err))
	}
	return SilentResult(fmt.Sprintf("Event '%s' deleted", id))
}

func (t *AgendaTool) upcoming(args map[string]interface{}) *ToolResult {
	hours := 4.0
	if h, ok := args["hours"].(float64); ok && h > 0 {
		hours = h
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	events, err := t.loadEvents()
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to load agenda: %v", err))
	}

	now := time.Now().In(argTZ)
	cutoff := now.Add(time.Duration(hours*60) * time.Minute)

	var matching []AgendaEvent
	for _, e := range events {
		expanded := t.expandRecurring(e, now.Format("2006-01-02"), now.Format("2006-01-02"))
		for _, exp := range expanded {
			st, err := time.Parse(time.RFC3339, exp.Start)
			if err != nil {
				continue
			}
			if (st.Equal(now) || st.After(now)) && st.Before(cutoff) {
				matching = append(matching, exp)
			}
		}
	}

	if len(matching) == 0 {
		return SilentResult(fmt.Sprintf("No events in the next %.0f hour(s)", hours))
	}

	sortEventsByStart(matching)
	var lines []string
	for _, e := range matching {
		lines = append(lines, formatAgendaEvent(e))
	}
	return SilentResult(fmt.Sprintf("Upcoming (%s, next %.0fh) — %d event(s):\n%s",
		now.Format("15:04"), hours, len(matching), strings.Join(lines, "\n")))
}

// --- Helpers ---

// eventsForDateRange returns events that fall within the given date range (inclusive).
// Expands recurring events.
func (t *AgendaTool) eventsForDateRange(events []AgendaEvent, fromDate, toDate string) []AgendaEvent {
	var result []AgendaEvent
	for _, e := range events {
		expanded := t.expandRecurring(e, fromDate, toDate)
		for _, exp := range expanded {
			st, err := time.Parse(time.RFC3339, exp.Start)
			if err != nil {
				continue
			}
			eventDate := st.Format("2006-01-02")
			if eventDate >= fromDate && eventDate <= toDate {
				result = append(result, exp)
			}
		}
	}
	return result
}

// expandRecurring expands a recurring event into individual occurrences within the date range.
// Non-recurring events return as-is if they fall in range.
func (t *AgendaTool) expandRecurring(e AgendaEvent, fromDate, toDate string) []AgendaEvent {
	if e.Recurring == "" {
		return []AgendaEvent{e}
	}

	st, err := time.Parse(time.RFC3339, e.Start)
	if err != nil {
		return nil
	}

	var et time.Time
	if e.End != "" {
		et, _ = time.Parse(time.RFC3339, e.End)
	}
	duration := et.Sub(st)
	if duration <= 0 {
		duration = time.Hour
	}

	from, _ := time.ParseInLocation("2006-01-02", fromDate, argTZ)
	to, _ := time.ParseInLocation("2006-01-02", toDate, argTZ)
	to = to.Add(24*time.Hour - time.Second) // end of day

	var results []AgendaEvent
	current := st
	// Don't generate more than 366 occurrences to prevent runaway
	for i := 0; i < 366 && !current.After(to); i++ {
		if !current.Before(from) || current.Format("2006-01-02") >= fromDate {
			occ := e
			occ.Start = current.Format(time.RFC3339)
			occ.End = current.Add(duration).Format(time.RFC3339)
			results = append(results, occ)
		}
		switch e.Recurring {
		case "daily":
			current = current.AddDate(0, 0, 1)
		case "weekly":
			current = current.AddDate(0, 0, 7)
		case "monthly":
			current = current.AddDate(0, 1, 0)
		default:
			return results
		}
	}
	return results
}

func sortEventsByStart(events []AgendaEvent) {
	sort.Slice(events, func(i, j int) bool {
		return events[i].Start < events[j].Start
	})
}

func formatAgendaEvent(e AgendaEvent) string {
	st, _ := time.Parse(time.RFC3339, e.Start)
	et, _ := time.Parse(time.RFC3339, e.End)

	line := fmt.Sprintf("- %s – %s: %s (ID: %s)",
		st.Format("15:04"), et.Format("15:04"), e.Title, e.ID)
	if e.Location != "" {
		line += fmt.Sprintf(" @ %s", e.Location)
	}
	if e.Recurring != "" {
		line += fmt.Sprintf(" [%s]", e.Recurring)
	}
	if e.Notes != "" {
		line += fmt.Sprintf(" — %s", e.Notes)
	}
	return line
}
