package lcdata

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger creates a slog.Logger configured for the given level string.
// level: "debug", "info", "warn", "error" (default: "info")
// Format is JSON for easy parsing by log aggregators.
func NewLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
