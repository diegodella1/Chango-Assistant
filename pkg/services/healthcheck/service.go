package healthcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/state"
)

// HealthLog persists the last N health checks.
type HealthLog struct {
	Checks []HealthCheck `json:"checks"`
}

// HealthCheck records a single endpoint check result.
type HealthCheck struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Status    int    `json:"status"`
	OK        bool   `json:"ok"`
	Timestamp string `json:"timestamp"`
	Failures  int    `json:"consecutive_failures"`
}

// Service monitors HTTP endpoints and optionally restarts containers.
type Service struct {
	cfg       config.HealthConfig
	bus       *bus.MessageBus
	state     *state.Manager
	workspace string
	client    *http.Client
	failures  map[string]int // endpoint name -> consecutive failures
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
}

const maxLogEntries = 100

// NewService creates a new health check service.
func NewService(cfg config.HealthConfig, workspace string, stateMgr *state.Manager) *Service {
	if cfg.Interval <= 0 {
		cfg.Interval = 300
	}
	return &Service{
		cfg:       cfg,
		state:     stateMgr,
		workspace: workspace,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		failures: make(map[string]int),
	}
}

// SetBus sets the message bus for sending alerts.
func (s *Service) SetBus(b *bus.MessageBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = b
}

// Start begins the health check polling loop.
func (s *Service) Start(ctx context.Context) {
	if !s.cfg.Enabled || len(s.cfg.Endpoints) == 0 {
		logger.InfoC("healthcheck", "Health check service disabled or no endpoints configured")
		return
	}

	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	logger.InfoC("healthcheck", "Health check service started")

	// Run immediately on start
	s.checkAll()

	ticker := time.NewTicker(time.Duration(s.cfg.Interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkAll()
		}
	}
}

// Stop stops the health check service.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	logger.InfoC("healthcheck", "Health check service stopped")
}

func (s *Service) checkAll() {
	var results []HealthCheck

	for _, ep := range s.cfg.Endpoints {
		result := s.checkEndpoint(ep)
		results = append(results, result)

		s.mu.Lock()
		if result.OK {
			s.failures[ep.Name] = 0
		} else {
			s.failures[ep.Name]++
		}
		consecutiveFailures := s.failures[ep.Name]
		msgBus := s.bus
		s.mu.Unlock()

		result.Failures = consecutiveFailures

		// Try docker restart at 3 consecutive failures
		if consecutiveFailures == 3 && ep.Container != "" {
			logger.InfoCF("healthcheck", "Attempting docker restart", map[string]interface{}{
				"container": ep.Container,
				"endpoint":  ep.Name,
			})
			if err := exec.Command("docker", "restart", ep.Container).Run(); err != nil {
				logger.ErrorCF("healthcheck", "Docker restart failed", map[string]interface{}{
					"container": ep.Container,
					"error":     err.Error(),
				})
			}
		}

		// Send alert at 5 consecutive failures
		if consecutiveFailures == 5 && msgBus != nil {
			s.sendAlert(msgBus, fmt.Sprintf("Endpoint '%s' (%s) lleva %d fallos consecutivos (status: %d)",
				ep.Name, ep.URL, consecutiveFailures, result.Status))
		}
	}

	s.saveLog(results)

	logger.DebugCF("healthcheck", "Health check round complete", map[string]interface{}{
		"endpoints": len(s.cfg.Endpoints),
	})
}

func (s *Service) checkEndpoint(ep config.HealthEndpoint) HealthCheck {
	expectedStatus := ep.ExpectStatus
	if expectedStatus == 0 {
		expectedStatus = 200
	}

	result := HealthCheck{
		Name:      ep.Name,
		URL:       ep.URL,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	resp, err := s.client.Get(ep.URL)
	if err != nil {
		result.Status = 0
		result.OK = false
		logger.WarnCF("healthcheck", "Endpoint check failed", map[string]interface{}{
			"name":  ep.Name,
			"error": err.Error(),
		})
		return result
	}
	resp.Body.Close()

	result.Status = resp.StatusCode
	result.OK = resp.StatusCode == expectedStatus

	return result
}

func (s *Service) sendAlert(msgBus *bus.MessageBus, alert string) {
	lastChannel := s.state.GetLastChannel()
	if lastChannel == "" {
		return
	}

	parts := strings.SplitN(lastChannel, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return
	}
	platform, userID := parts[0], parts[1]
	if constants.IsInternalChannel(platform) {
		return
	}

	msgBus.PublishOutbound(bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: "🔴 " + alert,
	})

	logger.InfoCF("healthcheck", "Alert sent", map[string]interface{}{
		"alert": alert,
		"to":    platform,
	})
}

func (s *Service) saveLog(newChecks []HealthCheck) {
	stateDir := filepath.Join(s.workspace, "state")
	os.MkdirAll(stateDir, 0755)

	logPath := filepath.Join(stateDir, "health_log.json")

	// Load existing log
	var healthLog HealthLog
	if data, err := os.ReadFile(logPath); err == nil {
		json.Unmarshal(data, &healthLog)
	}

	// Append new checks
	healthLog.Checks = append(healthLog.Checks, newChecks...)

	// Keep only last N entries
	if len(healthLog.Checks) > maxLogEntries {
		healthLog.Checks = healthLog.Checks[len(healthLog.Checks)-maxLogEntries:]
	}

	// Atomic write
	data, err := json.MarshalIndent(healthLog, "", "  ")
	if err != nil {
		logger.ErrorCF("healthcheck", "Failed to marshal log", map[string]interface{}{"error": err.Error()})
		return
	}

	tmpPath := logPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		logger.ErrorCF("healthcheck", "Failed to write log", map[string]interface{}{"error": err.Error()})
		return
	}
	if err := os.Rename(tmpPath, logPath); err != nil {
		os.Remove(tmpPath)
		logger.ErrorCF("healthcheck", "Failed to rename log file", map[string]interface{}{"error": err.Error()})
	}
}
