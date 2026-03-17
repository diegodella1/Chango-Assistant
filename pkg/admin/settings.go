package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// maskSecret returns "****" + last 4 chars, or empty if empty.
func maskSecret(val string) string {
	if val == "" {
		return ""
	}
	if len(val) <= 4 {
		return "****"
	}
	return "****" + val[len(val)-4:]
}

// resolveSecret keeps existing value if newVal starts with "****".
func resolveSecret(newVal, existing string) string {
	if strings.HasPrefix(newVal, "****") {
		return existing
	}
	return newVal
}

// jsonOK writes a JSON response.
func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// jsonErr writes a JSON error response.
func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// decodeBody reads JSON body (max 1MB).
func decodeBody(r *http.Request, v interface{}) error {
	limited := io.LimitReader(r.Body, 1<<20)
	return json.NewDecoder(limited).Decode(v)
}

func (h *Handler) saveConfig() error {
	return config.SaveConfig(h.configPath, h.config)
}

// --- Providers ---

type providerView struct {
	APIKey  string `json:"api_key"`
	APIBase string `json:"api_base"`
}

type providersResponse struct {
	Anthropic     providerView `json:"anthropic"`
	OpenAI        providerView `json:"openai"`
	OpenRouter    providerView `json:"openrouter"`
	Groq          providerView `json:"groq"`
	Gemini        providerView `json:"gemini"`
	DeepSeek      providerView `json:"deepseek"`
	Zhipu         providerView `json:"zhipu"`
	VLLM          providerView `json:"vllm"`
	Nvidia        providerView `json:"nvidia"`
	Moonshot      providerView `json:"moonshot"`
	ShengSuanYun  providerView `json:"shengsuanyun"`
	GitHubCopilot providerView `json:"github_copilot"`
}

func providerToView(p config.ProviderConfig) providerView {
	return providerView{
		APIKey:  maskSecret(p.APIKey),
		APIBase: p.APIBase,
	}
}

func (h *Handler) settingsProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p := h.config.Providers
		jsonOK(w, providersResponse{
			Anthropic:     providerToView(p.Anthropic),
			OpenAI:        providerToView(p.OpenAI),
			OpenRouter:    providerToView(p.OpenRouter),
			Groq:          providerToView(p.Groq),
			Gemini:        providerToView(p.Gemini),
			DeepSeek:      providerToView(p.DeepSeek),
			Zhipu:         providerToView(p.Zhipu),
			VLLM:          providerToView(p.VLLM),
			Nvidia:        providerToView(p.Nvidia),
			Moonshot:      providerToView(p.Moonshot),
			ShengSuanYun:  providerToView(p.ShengSuanYun),
			GitHubCopilot: providerToView(p.GitHubCopilot),
		})

	case http.MethodPut:
		var req providersResponse
		if err := decodeBody(r, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		p := &h.config.Providers
		p.Anthropic.APIKey = resolveSecret(req.Anthropic.APIKey, p.Anthropic.APIKey)
		p.Anthropic.APIBase = req.Anthropic.APIBase
		p.OpenAI.APIKey = resolveSecret(req.OpenAI.APIKey, p.OpenAI.APIKey)
		p.OpenAI.APIBase = req.OpenAI.APIBase
		p.OpenRouter.APIKey = resolveSecret(req.OpenRouter.APIKey, p.OpenRouter.APIKey)
		p.OpenRouter.APIBase = req.OpenRouter.APIBase
		p.Groq.APIKey = resolveSecret(req.Groq.APIKey, p.Groq.APIKey)
		p.Groq.APIBase = req.Groq.APIBase
		p.Gemini.APIKey = resolveSecret(req.Gemini.APIKey, p.Gemini.APIKey)
		p.Gemini.APIBase = req.Gemini.APIBase
		p.DeepSeek.APIKey = resolveSecret(req.DeepSeek.APIKey, p.DeepSeek.APIKey)
		p.DeepSeek.APIBase = req.DeepSeek.APIBase
		p.Zhipu.APIKey = resolveSecret(req.Zhipu.APIKey, p.Zhipu.APIKey)
		p.Zhipu.APIBase = req.Zhipu.APIBase
		p.VLLM.APIKey = resolveSecret(req.VLLM.APIKey, p.VLLM.APIKey)
		p.VLLM.APIBase = req.VLLM.APIBase
		p.Nvidia.APIKey = resolveSecret(req.Nvidia.APIKey, p.Nvidia.APIKey)
		p.Nvidia.APIBase = req.Nvidia.APIBase
		p.Moonshot.APIKey = resolveSecret(req.Moonshot.APIKey, p.Moonshot.APIKey)
		p.Moonshot.APIBase = req.Moonshot.APIBase
		p.ShengSuanYun.APIKey = resolveSecret(req.ShengSuanYun.APIKey, p.ShengSuanYun.APIKey)
		p.ShengSuanYun.APIBase = req.ShengSuanYun.APIBase
		p.GitHubCopilot.APIKey = resolveSecret(req.GitHubCopilot.APIKey, p.GitHubCopilot.APIKey)
		p.GitHubCopilot.APIBase = req.GitHubCopilot.APIBase

		if err := h.saveConfig(); err != nil {
			logger.ErrorCF("admin", "SaveConfig error", map[string]interface{}{"error": err.Error()})
			jsonErr(w, http.StatusInternalServerError, "save failed")
			return
		}
		logger.InfoC("admin", "Providers config updated")
		jsonOK(w, map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Channels ---

type channelView struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token,omitempty"`
	// Telegram-specific
	AllowFrom []string `json:"allow_from,omitempty"`
}

type channelsResponse struct {
	Telegram channelView `json:"telegram"`
	Discord  channelView `json:"discord"`
	Slack    channelView `json:"slack"`
	Webhook  channelView `json:"webhook"`
}

func (h *Handler) settingsChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ch := h.config.Channels
		jsonOK(w, channelsResponse{
			Telegram: channelView{
				Enabled:   ch.Telegram.Enabled,
				Token:     maskSecret(ch.Telegram.Token),
				AllowFrom: []string(ch.Telegram.AllowFrom),
			},
			Discord: channelView{
				Enabled: ch.Discord.Enabled,
				Token:   maskSecret(ch.Discord.Token),
			},
			Slack: channelView{
				Enabled: ch.Slack.Enabled,
				Token:   maskSecret(ch.Slack.BotToken),
			},
			Webhook: channelView{
				Enabled: ch.Webhook.Enabled,
			},
		})

	case http.MethodPut:
		var req channelsResponse
		if err := decodeBody(r, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		ch := &h.config.Channels
		ch.Telegram.Enabled = req.Telegram.Enabled
		ch.Telegram.Token = resolveSecret(req.Telegram.Token, ch.Telegram.Token)
		if req.Telegram.AllowFrom != nil {
			ch.Telegram.AllowFrom = config.FlexibleStringSlice(req.Telegram.AllowFrom)
		}
		ch.Discord.Enabled = req.Discord.Enabled
		ch.Discord.Token = resolveSecret(req.Discord.Token, ch.Discord.Token)
		ch.Slack.Enabled = req.Slack.Enabled
		ch.Slack.BotToken = resolveSecret(req.Slack.Token, ch.Slack.BotToken)
		ch.Webhook.Enabled = req.Webhook.Enabled

		if err := h.saveConfig(); err != nil {
			logger.ErrorCF("admin", "SaveConfig error", map[string]interface{}{"error": err.Error()})
			jsonErr(w, http.StatusInternalServerError, "save failed")
			return
		}
		logger.InfoC("admin", "Channels config updated")
		jsonOK(w, map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Tools ---

type toolsResponse struct {
	Web    webToolsView `json:"web"`
	Google googleView   `json:"google"`
}

type webToolsView struct {
	Serper     serperView     `json:"serper"`
	Brave      braveView      `json:"brave"`
	DuckDuckGo duckduckgoView `json:"duckduckgo"`
}

type serperView struct {
	Enabled    bool   `json:"enabled"`
	APIKey     string `json:"api_key"`
	MaxResults int    `json:"max_results"`
}

type braveView struct {
	Enabled    bool   `json:"enabled"`
	APIKey     string `json:"api_key"`
	MaxResults int    `json:"max_results"`
}

type duckduckgoView struct {
	Enabled    bool `json:"enabled"`
	MaxResults int  `json:"max_results"`
}

type googleView struct {
	ServiceAccountFile string `json:"service_account_file"`
	ImpersonateEmail   string `json:"impersonate_email"`
}

func (h *Handler) settingsTools(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		t := h.config.Tools
		jsonOK(w, toolsResponse{
			Web: webToolsView{
				Serper: serperView{
					Enabled:    t.Web.Serper.Enabled,
					APIKey:     maskSecret(t.Web.Serper.APIKey),
					MaxResults: t.Web.Serper.MaxResults,
				},
				Brave: braveView{
					Enabled:    t.Web.Brave.Enabled,
					APIKey:     maskSecret(t.Web.Brave.APIKey),
					MaxResults: t.Web.Brave.MaxResults,
				},
				DuckDuckGo: duckduckgoView{
					Enabled:    t.Web.DuckDuckGo.Enabled,
					MaxResults: t.Web.DuckDuckGo.MaxResults,
				},
			},
			Google: googleView{
				ServiceAccountFile: t.Google.ServiceAccountFile,
				ImpersonateEmail:   t.Google.ImpersonateEmail,
			},
		})

	case http.MethodPut:
		var req toolsResponse
		if err := decodeBody(r, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		t := &h.config.Tools
		t.Web.Serper.Enabled = req.Web.Serper.Enabled
		t.Web.Serper.APIKey = resolveSecret(req.Web.Serper.APIKey, t.Web.Serper.APIKey)
		t.Web.Serper.MaxResults = req.Web.Serper.MaxResults
		t.Web.Brave.Enabled = req.Web.Brave.Enabled
		t.Web.Brave.APIKey = resolveSecret(req.Web.Brave.APIKey, t.Web.Brave.APIKey)
		t.Web.Brave.MaxResults = req.Web.Brave.MaxResults
		t.Web.DuckDuckGo.Enabled = req.Web.DuckDuckGo.Enabled
		t.Web.DuckDuckGo.MaxResults = req.Web.DuckDuckGo.MaxResults
		t.Google.ImpersonateEmail = req.Google.ImpersonateEmail
		// service_account_file is set via upload, not here

		if err := h.saveConfig(); err != nil {
			logger.ErrorCF("admin", "SaveConfig error", map[string]interface{}{"error": err.Error()})
			jsonErr(w, http.StatusInternalServerError, "save failed")
			return
		}
		logger.InfoC("admin", "Tools config updated")
		jsonOK(w, map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Services ---

type servicesResponse struct {
	Heartbeat heartbeatView `json:"heartbeat"`
	Sentinel  sentinelView  `json:"sentinel"`
	Devices   devicesView   `json:"devices"`
}

type heartbeatView struct {
	Enabled  bool `json:"enabled"`
	Interval int  `json:"interval"`
}

type sentinelView struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"interval_seconds"`
}

type devicesView struct {
	Enabled    bool `json:"enabled"`
	MonitorUSB bool `json:"monitor_usb"`
}

func (h *Handler) settingsServices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonOK(w, servicesResponse{
			Heartbeat: heartbeatView{
				Enabled:  h.config.Heartbeat.Enabled,
				Interval: h.config.Heartbeat.Interval,
			},
			Sentinel: sentinelView{
				Enabled:         h.config.Sentinel.Enabled,
				IntervalSeconds: h.config.Sentinel.IntervalSeconds,
			},
			Devices: devicesView{
				Enabled:    h.config.Devices.Enabled,
				MonitorUSB: h.config.Devices.MonitorUSB,
			},
		})

	case http.MethodPut:
		var req servicesResponse
		if err := decodeBody(r, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		h.config.Heartbeat.Enabled = req.Heartbeat.Enabled
		if req.Heartbeat.Interval >= 5 {
			h.config.Heartbeat.Interval = req.Heartbeat.Interval
		}
		h.config.Sentinel.Enabled = req.Sentinel.Enabled
		if req.Sentinel.IntervalSeconds >= 30 {
			h.config.Sentinel.IntervalSeconds = req.Sentinel.IntervalSeconds
		}
		h.config.Devices.Enabled = req.Devices.Enabled
		h.config.Devices.MonitorUSB = req.Devices.MonitorUSB

		if err := h.saveConfig(); err != nil {
			logger.ErrorCF("admin", "SaveConfig error", map[string]interface{}{"error": err.Error()})
			jsonErr(w, http.StatusInternalServerError, "save failed")
			return
		}
		logger.InfoC("admin", "Services config updated")
		jsonOK(w, map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Agent ---

type agentResponse struct {
	Provider          string  `json:"provider"`
	Model             string  `json:"model"`
	Temperature       float64 `json:"temperature"`
	MaxTokens         int     `json:"max_tokens"`
	MaxToolIterations int     `json:"max_tool_iterations"`
}

func (h *Handler) settingsAgent(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a := h.config.Agents.Defaults
		jsonOK(w, agentResponse{
			Provider:          a.Provider,
			Model:             a.Model,
			Temperature:       a.Temperature,
			MaxTokens:         a.MaxTokens,
			MaxToolIterations: a.MaxToolIterations,
		})

	case http.MethodPut:
		var req agentResponse
		if err := decodeBody(r, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		a := &h.config.Agents.Defaults
		if req.Provider != "" {
			a.Provider = req.Provider
		}
		if req.Model != "" {
			a.Model = req.Model
		}
		if req.Temperature >= 0 && req.Temperature <= 2 {
			a.Temperature = req.Temperature
		}
		if req.MaxTokens > 0 {
			a.MaxTokens = req.MaxTokens
		}
		if req.MaxToolIterations > 0 {
			a.MaxToolIterations = req.MaxToolIterations
		}

		if err := h.saveConfig(); err != nil {
			logger.ErrorCF("admin", "SaveConfig error", map[string]interface{}{"error": err.Error()})
			jsonErr(w, http.StatusInternalServerError, "save failed")
			return
		}
		logger.InfoC("admin", "Agent config updated")
		jsonOK(w, map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Upload Google Service Account ---

func (h *Handler) uploadGoogleSA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Max 100KB
	r.Body = http.MaxBytesReader(w, r.Body, 100*1024)

	file, _, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "read error")
		return
	}

	// Validate JSON structure
	var sa map[string]interface{}
	if err := json.Unmarshal(data, &sa); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON file")
		return
	}
	if sa["type"] != "service_account" {
		jsonErr(w, http.StatusBadRequest, "not a service account file (type must be 'service_account')")
		return
	}

	// Save to fixed path
	saPath := filepath.Join(filepath.Dir(h.configPath), "google-service-account.json")
	tmp := saPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		jsonErr(w, http.StatusInternalServerError, "write error")
		return
	}
	if err := os.Rename(tmp, saPath); err != nil {
		os.Remove(tmp)
		jsonErr(w, http.StatusInternalServerError, "write error")
		return
	}

	// Update config
	h.config.Tools.Google.ServiceAccountFile = saPath
	if err := h.saveConfig(); err != nil {
		logger.ErrorCF("admin", "SaveConfig error", map[string]interface{}{"error": err.Error()})
		jsonErr(w, http.StatusInternalServerError, "config save failed")
		return
	}

	clientEmail, _ := sa["client_email"].(string)
	logger.InfoCF("admin", "Google SA uploaded", map[string]interface{}{"client_email": clientEmail})
	jsonOK(w, map[string]interface{}{
		"status":       "ok",
		"client_email": clientEmail,
		"path":         saPath,
	})
}

// --- Test Provider ---

func (h *Handler) testProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Provider string `json:"provider"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Get the provider config
	var apiKey, apiBase string
	switch req.Provider {
	case "anthropic":
		apiKey = h.config.Providers.Anthropic.APIKey
		apiBase = h.config.Providers.Anthropic.APIBase
		if apiBase == "" {
			apiBase = "https://api.anthropic.com/v1"
		}
	case "openai":
		apiKey = h.config.Providers.OpenAI.APIKey
		apiBase = h.config.Providers.OpenAI.APIBase
		if apiBase == "" {
			apiBase = "https://api.openai.com/v1"
		}
	case "openrouter":
		apiKey = h.config.Providers.OpenRouter.APIKey
		apiBase = h.config.Providers.OpenRouter.APIBase
		if apiBase == "" {
			apiBase = "https://openrouter.ai/api/v1"
		}
	case "groq":
		apiKey = h.config.Providers.Groq.APIKey
		apiBase = h.config.Providers.Groq.APIBase
		if apiBase == "" {
			apiBase = "https://api.groq.com/openai/v1"
		}
	case "gemini":
		apiKey = h.config.Providers.Gemini.APIKey
		apiBase = h.config.Providers.Gemini.APIBase
	case "deepseek":
		apiKey = h.config.Providers.DeepSeek.APIKey
		apiBase = h.config.Providers.DeepSeek.APIBase
		if apiBase == "" {
			apiBase = "https://api.deepseek.com/v1"
		}
	default:
		jsonErr(w, http.StatusBadRequest, "unknown provider: "+req.Provider)
		return
	}

	if apiKey == "" {
		jsonErr(w, http.StatusBadRequest, "no API key configured for "+req.Provider)
		return
	}

	// Simple models list request (lightweight, works for OpenAI-compatible APIs)
	url := apiBase + "/models"
	httpReq, _ := http.NewRequest("GET", url, nil)
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	// Anthropic uses different auth header
	if req.Provider == "anthropic" {
		httpReq.Header.Set("x-api-key", apiKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		url = apiBase + "/messages"
		body := `{"model":"claude-3-haiku-20240307","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`
		httpReq, _ = http.NewRequest("POST", url, bytes.NewBufferString(body))
		httpReq.Header.Set("x-api-key", apiKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		httpReq.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		jsonOK(w, map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("connection failed: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		jsonOK(w, map[string]interface{}{
			"status":      "ok",
			"status_code": resp.StatusCode,
		})
	} else {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		jsonOK(w, map[string]interface{}{
			"status":      "error",
			"status_code": resp.StatusCode,
			"error":       string(respBody),
		})
	}
}
