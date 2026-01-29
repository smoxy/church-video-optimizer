package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds the application configuration
type Config struct {
	SourceEndpoint  string
	OutputRoot      string
	PollingInterval int
	VideoCodec      string
	VideoCRF        int
	VideoPreset     string
	AudioCodec      string
	MaxThreads      int
	// Downloader settings
	DownloadConcurrency int
	ChunkSizeMB         int64
}

// LoadConfig reads configuration from environment variables
func LoadConfig() (*Config, error) {
	cfg := &Config{
		SourceEndpoint:  getEnv("SOURCE_ENDPOINT", ""),
		OutputRoot:      getEnv("OUTPUT_ROOT", "/data/output"),
		PollingInterval: getEnvInt("POLLING_INTERVAL", 300),
		VideoCodec:      getEnv("VIDEO_CODEC", "libx265"),
		VideoCRF:        getEnvInt("VIDEO_CRF", 27),
		VideoPreset:         getEnv("VIDEO_PRESET", "medium"),
		AudioCodec:          getEnv("AUDIO_CODEC", "aac"),
		MaxThreads:          getEnvInt("MAX_THREADS", 0),
		DownloadConcurrency: getEnvInt("DOWNLOAD_CONCURRENCY", 4),
		ChunkSizeMB:         int64(getEnvInt("DOWNLOAD_CHUNK_SIZE_MB", 5)),
	}

	if cfg.SourceEndpoint == "" {
		return nil, fmt.Errorf("SOURCE_ENDPOINT environment variable is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return fallback
}
