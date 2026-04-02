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
	"github.com/sipeed/picoclaw/pkg/heartbeat"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/sentinel"
	"github.com/sipeed/picoclaw/pkg/services/emailwatch"
	"github.com/sipeed/picoclaw/pkg/services/healthcheck"
	"github.com/sipeed/picoclaw/pkg/services/reasoning"
	"github.com/sipeed/picoclaw/pkg/services/rsswatch"
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

	// Set up background provider for cheap escalation (heartbeat/cron/summarize)
	if cfg.Background.Provider != "" {
		bgProv, err := providers.CreateProviderByName(cfg, cfg.Background.Provider)
		if err == nil {
			agentLoop.SetBackgroundProvider(bgProv, cfg.Background.Model)
			fmt.Printf("✓ Background provider set: %s/%s (cheap escalation)\n", cfg.Background.Provider, cfg.Background.Model)
		} else {
			fmt.Printf("⚠ Background provider %q failed: %v\n", cfg.Background.Provider, err)
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
	} else {
		fmt.Println("✓ Cron service started")
	}

	if err := heartbeatService.Start(); err != nil {
		fmt.Printf("Error starting heartbeat service: %v\n", err)
	} else {
		fmt.Println("✓ Heartbeat service started")
	}

	stateManager := state.NewManager(cfg.WorkspacePath())

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
	if cfg.Providers.LlamaCpp.Enabled {
		if localProv, err := providers.CreateLlamaCppProvider(cfg); err == nil {
			attentionService.SetLocalProvider(localProv)
		}
	}
	go attentionService.Start(ctx)
	fmt.Println("✓ Attention manager started")

	// Email watcher service
	if cfg.Tools.Google.ServiceAccountFile != "" && cfg.Tools.Google.ImpersonateEmail != "" {
		var localProv providers.LLMProvider
		if cfg.Providers.LlamaCpp.Enabled {
			localProv, _ = providers.CreateLlamaCppProvider(cfg)
		}
		emailWatcher := emailwatch.NewService(
			cfg.Tools.Google.ServiceAccountFile,
			cfg.Tools.Google.ImpersonateEmail,
			cfg.WorkspacePath(), stateManager, localProv,
		)
		emailWatcher.SetBus(msgBus)
		go emailWatcher.Start(ctx)
		defer emailWatcher.Stop()
		fmt.Println("✓ Email watcher started")
	}

	// Health check service
	if cfg.Health.Enabled && len(cfg.Health.Endpoints) > 0 {
		healthService := healthcheck.NewService(cfg.Health, cfg.WorkspacePath(), stateManager)
		healthService.SetBus(msgBus)
		go healthService.Start(ctx)
		defer healthService.Stop()
		fmt.Println("✓ Health check service started")
	}

	// Background reasoning service
	var reasoningService *reasoning.Service
	if cfg.Reasoning.Enabled {
		var localProv providers.LLMProvider
		if cfg.Providers.LlamaCpp.Enabled {
			localProv, _ = providers.CreateLlamaCppProvider(cfg)
		}
		reasoningService = reasoning.NewService(
			cfg.Reasoning, cfg.WorkspacePath(), stateManager,
			localProv, agentLoop.GetMemoryTool(), nil,
		)
		reasoningService.SetBus(msgBus)
		reasoningService.SetAgentLoop(agentLoop)
		go reasoningService.Start(ctx)
		defer reasoningService.Stop()
		fmt.Println("✓ Background reasoning service started")
	}

	// RSS reader service
	if cfg.RSS.Enabled && len(cfg.RSS.Feeds) > 0 {
		var localProv providers.LLMProvider
		if cfg.Providers.LlamaCpp.Enabled {
			localProv, _ = providers.CreateLlamaCppProvider(cfg)
		}
		rssService := rsswatch.NewService(cfg.RSS, cfg.WorkspacePath(), stateManager, localProv)
		rssService.SetBus(msgBus)
		go rssService.Start(ctx)
		defer rssService.Stop()
		fmt.Println("✓ RSS reader service started")
	}

	if err := channelManager.StartAll(ctx); err != nil {
		fmt.Printf("Error starting channels: %v\n", err)
	}

	// Health check HTTP server
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		build, goVer := formatBuildInfo()
		status := map[string]interface{}{
			"status":     "ok",
			"version":    formatVersion(),
			"git_commit": gitCommit,
			"build_time": build,
			"go_version": goVer,
			"uptime":     time.Since(startTime).String(),
		}
		if pr, ok := providers.IsPrivacyRouter(provider); ok {
			runtime := pr.RuntimeStatus()
			status["provider"] = map[string]interface{}{
				"router":               "privacy",
				"last_route":           runtime.LastRoute,
				"last_provider":        runtime.LastProvider,
				"last_model":           runtime.LastModel,
				"last_reason":          runtime.LastReason,
				"workload":             runtime.Workload,
				"degraded":             runtime.Degraded,
				"last_error":           runtime.LastError,
				"last_error_class":     runtime.LastErrorClass,
				"last_error_at":        runtime.LastErrorAt,
				"last_success_at":      runtime.LastSuccessAt,
				"consecutive_failures": runtime.ConsecutiveFailures,
				"stats":                pr.Stats(),
			}
			if runtime.Degraded || runtime.ConsecutiveFailures > 0 {
				status["status"] = "degraded"
			}
		}
		if tracker != nil {
			status["telemetry"] = map[string]interface{}{
				"today_tokens": tracker.GetToday(),
				"providers":    tracker.GetTodayProviderSnapshot(),
			}
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
		heartbeatService.SetEventCallback(adminHandler.EmitEvent)
		cronService.SetEventCallback(adminHandler.EmitEvent)
		if reasoningService != nil {
			reasoningService.SetEventCallback(adminHandler.EmitEvent)
		}
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
