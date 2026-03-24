package constants

import "time"

// Network and HTTP timeouts.
const (
	// HTTPClientTimeout is the default timeout for short HTTP requests (APIs, webhooks).
	HTTPClientTimeout = 10 * time.Second

	// HTTPLongTimeout for long-running HTTP requests (browse, image gen, LLM).
	HTTPLongTimeout = 60 * time.Second

	// LLMRequestTimeout for LLM provider HTTP calls.
	LLMRequestTimeout = 120 * time.Second

	// ToolExecutionTimeout is the default timeout for tool execution.
	ToolExecutionTimeout = 120 * time.Second

	// ToolExecutionTimeoutLong for tools that need more time (browse, shell).
	ToolExecutionTimeoutLong = 6 * time.Minute

	// ShellMaxTimeout is the hard cap for user-requested shell timeouts.
	ShellMaxTimeout = 300 * time.Second

	// ShellDefaultTimeout is the default timeout for shell commands.
	ShellDefaultTimeout = 30 * time.Second

	// WebSocketHandshakeTimeout for WS connections.
	WebSocketHandshakeTimeout = 10 * time.Second

	// GracefulShutdownTimeout for HTTP server shutdown.
	GracefulShutdownTimeout = 5 * time.Second
)

// Provider retry configuration.
const (
	// ProviderMaxRetries is the number of retry attempts for LLM providers.
	ProviderMaxRetries = 3
)
