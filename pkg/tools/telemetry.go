package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/telemetry"
)

type TelemetryTool struct {
	tracker *telemetry.Tracker
}

func NewTelemetryTool(tracker *telemetry.Tracker) *TelemetryTool {
	return &TelemetryTool{tracker: tracker}
}

func (t *TelemetryTool) Name() string { return "telemetry" }

func (t *TelemetryTool) Description() string {
	return "Check token usage statistics. Use when the user asks about token consumption, costs, or usage stats."
}

func (t *TelemetryTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"today", "day", "summary"},
				"description": "Action: 'today' for today's usage, 'day' for a specific date, 'summary' for last 7 days",
			},
			"date": map[string]interface{}{
				"type":        "string",
				"description": "Date in YYYY-MM-DD format (only for 'day' action)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *TelemetryTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	action, _ := args["action"].(string)

	switch action {
	case "today":
		bucket := t.tracker.GetToday()
		return SilentResult(telemetry.FormatDayBucket(bucket))

	case "day":
		date, _ := args["date"].(string)
		if date == "" {
			return ErrorResult("date is required for 'day' action (format: YYYY-MM-DD)")
		}
		bucket := t.tracker.GetDay(date)
		return SilentResult(telemetry.FormatDayBucket(bucket))

	case "summary":
		days := t.tracker.GetLastNDays(7)
		if len(days) == 0 {
			return SilentResult("No telemetry data available yet.")
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Token usage summary (last %d days):\n\n", len(days)))

		var grandTotal int64
		var grandCalls int64
		for _, d := range days {
			sb.WriteString(fmt.Sprintf("%s: %d tokens in %d calls\n",
				d.Date, d.Totals.TotalTokens, d.Totals.Calls))
			grandTotal += d.Totals.TotalTokens
			grandCalls += d.Totals.Calls
		}
		sb.WriteString(fmt.Sprintf("\nGrand total: %d tokens in %d calls over %d days\n",
			grandTotal, grandCalls, len(days)))

		return SilentResult(sb.String())

	default:
		return ErrorResult("invalid action, use: today, day, or summary")
	}
}
