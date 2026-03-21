package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) updatesCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/diegodella1/picoclaw/releases/latest")
	if err != nil {
		jsonOK(w, map[string]interface{}{
			"current":          h.version,
			"latest":           "",
			"update_available": false,
			"error":            "github api request failed: " + err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		errMsg := "github api returned status " + resp.Status
		if err != nil {
			errMsg = "read response failed: " + err.Error()
		}
		jsonOK(w, map[string]interface{}{
			"current":          h.version,
			"latest":           "",
			"update_available": false,
			"error":            errMsg,
		})
		return
	}

	var release struct {
		TagName     string `json:"tag_name"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		jsonOK(w, map[string]interface{}{
			"current":          h.version,
			"latest":           "",
			"update_available": false,
			"error":            "parse response failed: " + err.Error(),
		})
		return
	}

	// Compare versions (strip leading "v" for comparison)
	current := strings.TrimPrefix(h.version, "v")
	latest := strings.TrimPrefix(release.TagName, "v")
	updateAvailable := current != latest && latest != ""

	jsonOK(w, map[string]interface{}{
		"current":          h.version,
		"latest":           release.TagName,
		"update_available": updateAvailable,
		"release_notes":    release.Body,
		"published_at":     release.PublishedAt,
	})
}

func (h *Handler) updatesApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	jsonOK(w, map[string]interface{}{
		"status":  "not_implemented",
		"message": "Deploy via Coolify: push to fork + trigger restart",
	})
}
