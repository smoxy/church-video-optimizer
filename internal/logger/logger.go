package logger

import (
	"log/slog"
	"os"
)

// Setup initializes the global logger
func Setup() {
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, opts))
	slog.SetDefault(logger)
}
