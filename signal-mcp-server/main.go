package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const serverName = "signal-mcp-server"
const serverVersion = "0.2.0"

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger, closeLog, err := setupLogger(cfg.LogLevel, cfg.LogFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logging setup error: %v\n", err)
		os.Exit(1)
	}
	defer closeLog()

	addr := cfg.HTTPHost + ":" + cfg.HTTPPort
	logger.Info("starting signal-mcp-server",
		"version", serverVersion,
		"signal_api_base_url", cfg.SignalAPIBaseURL,
		"signal_number", maskNumber(cfg.SignalNumber),
		"log_level", cfg.LogLevel,
		"http_addr", addr,
		"http_path", cfg.HTTPPath,
		"auth_enabled", cfg.MCPAuthToken != "",
		"require_session", cfg.RequireSession,
	)

	client := NewSignalClient(cfg.SignalAPIBaseURL, cfg.SignalNumber, cfg.SignalAPIToken, cfg.HTTPTimeout, logger)

	mcpSrv := NewMCPServer(serverName, serverVersion, logger)
	RegisterTools(mcpSrv, client, logger)

	transport := NewHTTPTransport(mcpSrv, logger, cfg)

	httpSrv := &http.Server{
		Addr:    addr,
		Handler: transport.Mux(),
		// Deliberately no ReadTimeout/WriteTimeout: receive_message can
		// legitimately long-poll for up to a few minutes. ReadHeaderTimeout
		// alone is enough to guard against slow-header DoS attempts.
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("mcp http server listening", "addr", addr, "path", cfg.HTTPPath, "health_path", "/healthz")
		serveErr <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server exited with error", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("error during graceful shutdown", "error", err)
		}
	}

	logger.Info("server shut down cleanly")
}

// maskNumber avoids writing the full phone number to logs.
func maskNumber(n string) string {
	n = strings.TrimSpace(n)
	if len(n) <= 5 {
		return strings.Repeat("*", len(n))
	}
	return n[:3] + strings.Repeat("*", len(n)-5) + n[len(n)-2:]
}
