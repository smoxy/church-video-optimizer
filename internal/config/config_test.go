package config

import (
	"os"
	"testing"
)

// envKeys lists every environment variable read by LoadConfig.
var envKeys = []string{
	"SOURCE_ENDPOINT",
	"OUTPUT_ROOT",
	"POLLING_INTERVAL",
	"VIDEO_CODEC",
	"VIDEO_CRF",
	"VIDEO_PRESET",
	"AUDIO_CODEC",
	"MAX_THREADS",
	"DOWNLOAD_CONCURRENCY",
	"DOWNLOAD_CHUNK_SIZE_MB",
}

// clearEnv unsets every LoadConfig-relevant env var and restores the previous
// values on test cleanup, so results don't depend on the ambient environment.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range envKeys {
		old, ok := os.LookupEnv(k)
		os.Unsetenv(k)
		if ok {
			t.Cleanup(func() { os.Setenv(k, old) })
		}
	}
}

func TestLoadConfig_RequiresSourceEndpoint(t *testing.T) {
	clearEnv(t)

	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected error when SOURCE_ENDPOINT is unset, got nil")
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("SOURCE_ENDPOINT", "https://example.com/api/files")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := Config{
		SourceEndpoint:      "https://example.com/api/files",
		OutputRoot:          "/data/output",
		PollingInterval:     300,
		VideoCodec:          "libx265",
		VideoCRF:            27,
		VideoPreset:         "medium",
		AudioCodec:          "aac",
		MaxThreads:          0,
		DownloadConcurrency: 4,
		ChunkSizeMB:         5,
	}

	if *cfg != want {
		t.Fatalf("defaults mismatch:\n got:  %+v\n want: %+v", *cfg, want)
	}
}

func TestLoadConfig_ParsesEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("SOURCE_ENDPOINT", "https://example.com/api/files")
	t.Setenv("OUTPUT_ROOT", "/tmp/out")
	t.Setenv("POLLING_INTERVAL", "60")
	t.Setenv("VIDEO_CODEC", "libsvtav1")
	t.Setenv("VIDEO_CRF", "30")
	t.Setenv("VIDEO_PRESET", "fast")
	t.Setenv("AUDIO_CODEC", "opus")
	t.Setenv("MAX_THREADS", "4")
	t.Setenv("DOWNLOAD_CONCURRENCY", "8")
	t.Setenv("DOWNLOAD_CHUNK_SIZE_MB", "10")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := Config{
		SourceEndpoint:      "https://example.com/api/files",
		OutputRoot:          "/tmp/out",
		PollingInterval:     60,
		VideoCodec:          "libsvtav1",
		VideoCRF:            30,
		VideoPreset:         "fast",
		AudioCodec:          "opus",
		MaxThreads:          4,
		DownloadConcurrency: 8,
		ChunkSizeMB:         10,
	}

	if *cfg != want {
		t.Fatalf("overrides mismatch:\n got:  %+v\n want: %+v", *cfg, want)
	}
}

func TestLoadConfig_InvalidIntFallsBackToDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv("SOURCE_ENDPOINT", "https://example.com/api/files")
	t.Setenv("POLLING_INTERVAL", "not-a-number")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PollingInterval != 300 {
		t.Fatalf("expected fallback 300 for invalid POLLING_INTERVAL, got %d", cfg.PollingInterval)
	}
}
