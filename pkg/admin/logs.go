package admin

import (
	"net/http"
	"strconv"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// getLogs handles GET /api/logs?lines=100&level=error&search=telegram
func (h *Handler) getLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lines := 100
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}

	level := r.URL.Query().Get("level")
	search := r.URL.Query().Get("search")

	logs := logger.GetRecentLogs(lines, level, search)
	if logs == nil {
		logs = []logger.LogEntry{}
	}

	jsonOK(w, map[string]interface{}{
		"logs":  logs,
		"total": len(logs),
	})
}
