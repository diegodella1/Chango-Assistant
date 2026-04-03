package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// LlamaCppProvider supports local LLM inference via llama.cpp.
// Two modes:
//   - "server": connects to a running llama-server (OpenAI-compatible API)
//   - "binary": runs llama-cli as a subprocess for each request
type LlamaCppProvider struct {
	cfg        config.LlamaCppConfig
	httpProv   *HTTPProvider // used in server mode
	defaultMdl string
}

// NewLlamaCppProvider creates a provider based on config mode.
func NewLlamaCppProvider(cfg config.LlamaCppConfig) (*LlamaCppProvider, error) {
	p := &LlamaCppProvider{cfg: cfg}

	switch cfg.Mode {
	case "server":
		if cfg.APIBase == "" {
			return nil, fmt.Errorf("llamacpp server mode requires api_base (e.g. http://localhost:8080/v1)")
		}
		p.httpProv = NewHTTPProvider("", cfg.APIBase, "")
		p.defaultMdl = cfg.DefaultModel
		if p.defaultMdl == "" {
			p.defaultMdl = "gemma-4-E2B-it"
		}

	case "binary":
		if cfg.BinaryPath == "" {
			return nil, fmt.Errorf("llamacpp binary mode requires binary_path (path to llama-cli)")
		}
		if cfg.ModelPath == "" {
			return nil, fmt.Errorf("llamacpp binary mode requires model_path (path to .gguf file)")
		}
		// Verify binary exists
		if _, err := exec.LookPath(cfg.BinaryPath); err != nil {
			// Try as absolute path
			if _, err2 := exec.LookPath(cfg.BinaryPath); err2 != nil {
				logger.WarnCF("llamacpp", "Binary not found at path, will try at runtime", map[string]interface{}{
					"path": cfg.BinaryPath,
				})
			}
		}
		p.defaultMdl = "local"

	default:
		return nil, fmt.Errorf("llamacpp: invalid mode %q (use \"server\" or \"binary\")", cfg.Mode)
	}

	return p, nil
}

// Chat implements LLMProvider. Routes to server or binary mode.
func (p *LlamaCppProvider) Chat(ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]interface{}) (*LLMResponse, error) {
	if p.cfg.Mode == "server" {
		return p.chatServer(ctx, messages, tools, model, options)
	}
	return p.chatBinary(ctx, messages, tools, options)
}

// chatServer delegates to the HTTP provider (llama-server is OpenAI-compatible).
func (p *LlamaCppProvider) chatServer(ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]interface{}) (*LLMResponse, error) {
	if model == "" || model == "local" {
		model = p.defaultMdl
	}

	// Cap max_tokens to what the local model can handle.
	maxTok := p.maxTokens()
	if mt, ok := options["max_tokens"].(int); ok && mt > maxTok {
		options["max_tokens"] = maxTok
	} else if !ok {
		options["max_tokens"] = maxTok
	}

	// Truncate messages to fit within context window.
	// Reserve tokens for output. Use a conservative chars/token estimate for local models
	// because the full Chango system prompt can tokenize much denser than plain chat text.
	ctxSize := p.cfg.ContextSize
	if ctxSize <= 0 {
		ctxSize = 2048
	}
	maxPromptChars := (ctxSize - maxTok) * 2
	messages = truncateMessagesForContext(messages, maxPromptChars)
	messages = compactLocalMessages(messages, maxPromptChars)

	resp, err := p.httpProv.Chat(ctx, messages, tools, model, options)
	if err != nil {
		return nil, fmt.Errorf("llamacpp server: %w", err)
	}
	return resp, nil
}

// truncateMessagesForContext keeps the system message + as many recent messages
// as fit within maxChars. Older messages are dropped first.
func truncateMessagesForContext(messages []Message, maxChars int) []Message {
	if maxChars <= 0 || len(messages) == 0 {
		return messages
	}

	// Calculate total chars
	total := 0
	for _, m := range messages {
		total += len(m.Content)
	}
	if total <= maxChars {
		return messages
	}

	// Keep system message (first) + trim from oldest user/assistant messages
	var system []Message
	var rest []Message
	for _, m := range messages {
		if m.Role == "system" {
			system = append(system, m)
		} else {
			rest = append(rest, m)
		}
	}

	// Budget after system messages
	budget := maxChars
	for _, m := range system {
		budget -= len(m.Content)
	}
	if budget <= 0 {
		// System prompt alone exceeds context — truncate it
		if len(system) > 0 {
			system[0].Content = system[0].Content[:maxChars]
		}
		return system
	}

	// Keep recent messages, drop oldest
	var kept []Message
	for i := len(rest) - 1; i >= 0; i-- {
		cost := len(rest[i].Content)
		if budget-cost < 0 {
			break
		}
		budget -= cost
		kept = append([]Message{rest[i]}, kept...)
	}

	return append(system, kept...)
}

// compactLocalMessages applies an extra defensive trim before sending a request to
// llama-server. The Chango system prompt can become very large, so keep only the
// first system message (trimmed) plus a short tail of recent chat turns.
func compactLocalMessages(messages []Message, maxChars int) []Message {
	if len(messages) == 0 || maxChars <= 0 {
		return messages
	}

	systemBudget := maxChars / 3
	if systemBudget > 4000 {
		systemBudget = 4000
	}
	if systemBudget < 1200 {
		systemBudget = 1200
	}

	trimmed := make([]Message, 0, len(messages))
	nonSystem := make([]Message, 0, len(messages))

	for i, msg := range messages {
		if msg.Role == "system" {
			if i == 0 {
				msg.Content = truncateToChars(msg.Content, systemBudget)
				trimmed = append(trimmed, msg)
			}
			continue
		}
		nonSystem = append(nonSystem, msg)
	}

	if len(nonSystem) > 6 {
		nonSystem = nonSystem[len(nonSystem)-6:]
	}

	trimmed = append(trimmed, nonSystem...)
	return truncateMessagesForContext(trimmed, maxChars)
}

func truncateToChars(s string, maxChars int) string {
	if maxChars <= 0 || len(s) <= maxChars {
		return s
	}
	return s[:maxChars] + "\n\n[... contexto local recortado ...]"
}

// chatBinary runs llama-cli as a subprocess with a model-appropriate prompt format.
func (p *LlamaCppProvider) chatBinary(ctx context.Context, messages []Message, tools []ToolDefinition, options map[string]interface{}) (*LLMResponse, error) {
	prompt := buildLocalPrompt(messages, p.cfg.ModelPath)

	maxTok := p.maxTokens()
	if mt, ok := options["max_tokens"].(int); ok && mt > 0 {
		maxTok = mt
	}

	temp := p.cfg.Temperature
	if temp <= 0 {
		if t, ok := options["temperature"].(float64); ok && t > 0 {
			temp = t
		} else {
			temp = 0.7
		}
	}

	threads := p.cfg.Threads
	if threads <= 0 {
		threads = 4 // Pi 5 has 4 cores
	}

	ctxSize := p.cfg.ContextSize
	if ctxSize <= 0 {
		ctxSize = 2048
	}

	args := []string{
		"-m", p.cfg.ModelPath,
		"-p", prompt,
		"-n", fmt.Sprintf("%d", maxTok),
		"-t", fmt.Sprintf("%d", threads),
		"-c", fmt.Sprintf("%d", ctxSize),
		"--temp", fmt.Sprintf("%.2f", temp),
		"--no-display-prompt",
		"--log-disable",
	}

	if p.cfg.GPULayers > 0 {
		args = append(args, "-ngl", fmt.Sprintf("%d", p.cfg.GPULayers))
	}

	// Binary mode timeout: generous for Pi 5 (small models ~10-60s)
	timeout := constants.LLMRequestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	binCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logger.InfoCF("llamacpp", "Running inference", map[string]interface{}{
		"model":    p.cfg.ModelPath,
		"tokens":   maxTok,
		"threads":  threads,
		"ctx_size": ctxSize,
	})

	cmd := exec.CommandContext(binCtx, p.cfg.BinaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if binCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("llamacpp binary timed out after %v", timeout)
		}
		return nil, fmt.Errorf("llamacpp binary failed: %w\nstderr: %s", err, errMsg)
	}
	elapsed := time.Since(start)

	output := strings.TrimSpace(stdout.String())

	// Strip any trailing <|im_end|> or <|endoftext|> tokens
	output = stripSpecialTokens(output)

	logger.InfoCF("llamacpp", "Inference complete", map[string]interface{}{
		"elapsed":      elapsed.String(),
		"output_len":   len(output),
		"output_runes": len([]rune(output)),
	})

	return &LLMResponse{
		Content:      output,
		FinishReason: "stop",
		Usage: &UsageInfo{
			CompletionTokens: estimateTokens(output),
			TotalTokens:      estimateTokens(output),
		},
	}, nil
}

// GetDefaultModel implements LLMProvider.
func (p *LlamaCppProvider) GetDefaultModel() string {
	return p.defaultMdl
}

// Ping checks if the llama-server is reachable (server mode only).
func (p *LlamaCppProvider) Ping(ctx context.Context) error {
	if p.cfg.Mode != "server" {
		// Binary mode: check that file exists
		if _, err := exec.LookPath(p.cfg.BinaryPath); err != nil {
			return fmt.Errorf("llama-cli binary not found: %s", p.cfg.BinaryPath)
		}
		return nil
	}

	// Server mode: hit /health endpoint
	healthURL := strings.TrimSuffix(p.cfg.APIBase, "/v1") + "/health"
	resp, err := p.httpProv.httpClient.Get(healthURL)
	if err != nil {
		return fmt.Errorf("llamacpp server unreachable at %s: %w", healthURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("llamacpp server unhealthy (status %d)", resp.StatusCode)
	}
	return nil
}

// buildLocalPrompt converts messages to the prompt format expected by the local model.
func buildLocalPrompt(messages []Message, modelHint string) string {
	if usesGemmaPrompt(modelHint) {
		return buildGemmaPrompt(messages)
	}
	return buildChatMLPrompt(messages)
}

func usesGemmaPrompt(modelHint string) bool {
	lower := strings.ToLower(strings.TrimSpace(modelHint))
	return strings.Contains(lower, "gemma")
}

// buildChatMLPrompt converts messages to ChatML format.
func buildChatMLPrompt(messages []Message) string {
	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString("<|im_start|>")
		sb.WriteString(msg.Role)
		sb.WriteString("\n")

		content := msg.Content
		if content == "" && len(msg.Parts) > 0 {
			// Extract text from multimodal parts (skip images for local model)
			for _, part := range msg.Parts {
				if part.Type == "text" && part.Text != "" {
					content += part.Text + "\n"
				}
			}
			content = strings.TrimSpace(content)
		}

		// Append tool call results
		if msg.ToolCallID != "" {
			content = fmt.Sprintf("[Tool Result %s]: %s", msg.ToolCallID, content)
		}

		sb.WriteString(content)
		sb.WriteString("<|im_end|>\n")
	}

	// Prompt the assistant to respond
	sb.WriteString("<|im_start|>assistant\n")
	return sb.String()
}

// buildGemmaPrompt converts messages to Gemma instruction format.
func buildGemmaPrompt(messages []Message) string {
	var sb strings.Builder
	for _, msg := range messages {
		role := "user"
		if msg.Role == "assistant" {
			role = "model"
		}

		content := msg.Content
		if content == "" && len(msg.Parts) > 0 {
			for _, part := range msg.Parts {
				if part.Type == "text" && part.Text != "" {
					content += part.Text + "\n"
				}
			}
			content = strings.TrimSpace(content)
		}

		if msg.ToolCallID != "" {
			content = fmt.Sprintf("[Tool Result %s]: %s", msg.ToolCallID, content)
		}

		if msg.Role == "system" {
			content = "System instruction:\n" + content
		}

		sb.WriteString("<start_of_turn>")
		sb.WriteString(role)
		sb.WriteString("\n")
		sb.WriteString(content)
		sb.WriteString("<end_of_turn>\n")
	}

	sb.WriteString("<start_of_turn>model\n")
	return sb.String()
}

// stripSpecialTokens removes llama.cpp special tokens from output.
func stripSpecialTokens(s string) string {
	for _, tok := range []string{"<|im_end|>", "<|endoftext|>", "<|im_start|>", "</s>"} {
		s = strings.ReplaceAll(s, tok, "")
	}
	return strings.TrimSpace(s)
}

// estimateTokens gives a rough token count (~4 chars per token for English/Spanish).
func estimateTokens(s string) int {
	n := len([]rune(s)) / 3
	if n == 0 && len(s) > 0 {
		n = 1
	}
	return n
}

// maxTokens returns the configured max or a sensible default for small models.
func (p *LlamaCppProvider) maxTokens() int {
	if p.cfg.MaxTokens > 0 {
		return p.cfg.MaxTokens
	}
	return 512
}

// CreateLlamaCppProvider is the factory used by CreateProvider.
func CreateLlamaCppProvider(cfg *config.Config) (LLMProvider, error) {
	lcfg := cfg.Providers.LlamaCpp
	if !lcfg.Enabled {
		return nil, fmt.Errorf("llamacpp provider is not enabled")
	}

	provider, err := NewLlamaCppProvider(lcfg)
	if err != nil {
		return nil, err
	}

	// Optional: ping on creation to catch misconfig early
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.Ping(ctx); err != nil {
		logger.WarnCF("llamacpp", "Provider created but health check failed", map[string]interface{}{
			"error": err.Error(),
			"mode":  lcfg.Mode,
		})
	}

	return provider, nil
}

// LlamaCppModelInfo returns info about recommended models for display.
func LlamaCppModelInfo() []map[string]string {
	return []map[string]string{
		{
			"name": "Gemma 4 E2B Instruct",
			"file": "gemma-4-e2b-it-Q8_0.gguf",
			"size": "~5.0 GB",
			"use":  "Local inner monologue, privacy routing, lightweight reasoning",
			"url":  "huggingface.co/ggml-org/gemma-4-E2B-it-GGUF",
		},
	}
}

// --- Fallback support ---

// contextKey is an unexported type for context keys to avoid collisions.
type contextKey string

const fallbackDepthKey contextKey = "fallback_depth"

// FallbackProvider wraps a primary and local fallback provider.
// If the primary fails (network error, timeout), it falls back to local.
// A depth counter in the context prevents recursive fallback chains.
type FallbackProvider struct {
	Primary  LLMProvider
	Fallback LLMProvider
}

func NewFallbackProvider(primary, fallback LLMProvider) *FallbackProvider {
	return &FallbackProvider{Primary: primary, Fallback: fallback}
}

func (f *FallbackProvider) Chat(ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]interface{}) (*LLMResponse, error) {
	// Check fallback depth to prevent recursive nesting
	depth := 0
	if d, ok := ctx.Value(fallbackDepthKey).(int); ok {
		depth = d
	}

	// Vision turns must not fall back to a blind local model.
	if HasVisionInput(messages) {
		return f.Primary.Chat(ctx, messages, tools, model, options)
	}

	resp, err := f.Primary.Chat(ctx, messages, tools, model, options)
	if err == nil {
		return resp, nil
	}

	// If we're already in a nested fallback, don't go deeper
	if depth > 1 {
		logger.WarnCF("fallback", "Max fallback depth reached, returning primary error", map[string]interface{}{
			"depth": depth,
			"error": err.Error(),
		})
		return nil, err
	}

	// Only fallback on network/server errors and rate limits, not on bad requests
	errStr := err.Error()
	isRetryable := strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "status 5") ||
		strings.Contains(errStr, "status 429") ||
		strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "usagelimitreached") ||
		strings.Contains(errStr, "rate") ||
		strings.Contains(errStr, "exceed") // context size exceeded → try cloud

	if !isRetryable {
		return nil, err
	}

	logger.WarnCF("fallback", "Primary provider failed, falling back to local", map[string]interface{}{
		"error": errStr,
		"depth": depth,
	})

	// Increment depth in context before calling fallback
	fbCtx := context.WithValue(ctx, fallbackDepthKey, depth+1)

	// Strip tools for small local models (unreliable tool calling).
	// If local model is still loading (503), retry with patience — it needs time to init.
	resp, err = f.Fallback.Chat(fbCtx, messages, nil, "", options)
	if err != nil && strings.Contains(err.Error(), "Loading model") {
		logger.InfoCF("fallback", "Local model is loading, waiting for it to be ready...", nil)
		for attempt := 0; attempt < 4; attempt++ {
			wait := time.Duration(5*(attempt+1)) * time.Second // 5s, 10s, 15s, 20s
			select {
			case <-fbCtx.Done():
				return nil, fbCtx.Err()
			case <-time.After(wait):
			}
			resp, err = f.Fallback.Chat(fbCtx, messages, nil, "", options)
			if err == nil {
				return resp, nil
			}
			if !strings.Contains(err.Error(), "Loading model") {
				break
			}
			logger.InfoCF("fallback", "Local model still loading...", map[string]interface{}{
				"attempt": attempt + 2,
				"wait":    wait.String(),
			})
		}
	}
	return resp, err
}

func (f *FallbackProvider) GetDefaultModel() string {
	return f.Primary.GetDefaultModel()
}

// MarshalJSON prevents logging sensitive data.
func (p *LlamaCppProvider) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"type": "llamacpp",
		"mode": p.cfg.Mode,
	})
}
