package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"video-optimizer/internal/config"
	"video-optimizer/internal/extractor"
	"video-optimizer/internal/fetcher"
	"video-optimizer/internal/processor"
)

type Job struct {
	SourceFile string // The original filename from API (e.g. archive.zip)
	LocalPath  string // The path to the video file to process (e.g. /tmp/video.mp4)
}

type Service struct {
	cfg        *config.Config
	queue      chan Job
	fetcher    *fetcher.Fetcher
	history    *History        // Persistent: processed.log
	downloaded map[string]bool // Volatile: Session cache
	mu         sync.RWMutex    // Protects downloaded map
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
}

func NewService(cfg *config.Config) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	
	// History file in output root (Level 3: Persistent)
	histPath := filepath.Join(cfg.OutputRoot, "processed.json")
	hist, err := NewHistory(histPath)
	if err != nil {
		slog.Error("Failed to initialize history", "error", err)
		hist = &History{Processed: make(map[string]time.Time)}
	}

	// Cleanup old output directories (older than 14 days)
	// Running async to not block startup
	go CleanupOldOutputDirs(cfg.OutputRoot, 14)

	return &Service{
		cfg:        cfg,
		queue:      make(chan Job, 100),
		fetcher:    fetcher.New(cfg.SourceEndpoint, cfg.DownloadConcurrency, cfg.ChunkSizeMB),
		history:    hist,
		downloaded: make(map[string]bool),
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (s *Service) Start() {
	// Start Worker
	s.wg.Add(1)
	go s.worker()

	// Start Poller
	s.wg.Add(1)
	go s.poller()
}

func (s *Service) Stop() {
	slog.Info("Stopping service...")
	s.cancel() // Signal cancellation
	s.wg.Wait() // Wait for goroutines to finish
	slog.Info("Service stopped gracefully")
}

func (s *Service) poller() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Duration(s.cfg.PollingInterval) * time.Second)
	defer ticker.Stop()

	// Run immediately once
	s.poll()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.poll()
		}
	}
}

func (s *Service) poll() {
	slog.Info("Polling for new files...")
	items, err := s.fetcher.Poll()
	if err != nil {
		slog.Error("Polling failed", "error", err)
		return
	}

	for _, item := range items {
		// Basic filter
		ext := strings.ToLower(filepath.Ext(item.Filename))
		if ext != ".zip" && ext != ".mkv" && ext != ".mp4" {
			continue
		}

		// Level 3 Check: Persistent History (processed.log)
		if s.history.Has(item.Filename) {
			continue
		}

		// Level 1 Check: Volatile Session Cache
		s.mu.RLock()
		alreadyDownloaded := s.downloaded[item.Filename]
		s.mu.RUnlock()
		if alreadyDownloaded {
			continue
		}

		// Level 2 Check: Physical presence in /tmp/ingest
		tmpDir := "/tmp/ingest"
		os.MkdirAll(tmpDir, 0755)
		localPath := filepath.Join(tmpDir, item.Filename)
		
		if _, err := os.Stat(localPath); err == nil {
			slog.Info("File present in ingest (skipping download)", "file", item.Filename)
			// Mark as downloaded in session to skip next poll check faster
			s.mu.Lock()
			s.downloaded[item.Filename] = true
			s.mu.Unlock()
			continue 
		}

		slog.Info("Downloading file", "url", item.URL)
		if err := s.fetcher.Download(item.URL, localPath); err != nil {
			slog.Error("Download failed", "file", item.Filename, "error", err)
			continue
		}

		// Mark Level 1: Downloaded
		s.mu.Lock()
		s.downloaded[item.Filename] = true
		s.mu.Unlock()

		// Handle Zip
		if ext == ".zip" {
			slog.Info("Extracting zip", "file", localPath)
			files, err := extractor.ExtractAndClean(localPath, tmpDir)
			if err != nil {
				slog.Error("Extraction failed", "file", localPath, "error", err)
				os.Remove(localPath) 
				continue
			}
			for _, f := range files {
				if isVideo(f) {
					// Queue with SourceFile metadata
					s.queue <- Job{SourceFile: item.Filename, LocalPath: f}
				}
			}
			// Note: We do NOT mark history here anymore. We wait for processing.
		} else {
			s.queue <- Job{SourceFile: item.Filename, LocalPath: localPath}
		}
	}
}

func (s *Service) worker() {
	defer s.wg.Done()
	
	for {
		select {
		case <-s.ctx.Done():
			return
		case job := <-s.queue:
			s.processItem(job)
		}
	}
}

func (s *Service) processItem(job Job) {
	src := job.LocalPath
	slog.Info("Processing file", "path", src)

	// Determine output path: Root / Year-Wxx / filename
	year, week := time.Now().ISOWeek()
	folderName := fmt.Sprintf("%d-W%02d", year, week)
	outDir := filepath.Join(s.cfg.OutputRoot, folderName)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		slog.Error("Failed to create output directory", "dir", outDir, "error", err)
		return
	}

	filename := filepath.Base(src)
	dest := filepath.Join(outDir, filename)

	params := processor.EncodeParams{
		VideoCodec:  s.cfg.VideoCodec,
		VideoCRF:    s.cfg.VideoCRF,
		VideoPreset: s.cfg.VideoPreset,
		AudioCodec:  s.cfg.AudioCodec,
		MaxThreads:  s.cfg.MaxThreads,
	}

	err := processor.ProcessVideo(s.ctx, src, dest, params)
	if err != nil {
		slog.Error("Processing failed", "file", src, "error", err)
		
		// CRITICAL: Remove from session cache so it gets picked up by next poll
		s.mu.Lock()
		delete(s.downloaded, job.SourceFile)
		s.mu.Unlock()

		// Move to failed?
		failedDir := filepath.Join(s.cfg.OutputRoot, "failed")
		os.MkdirAll(failedDir, 0755)
		os.Rename(src, filepath.Join(failedDir, filepath.Base(src)))
		
		// Cleanup partial dest if exists
		os.Remove(dest)
		return
	}

	slog.Info("Processing completed", "file", dest)
	
	// Enforce default permissions (644) for web serving
	if err := os.Chmod(dest, 0644); err != nil {
		slog.Warn("Failed to set file permissions", "file", dest, "error", err)
	}

	// Cleanup source
	os.Remove(src)

	// Level 3 Update: Mark original source file as totally done
	s.history.Add(job.SourceFile)
}

func isVideo(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".mkv" || ext == ".mp4"
}
