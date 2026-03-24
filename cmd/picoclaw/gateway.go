package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/admin"
	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/council"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/devices"
	"github.com/sipeed/picoclaw/pkg/heartbeat"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/sentinel"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/telemetry"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/voice"
)

func gatewayCmd() {
	startTime := time.Now()

	// Check for --debug flag
	args := os.Args[2:]
	for _, arg := range args {
		if arg == "--debug" || arg == "-d" {
			logger.SetLevel(logger.DEBUG)
			fmt.Println("🔍 Debug mode enabled")
			break
		}
	}

	configPath := getConfigPath()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	provider, err := providers.CreateProvider(cfg)
	if err != nil {
		fmt.Printf("Error creating provider: %v\n", err)
		os.Exit(1)
	}

	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider, getConfigPath())

	// Set up local provider for inner monologue (zero cost, private)
	if cfg.Providers.LlamaCpp.Enabled {
		localProv, err := providers.CreateLlamaCppProvider(cfg)
		if err == nil {
			agentLoop.SetLocalProvider(localProv)
			fmt.Println("✓ Local provider set for inner monologue (zero cost)")
		}
	}

	// Print agent startup info
	fmt.Println("\n📦 Agent Status:")
	startupInfo := agentLoop.GetStartupInfo()
	toolsInfo := startupInfo["tools"].(map[string]interface{})
	skillsInfo := startupInfo["skills"].(map[string]interface{})
	fmt.Printf("  • Tools: %d loaded\n", toolsInfo["count"])
	fmt.Printf("  • Skills: %d/%d available\n",
		skillsInfo["available"],
		skillsInfo["total"])

	// Log to file as well
	logger.InfoCF("agent", "Agent initialized",
		map[string]interface{}{
			"tools_count":      toolsInfo["count"],
			"skills_total":     skillsInfo["total"],
			"skills_available": skillsInfo["available"],
		})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup telemetry tracker
	tracker := telemetry.NewTracker(cfg.WorkspacePath())
	tracker.Start(ctx)
	agentLoop.SetTracker(tracker)
	agentLoop.RegisterTool(tools.NewTelemetryTool(tracker))
	fmt.Println("✓ Telemetry tracker started")

	// Setup cron tool and service
	cronService := setupCronTool(agentLoop, msgBus, cfg.WorkspacePath())

	// Council setup
	if cfg.Council.Enabled && len(cfg.Council.Members) > 0 {
		councilInstance, err := council.NewCouncil(cfg.Council, provider, cfg.Agents.Defaults.Model, cfg.WorkspacePath())
		if err != nil {
			fmt.Printf("⚠ Council init failed: %v\n", err)
		} else {
			// Give council members access to research tools via runner
			councilTools := tools.NewToolRegistry()
			if searchTool := tools.NewWebSearchTool(tools.WebSearchToolOptions{
				BraveAPIKey:          cfg.Tools.Web.Brave.APIKey,
				BraveMaxResults:      cfg.Tools.Web.Brave.MaxResults,
				BraveEnabled:         cfg.Tools.Web.Brave.Enabled,
				DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
				DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
			}); searchTool != nil {
				councilTools.Register(searchTool)
			}
			councilTools.Register(tools.NewWebFetchTool(50000))
			councilTools.Register(tools.NewMemoryTool(cfg.WorkspacePath()))

			councilInstance.SetRunner(func(ctx context.Context, model string, msgs []providers.Message) (string, error) {
				result, err := tools.RunToolLoop(ctx, tools.ToolLoopConfig{
					Provider:      provider,
					Model:         model,
					Tools:         councilTools,
					MaxIterations: 5,
					LLMOptions: map[string]any{
						"max_tokens":  2048,
						"temperature": 0.7,
					},
				}, msgs, "", "")
				if err != nil {
					return "", err
				}
				return strings.TrimSpace(result.Content), nil
			})

			councilTool := tools.NewCouncilTool(councilInstance)
			councilTool.SetSendCallback(func(channel, chatID, content string) error {
				msgBus.PublishOutbound(bus.OutboundMessage{
					Channel: channel,
					ChatID:  chatID,
					Content: content,
				})
				return nil
			})
			agentLoop.RegisterTool(councilTool)
			fmt.Printf("✓ Council enabled with %d members, tools: %d\n", len(cfg.Council.Members), councilTools.Count())
		}
	}

	heartbeatService := heartbeat.NewHeartbeatService(
		cfg.WorkspacePath(),
		cfg.Heartbeat.Interval,
		cfg.Heartbeat.Enabled,
	)
	heartbeatService.SetBus(msgBus)
	heartbeatService.SetHandler(func(prompt, channel, chatID string) *tools.ToolResult {
		// Use cli:direct as fallback if no valid channel
		if channel == "" || chatID == "" {
			channel, chatID = "cli", "direct"
		}
		// Use ProcessHeartbeat - no session history, each heartbeat is independent
		response, err := agentLoop.ProcessHeartbeat(context.Background(), prompt, channel, chatID)
		if err != nil {
			return tools.ErrorResult(fmt.Sprintf("Heartbeat error: %v", err))
		}
		if response == "HEARTBEAT_OK" {
			return tools.SilentResult("Heartbeat OK")
		}
		// For heartbeat, always return silent - the subagent result will be
		// sent to user via processSystemMessage when the async task completes
		return tools.SilentResult(response)
	})

	channelManager, err := channels.NewManager(cfg, msgBus)
	if err != nil {
		fmt.Printf("Error creating channel manager: %v\n", err)
		os.Exit(1)
	}

	// Configure Telegram channel for group support
	if telegramCh, ok := channelManager.GetChannel("telegram"); ok {
		if tc, ok := telegramCh.(*channels.TelegramChannel); ok {
			tc.SetConfigPath(getConfigPath())
			// Set admin from first allow_from entry (owner)
			if len(cfg.Channels.Telegram.AllowFrom) > 0 {
				adminID := string(cfg.Channels.Telegram.AllowFrom[0])
				// Strip compound "id|username" → just the ID
				if idx := strings.Index(adminID, "|"); idx > 0 {
					adminID = adminID[:idx]
				}
				tc.SetAdminUserID(adminID)
			}
		}
	}

	var transcriber *voice.GroqTranscriber
	if cfg.Providers.Groq.APIKey != "" {
		transcriber = voice.NewGroqTranscriber(cfg.Providers.Groq.APIKey)
		logger.InfoC("voice", "Groq voice transcription enabled")
	}

	if transcriber != nil {
		if telegramChannel, ok := channelManager.GetChannel("telegram"); ok {
			if tc, ok := telegramChannel.(*channels.TelegramChannel); ok {
				tc.SetTranscriber(transcriber)
				logger.InfoC("voice", "Groq transcription attached to Telegram channel")
			}
		}
		if discordChannel, ok := channelManager.GetChannel("discord"); ok {
			if dc, ok := discordChannel.(*channels.DiscordChannel); ok {
				dc.SetTranscriber(transcriber)
				logger.InfoC("voice", "Groq transcription attached to Discord channel")
			}
		}
		if slackChannel, ok := channelManager.GetChannel("slack"); ok {
			if sc, ok := slackChannel.(*channels.SlackChannel); ok {
				sc.SetTranscriber(transcriber)
				logger.InfoC("voice", "Groq transcription attached to Slack channel")
			}
		}
	}

	enabledChannels := channelManager.GetEnabledChannels()
	if len(enabledChannels) > 0 {
		fmt.Printf("✓ Channels enabled: %s\n", enabledChannels)
	} else {
		fmt.Println("⚠ Warning: No channels enabled")
	}

	fmt.Printf("✓ Gateway started on %s:%d\n", cfg.Gateway.Host, cfg.Gateway.Port)
	fmt.Println("Press Ctrl+C to stop")

	if err := cronService.Start(); err != nil {
		fmt.Printf("Error starting cron service: %v\n", err)
	}
	fmt.Println("✓ Cron service started")

	if err := heartbeatService.Start(); err != nil {
		fmt.Printf("Error starting heartbeat service: %v\n", err)
	}
	fmt.Println("✓ Heartbeat service started")

	stateManager := state.NewManager(cfg.WorkspacePath())
	deviceService := devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
	}, stateManager)
	deviceService.SetBus(msgBus)
	if err := deviceService.Start(ctx); err != nil {
		fmt.Printf("Error starting device service: %v\n", err)
	} else if cfg.Devices.Enabled {
		fmt.Println("✓ Device event service started")
	}

	sentinelService := sentinel.NewService(sentinel.Config{
		Enabled:         cfg.Sentinel.Enabled,
		IntervalSeconds: cfg.Sentinel.IntervalSeconds,
		Workspace:       cfg.WorkspacePath(),
	}, stateManager)
	sentinelService.SetBus(msgBus)
	if err := sentinelService.Start(ctx); err != nil {
		fmt.Printf("Error starting sentinel: %v\n", err)
	} else if cfg.Sentinel.Enabled {
		fmt.Println("✓ Sentinel service started")
	}

	// Attention manager
	attentionService := attention.NewService(cfg.WorkspacePath(), stateManager)
	attentionService.SetBus(msgBus)
	go attentionService.Start(ctx)
	fmt.Println("✓ Attention manager started")

	if err := channelManager.StartAll(ctx); err != nil {
		fmt.Printf("Error starting channels: %v\n", err)
	}

	// Health check HTTP server
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status := map[string]interface{}{
			"status":  "ok",
			"version": formatVersion(),
			"uptime":  time.Since(startTime).String(),
		}
		json.NewEncoder(w).Encode(status)
	})
	// Admin panel
	if cfg.Admin.Enabled {
		adminHandler := admin.New(cfg.WorkspacePath(), cfg.Admin.Token, configPath, cfg)
		adminHandler.SetCronService(cronService)
		adminHandler.SetLightsTool(tools.NewLightsTool(cfg.WorkspacePath()))
		adminHandler.SetVersion(version)
		adminHandler.SetReloadFn(func() error {
			return cronService.Load()
		})
		adminHandler.Register(healthMux)
		// Connect agent activity events to admin SSE for real-time visualization
		agentLoop.SetEventCallback(adminHandler.EmitEvent)
		fmt.Println("✓ Admin panel enabled at /admin")
	}

	// Pitch deck static files
	pitchFS := http.FileServer(http.FS(pitchFiles))
	healthMux.Handle("/pitch/", pitchFS)
	healthMux.HandleFunc("/pitch", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pitch/", http.StatusMovedPermanently)
	})

	healthAddr := fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
	healthServer := &http.Server{Addr: healthAddr, Handler: healthMux}
	go func() {
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.ErrorCF("gateway", "Health server error", map[string]interface{}{"error": err.Error()})
		}
	}()
	fmt.Printf("✓ Health endpoint: http://%s/health\n", healthAddr)

	go agentLoop.Run(ctx)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	<-sigChan

	fmt.Println("\nShutting down...")
	cancel()
	healthServer.Close()
	tracker.Stop()
	attentionService.Stop()
	sentinelService.Stop()
	deviceService.Stop()
	heartbeatService.Stop()
	cronService.Stop()
	agentLoop.Stop()
	channelManager.StopAll(ctx)
	msgBus.Close()
	fmt.Println("✓ Gateway stopped")
}

func setupCronTool(agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, workspace string) *cron.CronService {
	cronStorePath := filepath.Join(workspace, "cron", "jobs.json")

	// Create cron service
	cronService := cron.NewCronService(cronStorePath, nil)

	// Create and register CronTool
	cronTool := tools.NewCronTool(cronService, agentLoop, msgBus, workspace)
	agentLoop.RegisterTool(cronTool)

	// Set the onJob handler
	cronService.SetOnJob(func(job *cron.CronJob) (string, error) {
		result := cronTool.ExecuteJob(context.Background(), job)
		return result, nil
	})

	return cronService
}
