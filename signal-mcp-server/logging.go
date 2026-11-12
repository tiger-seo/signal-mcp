package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// setupLogger builds a structured (JSON) logger. Crucially, it NEVER writes to
// stdout: stdout is reserved exclusively for MCP JSON-RPC protocol messages.
// All logs go to stderr, and optionally also to a file for later inspection.
func setupLogger(level string, logFile string) (*slog.Logger, func() error, error) {
	lvl := parseLevel(level)

	writers := []io.Writer{os.Stderr}
	closer := func() error { return nil }

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, nil, fmt.Errorf("opening log file %q: %w", logFile, err)
		}
		writers = append(writers, f)
		closer = f.Close
	}

	out := io.MultiWriter(writers...)
	handler := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level:     lvl,
		AddSource: true,
	})

	return slog.New(handler), closer, nil
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
