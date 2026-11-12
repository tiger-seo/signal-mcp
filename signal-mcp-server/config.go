package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration, sourced from environment variables
// so the binary can be dropped straight into a container/systemd unit/MCP
// client config without any flags.
type Config struct {
	// SignalAPIBaseURL is the base URL of a running signal-cli-rest-api instance.
	SignalAPIBaseURL string
	// SignalNumber is the registered/linked Signal phone number used as the
	// sender for outgoing messages and as the account polled for incoming ones.
	SignalNumber string
	// SignalAPIToken, if set, is sent as a Bearer token (useful if signal-cli-rest-api
	// sits behind an authenticating reverse proxy; the API itself has no built-in auth).
	SignalAPIToken string
	// HTTPTimeout bounds ordinary (non-receive) calls to signal-cli-rest-api.
	HTTPTimeout time.Duration

	// LogLevel: debug | info | warn | error
	LogLevel string
	// LogFile, if set, is an additional destination for logs (stderr is always used too).
	LogFile string

	// --- MCP HTTP transport settings ---

	// HTTPHost/HTTPPort is the address this MCP server itself listens on.
	HTTPHost string
	HTTPPort string
	// HTTPPath is the single MCP endpoint path (default "/mcp").
	HTTPPath string
	// MCPAuthToken, if set, requires "Authorization: Bearer <token>" on every
	// request to this server. Strongly recommended if binding beyond localhost.
	MCPAuthToken string
	// AllowedOrigins is a comma-separated Origin allow-list (protects against
	// DNS-rebinding style attacks from browser-based clients). Empty = allow any.
	AllowedOrigins string
	// RequireSession enforces the Mcp-Session-Id handshake after "initialize".
	RequireSession bool
	// MaxBodyBytes caps request body size.
	MaxBodyBytes int64
}

func loadConfig() (*Config, error) {
	cfg := &Config{
		SignalAPIBaseURL: getEnvDefault("SIGNAL_API_BASE_URL", "http://localhost:8080"),
		SignalNumber:     os.Getenv("SIGNAL_NUMBER"),
		SignalAPIToken:   os.Getenv("SIGNAL_API_TOKEN"),
		LogLevel:         getEnvDefault("LOG_LEVEL", "info"),
		LogFile:          os.Getenv("LOG_FILE"),
		HTTPHost:         getEnvDefault("MCP_HTTP_HOST", "127.0.0.1"),
		HTTPPort:         getEnvDefault("MCP_HTTP_PORT", "8081"),
		HTTPPath:         getEnvDefault("MCP_HTTP_PATH", "/mcp"),
		MCPAuthToken:     os.Getenv("MCP_AUTH_TOKEN"),
		AllowedOrigins:   os.Getenv("MCP_ALLOWED_ORIGINS"),
	}

	if cfg.SignalNumber == "" {
		return nil, errors.New("SIGNAL_NUMBER environment variable is required " +
			"(the registered/linked Signal number used to send/receive messages, e.g. +15551234567)")
	}

	timeoutRaw := getEnvDefault("SIGNAL_HTTP_TIMEOUT_SECONDS", "15")
	seconds, err := strconv.Atoi(timeoutRaw)
	if err != nil || seconds <= 0 {
		return nil, fmt.Errorf("invalid SIGNAL_HTTP_TIMEOUT_SECONDS value %q: must be a positive integer", timeoutRaw)
	}
	cfg.HTTPTimeout = time.Duration(seconds) * time.Second

	requireSessionRaw := getEnvDefault("MCP_REQUIRE_SESSION", "true")
	requireSession, err := strconv.ParseBool(requireSessionRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid MCP_REQUIRE_SESSION value %q: must be true or false", requireSessionRaw)
	}
	cfg.RequireSession = requireSession

	maxBodyRaw := getEnvDefault("MCP_MAX_BODY_BYTES", "4194304") // 4 MiB
	maxBody, err := strconv.ParseInt(maxBodyRaw, 10, 64)
	if err != nil || maxBody <= 0 {
		return nil, fmt.Errorf("invalid MCP_MAX_BODY_BYTES value %q: must be a positive integer", maxBodyRaw)
	}
	cfg.MaxBodyBytes = maxBody

	if cfg.HTTPPath == "" || cfg.HTTPPath[0] != '/' {
		return nil, fmt.Errorf("MCP_HTTP_PATH must start with '/', got %q", cfg.HTTPPath)
	}

	return cfg, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
