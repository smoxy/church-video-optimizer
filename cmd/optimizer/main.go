package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"video-optimizer/internal/app"
	"video-optimizer/internal/config"
	"video-optimizer/internal/logger"
)

func main() {
	logger.Setup()

	cfg, err := config.LoadConfig()
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	slog.Info("Starting Video Optimizer Service", 
		"polling_interval", cfg.PollingInterval,
		"video_codec", cfg.VideoCodec,
	)

	if err := checkWritePermission(cfg.OutputRoot); err != nil {
		slog.Error("Output root is not writable", "path", cfg.OutputRoot, "error", err)
		os.Exit(1)
	}

	svc := app.NewService(cfg)
	svc.Start()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	slog.Info("Received signal, shutting down...", "signal", sig)
	
	svc.Stop()
	slog.Info("Shutdown complete")
}

func checkWritePermission(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			// Try to create it
			return os.MkdirAll(path, 0755)
		}
		return err
	}
	// Try to create a dummy file to verify write access
	testFile := filepath.Join(path, ".write_test")
	f, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("failed to create test file in %s: %w", path, err)
	}
	f.Close()
	os.Remove(testFile)
	
	return nil
}
