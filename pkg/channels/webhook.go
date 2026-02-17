package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// WebhookChannel receives external events via HTTP POST and routes them to the agent.
// Responses from the agent are logged but not sent back (fire-and-forget);
// the agent's response will be routed to the last active channel (e.g. Telegram) via the bus.
type WebhookChannel struct {
	*BaseChannel
	config     config.WebhookConfig
	httpServer *http.Server
}

type webhookPayload struct {
	Source   string            `json:"source"`
	Event   string            `json:"event"`
	Content string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// NewWebhookChannel creates a new webhook channel instance.
func NewWebhookChannel(cfg config.WebhookConfig, messageBus *bus.MessageBus) (*WebhookChannel, error) {
	base := NewBaseChannel("webhook", cfg, messageBus, nil) // no allowList, auth is via bearer token

	return &WebhookChannel{
		BaseChannel: base,
		config:      cfg,
	}, nil
}

// Start launches the HTTP webhook server.
func (c *WebhookChannel) Start(ctx context.Context) error {
	logger.InfoC("webhook", "Starting webhook channel")

	mux := http.NewServeMux()
	path := c.config.Path
	if path == "" {
		path = "/webhook/inbound"
	}
	mux.HandleFunc(path, c.handler)

	addr := fmt.Sprintf("%s:%d", c.config.Host, c.config.Port)
	c.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		logger.InfoCF("webhook", "Webhook server listening", map[string]interface{}{
			"addr": addr,
			"path": path,
		})
		if err := c.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.ErrorCF("webhook", "Webhook server error", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	c.setRunning(true)
	logger.InfoC("webhook", "Webhook channel started")
	return nil
}

// Stop gracefully shuts down the HTTP server.
func (c *WebhookChannel) Stop(ctx context.Context) error {
	logger.InfoC("webhook", "Stopping webhook channel")

	if c.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := c.httpServer.Shutdown(shutdownCtx); err != nil {
			logger.ErrorCF("webhook", "Webhook server shutdown error", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}

	c.setRunning(false)
	logger.InfoC("webhook", "Webhook channel stopped")
	return nil
}

// Send logs outbound messages (webhook is fire-and-forget, responses go to other channels).
func (c *WebhookChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	logger.DebugCF("webhook", "Webhook outbound (logged only)", map[string]interface{}{
		"chat_id":     msg.ChatID,
		"content_len": len(msg.Content),
	})
	return nil
}

// handler processes incoming webhook POST requests.
func (c *WebhookChannel) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate bearer token if configured
	if c.config.Secret != "" {
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + c.config.Secret
		if !strings.EqualFold(auth, expected) {
			logger.WarnC("webhook", "Invalid or missing bearer token")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.ErrorCF("webhook", "Failed to read request body", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.ErrorCF("webhook", "Failed to parse webhook payload", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	if payload.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	logger.InfoCF("webhook", "Received webhook event", map[string]interface{}{
		"source":  payload.Source,
		"event":   payload.Event,
		"preview": utils.Truncate(payload.Content, 80),
	})

	// Return 200 OK immediately
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))

	// Process event asynchronously
	go c.processEvent(payload)
}

// processEvent routes the webhook payload to the agent via the message bus.
func (c *WebhookChannel) processEvent(payload webhookPayload) {
	// Build a descriptive message for the agent
	content := fmt.Sprintf("[Webhook: %s/%s] %s", payload.Source, payload.Event, payload.Content)

	// Use source as sender ID, event type as part of chat ID
	senderID := fmt.Sprintf("webhook:%s", payload.Source)
	chatID := fmt.Sprintf("webhook:%s:%s", payload.Source, payload.Event)

	metadata := map[string]string{
		"platform": "webhook",
		"source":   payload.Source,
		"event":    payload.Event,
	}
	for k, v := range payload.Metadata {
		metadata[k] = v
	}

	c.HandleMessage(senderID, chatID, content, nil, metadata)
}
