package app

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type History struct {
	mu        sync.RWMutex
	Processed map[string]time.Time `json:"processed"` // Filename -> ProcessedAt
	filePath  string
}

func NewHistory(path string) (*History, error) {
	h := &History{
		Processed: make(map[string]time.Time),
		filePath:  path,
	}

	// Load existing
	if _, err := os.Stat(path); err == nil {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(content) > 0 {
			if err := json.Unmarshal(content, &h); err != nil {
				return nil, err
			}
		}
	}

	// Auto-clean on startup
	h.CleanOldEntries(12)

	return h, nil
}

func (h *History) Has(filename string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, exists := h.Processed[filename]
	return exists
}

func (h *History) Add(filename string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.Processed[filename] = time.Now()
	return h.save() // Save immediately
}

func (h *History) CleanOldEntries(retentionDays int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	modified := false

	for filename, timestamp := range h.Processed {
		if timestamp.Before(cutoff) {
			delete(h.Processed, filename)
			modified = true
		}
	}

	if modified {
		h.save()
	}
}

func (h *History) save() error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write approach could be better, but simple write is fine for now
	return os.WriteFile(h.filePath, data, 0644)
}
