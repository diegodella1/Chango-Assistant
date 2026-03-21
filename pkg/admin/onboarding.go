package admin

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

func (h *Handler) onboardingStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var completedSteps []string

	// WiFi: always true (if they reached the admin panel, network is connected)
	completedSteps = append(completedSteps, "wifi")

	// Provider: at least one provider has a non-empty API key
	hasProvider := h.config.Providers.OpenRouter.APIKey != "" ||
		h.config.Providers.OpenAI.APIKey != "" ||
		h.config.Providers.Anthropic.APIKey != "" ||
		h.config.Providers.Groq.APIKey != "" ||
		h.config.Providers.Gemini.APIKey != "" ||
		h.config.Providers.DeepSeek.APIKey != ""
	if hasProvider {
		completedSteps = append(completedSteps, "provider")
	}

	// Telegram: token is non-empty
	hasTelegram := h.config.Channels.Telegram.Token != ""
	if hasTelegram {
		completedSteps = append(completedSteps, "telegram")
	}

	// Profile: USER.md exists and is non-empty in workspace
	hasProfile := false
	userMDPath := filepath.Join(h.workspacePath, "USER.md")
	if info, err := os.Stat(userMDPath); err == nil && info.Size() > 0 {
		hasProfile = true
		completedSteps = append(completedSteps, "profile")
	}

	// Onboarding is needed if no Telegram token AND no provider keys
	needed := !hasTelegram && !hasProvider && !hasProfile

	jsonOK(w, map[string]interface{}{
		"needed":          needed,
		"completed_steps": completedSteps,
	})
}

func (h *Handler) onboardingProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name     string `json:"name"`
		Role     string `json:"role"`
		Industry string `json:"industry"`
		Timezone string `json:"timezone"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	content := fmt.Sprintf(`# User Profile

- **Name**: %s
- **Role**: %s
- **Industry**: %s
- **Timezone**: %s
- **Language**: es-AR
`, req.Name, req.Role, req.Industry, req.Timezone)

	// Atomic write: tmp + rename
	target := filepath.Join(h.workspacePath, "USER.md")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		jsonErr(w, http.StatusInternalServerError, "write error: "+err.Error())
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		jsonErr(w, http.StatusInternalServerError, "write error: "+err.Error())
		return
	}

	jsonOK(w, map[string]string{"status": "ok"})
}
