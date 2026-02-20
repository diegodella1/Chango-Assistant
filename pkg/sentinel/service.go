package sentinel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/state"
)

// Config holds sentinel service configuration.
type Config struct {
	Enabled         bool
	IntervalSeconds int
	Workspace       string
}

// SentinelState is the JSON structure persisted to sentinel.json.
type SentinelState struct {
	LastCheck       time.Time `json:"last_check"`
	UptimeSeconds   int64     `json:"uptime_seconds"`
	CPUTempC        float64   `json:"cpu_temp_c"`
	RAMTotalMB      int64     `json:"ram_total_mb"`
	RAMAvailableMB  int64     `json:"ram_available_mb"`
	RAMUsedPercent  float64   `json:"ram_used_percent"`
	DiskTotalGB     float64   `json:"disk_total_gb"`
	DiskFreeGB      float64   `json:"disk_free_gb"`
	DiskUsedPercent float64   `json:"disk_used_percent"`
	Alerts          []string  `json:"alerts"`
}

// Service monitors system health and persists state.
type Service struct {
	cfg       Config
	bus       *bus.MessageBus
	state     *state.Manager
	startTime time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.RWMutex

	lastAlertTime map[string]time.Time
}

// NewService creates a new sentinel service.
func NewService(cfg Config, stateMgr *state.Manager) *Service {
	if cfg.IntervalSeconds <= 0 {
		cfg.IntervalSeconds = 120
	}
	return &Service{
		cfg:           cfg,
		state:         stateMgr,
		lastAlertTime: make(map[string]time.Time),
	}
}

// SetBus sets the message bus for sending critical alerts.
func (s *Service) SetBus(msgBus *bus.MessageBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = msgBus
}

// Start begins the sentinel polling loop.
func (s *Service) Start(ctx context.Context) error {
	if !s.cfg.Enabled {
		logger.InfoC("sentinel", "Sentinel service disabled")
		return nil
	}

	s.mu.Lock()
	s.startTime = time.Now()
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	go s.loop()

	logger.InfoC("sentinel", "Sentinel service started")
	return nil
}

// Stop stops the sentinel service.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	logger.InfoC("sentinel", "Sentinel service stopped")
}

func (s *Service) loop() {
	// Run immediately on start
	s.collect()

	ticker := time.NewTicker(time.Duration(s.cfg.IntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.collect()
		}
	}
}

func (s *Service) collect() {
	st := SentinelState{
		LastCheck:     time.Now(),
		UptimeSeconds: int64(time.Since(s.startTime).Seconds()),
	}

	st.CPUTempC = readCPUTemp()
	st.RAMTotalMB, st.RAMAvailableMB, st.RAMUsedPercent = readRAM()
	st.DiskTotalGB, st.DiskFreeGB, st.DiskUsedPercent = readDisk()

	// Check thresholds and build alerts
	var alerts []string
	if st.CPUTempC > 80 {
		alerts = append(alerts, fmt.Sprintf("CPU temperatura alta: %.1f°C", st.CPUTempC))
	}
	if st.RAMUsedPercent > 90 {
		alerts = append(alerts, fmt.Sprintf("RAM crítica: %.1f%% usada", st.RAMUsedPercent))
	}
	if st.DiskUsedPercent > 95 {
		alerts = append(alerts, fmt.Sprintf("Disco casi lleno: %.1f%% usado", st.DiskUsedPercent))
	}
	st.Alerts = alerts

	// Persist state
	s.saveState(&st)

	// Send critical alerts via MessageBus (max 1 per alert type per hour)
	for _, alert := range alerts {
		s.sendAlert(alert)
	}

	logger.DebugCF("sentinel", "Collected metrics", map[string]interface{}{
		"cpu_temp":    st.CPUTempC,
		"ram_pct":     st.RAMUsedPercent,
		"disk_pct":    st.DiskUsedPercent,
		"alerts":      len(alerts),
	})
}

func (s *Service) saveState(st *SentinelState) {
	stateDir := filepath.Join(s.cfg.Workspace, "state")
	os.MkdirAll(stateDir, 0755)

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		logger.ErrorCF("sentinel", "Failed to marshal state", map[string]interface{}{"error": err.Error()})
		return
	}

	filePath := filepath.Join(stateDir, "sentinel.json")
	tmpPath := filePath + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		logger.ErrorCF("sentinel", "Failed to write state", map[string]interface{}{"error": err.Error()})
		return
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		logger.ErrorCF("sentinel", "Failed to rename state file", map[string]interface{}{"error": err.Error()})
	}
}

func (s *Service) sendAlert(alert string) {
	s.mu.Lock()
	lastTime, exists := s.lastAlertTime[alert]
	if exists && time.Since(lastTime) < time.Hour {
		s.mu.Unlock()
		return
	}
	s.lastAlertTime[alert] = time.Now()
	msgBus := s.bus
	s.mu.Unlock()

	if msgBus == nil {
		return
	}

	lastChannel := s.state.GetLastChannel()
	if lastChannel == "" {
		return
	}

	platform, userID := parseLastChannel(lastChannel)
	if platform == "" || userID == "" || constants.IsInternalChannel(platform) {
		return
	}

	msgBus.PublishOutbound(bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: "⚠️ " + alert,
	})

	logger.InfoCF("sentinel", "Critical alert sent", map[string]interface{}{
		"alert": alert,
		"to":    platform,
	})
}

func parseLastChannel(lastChannel string) (platform, userID string) {
	if lastChannel == "" {
		return "", ""
	}
	parts := strings.SplitN(lastChannel, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// --- System metric readers ---

func readCPUTemp() float64 {
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(data))
	var milliC int64
	fmt.Sscanf(s, "%d", &milliC)
	return float64(milliC) / 1000.0
}

func readRAM() (totalMB, availableMB int64, usedPct float64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}

	var memTotal, memAvailable int64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fmt.Sscanf(line, "MemTotal: %d kB", &memTotal)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &memAvailable)
		}
	}

	totalMB = memTotal / 1024
	availableMB = memAvailable / 1024
	if totalMB > 0 {
		usedPct = float64(totalMB-availableMB) / float64(totalMB) * 100
	}
	return
}

func readDisk() (totalGB, freeGB float64, usedPct float64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, 0, 0
	}

	totalBytes := stat.Blocks * uint64(stat.Bsize)
	freeBytes := stat.Bavail * uint64(stat.Bsize)

	totalGB = float64(totalBytes) / (1024 * 1024 * 1024)
	freeGB = float64(freeBytes) / (1024 * 1024 * 1024)
	if totalGB > 0 {
		usedPct = (1 - freeGB/totalGB) * 100
	}
	return
}
