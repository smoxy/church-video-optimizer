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
	"video-optimizer/internal/naming"
	"video-optimizer/internal/processor"
	"video-optimizer/internal/sidecar"
)

// Job is a single video to encode, carrying the provenance of the resource
// it came from (adr-0008 "matching per provenienza"). A zip resource with
// multiple videos produces one Job per video, all sharing the same
// SourceFile/ResourceID/Category/Title/SourceURL but with a distinct
// Index/OriginalFilename.
type Job struct {
	SourceFile string // Original top-level downloaded filename (zip or video); dedup/history key.
	LocalPath  string // Local path to the video file to process (e.g. /tmp/ingest/video.mp4).

	ResourceID       int    // Resource id from the source API (sidecar resource_id).
	Category         string // Resource category from the source API (sidecar category, artifact prefix).
	Title            string // Resource title from the source API; slug fallback when the filename yields none.
	SourceURL        string // Resource download_url (sidecar source_url).
	OriginalFilename string // Name of this specific video: the zip entry name, or the downloaded filename for a direct video (sidecar original_filename, slug source).
	Index            int    // 1-based progressive index of this video within its resource (artifact naming "n").
	WeekDate         string // Content week ("YYYY-MM-DD", source API week_date): drives the output week folder; empty on older servers → processing-week fallback.
}

type Service struct {
	cfg        *config.Config
	queue      chan Job
	fetcher    *fetcher.Fetcher
	encoder    processor.Encoder
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
		encoder:    processor.FFmpegEncoder{},
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

		// Build one Job per video this resource produces (adr-0008): the
		// file itself if it's a direct video, or one per video extracted
		// from a zip, each inheriting the resource's id/category/title.
		// Note: we do NOT mark history for the "has videos" case here
		// anymore. We wait for processing.
		jobs, err := jobsForItem(item, localPath, tmpDir)
		if err != nil {
			slog.Error("Extraction failed", "file", localPath, "error", err)
			os.Remove(localPath)
			continue
		}
		if len(jobs) == 0 {
			// Non-zip items with no video jobs can't happen (filtered by
			// the extension check above); a zip with no video entries logs
			// its own WARNING in jobsForItem. Either way there is nothing
			// to encode: mark it done so it isn't re-downloaded forever.
			s.history.Add(item.Filename)
			continue
		}
		for _, job := range jobs {
			s.queue <- job
		}
	}
}

// jobsForItem turns a downloaded resource (item, already saved at
// localPath) into the Jobs it produces. A zip is extracted into extractDir
// and yields one Job per video file found ANYWHERE in the extracted tree
// (extractor.ExtractAndClean walks every entry regardless of nesting depth
// — a zip has no top level to stop at — so a video several subfolders deep
// is found exactly like a top-level one; categorie-video-non-mappate
// backlog, owner decision 2026-07-02: any category/any depth can carry
// video, so there is no category filter here either), in a 1-based
// progressive Index order that is stable/reproducible because
// ExtractAndClean returns paths sorted, not in the archive's own
// (unspecified) entry order. Non-video zip entries are ignored, including
// nested zip entries (zip-in-zip is not supported: ExtractAndClean already
// skips and logs those, so they never reach files here). A direct video
// file yields exactly one Job with Index 1. Every Job inherits item's
// ResourceID/Category/Title/URL (adr-0008).
func jobsForItem(item fetcher.FileItem, localPath, extractDir string) ([]Job, error) {
	ext := strings.ToLower(filepath.Ext(item.Filename))
	if ext != ".zip" {
		return []Job{newJob(item, localPath, item.Filename, 1)}, nil
	}

	slog.Info("Extracting zip", "file", localPath)
	files, err := extractor.ExtractAndClean(localPath, extractDir)
	if err != nil {
		return nil, err
	}

	var jobs []Job
	n := 1
	for _, f := range files {
		if !isVideo(f) {
			continue // File non-video: ignorato.
		}
		jobs = append(jobs, newJob(item, f, filepath.Base(f), n))
		n++
	}
	if len(jobs) == 0 {
		slog.Warn("Zip contains no video files, skipping", "file", item.Filename)
	}
	return jobs, nil
}

func newJob(item fetcher.FileItem, localPath, originalFilename string, index int) Job {
	return Job{
		SourceFile:       item.Filename,
		LocalPath:        localPath,
		ResourceID:       item.ResourceID,
		Category:         item.Category,
		Title:            item.Title,
		SourceURL:        item.URL,
		OriginalFilename: originalFilename,
		Index:            index,
		WeekDate:         item.WeekDate,
	}
}

// weekFolderName returns the "YYYY-Www" output folder for a job
// (contract-video-volume): the ISO week of the CONTENT when weekDate (the
// source API's additive `week_date` field, "YYYY-MM-DD") is present and
// parsable, otherwise the ISO week of now (processing time). The fallback
// keeps the rollout safe against source APIs that don't emit `week_date`
// yet (older mail-parser): empty/invalid input degrades to the previous
// behavior instead of failing. The boolean reports whether the fallback
// was used, so the caller can log it explicitly.
func weekFolderName(weekDate string, now time.Time) (string, bool) {
	if weekDate != "" {
		if d, err := time.Parse("2006-01-02", weekDate); err == nil {
			year, week := d.ISOWeek()
			return fmt.Sprintf("%d-W%02d", year, week), false
		}
	}
	year, week := now.ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week), true
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
	slog.Info("Processing file", "path", src, "resource_id", job.ResourceID, "category", job.Category, "index", job.Index)

	// Determine output path: Root / Year-Wxx / filename. The week is the
	// CONTENT's week (job.WeekDate from the source API), not the processing
	// week — falling back to time.Now() only when week_date is absent
	// (older mail-parser) or unparsable (contract-video-volume).
	folderName, usedFallback := weekFolderName(job.WeekDate, time.Now())
	if usedFallback {
		if job.WeekDate == "" {
			slog.Info("week_date assente dal payload: fallback alla settimana di elaborazione",
				"folder", folderName, "resource_id", job.ResourceID)
		} else {
			slog.Warn("week_date non parsabile (atteso YYYY-MM-DD): fallback alla settimana di elaborazione",
				"week_date", job.WeekDate, "folder", folderName, "resource_id", job.ResourceID)
		}
	}
	outDir := filepath.Join(s.cfg.OutputRoot, folderName)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		slog.Error("Failed to create output directory", "dir", outDir, "error", err)
		return
	}

	// Canonical artifact name (adr-0008): {category}_{resource_id}_{n}_{slug}.mp4
	slug := naming.DeriveSlug(job.OriginalFilename, job.Title)
	filename := naming.ArtifactFilename(job.Category, job.ResourceID, job.Index, slug)
	dest := filepath.Join(outDir, filename)

	params := processor.EncodeParams{
		VideoCodec:  s.cfg.VideoCodec,
		VideoCRF:    s.cfg.VideoCRF,
		VideoPreset: s.cfg.VideoPreset,
		AudioCodec:  s.cfg.AudioCodec,
		MaxThreads:  s.cfg.MaxThreads,
	}

	if err := s.encoder.Encode(s.ctx, src, dest, params); err != nil {
		slog.Error("Processing failed", "file", src, "error", err)
		s.failJob(job, dest)
		return
	}

	// Enforce default permissions (644) for web serving
	if err := os.Chmod(dest, 0644); err != nil {
		slog.Warn("Failed to set file permissions", "file", dest, "error", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		slog.Error("Failed to stat encoded artifact, aborting", "file", dest, "error", err)
		s.failJob(job, dest)
		return
	}

	// Sidecar (adr-0008 / contract-video-volume): written atomically, only
	// after encoding succeeded. The sidecar is the source of truth for
	// mail-parser's resource_id matching, so treat a failure to write it as
	// a failed job too rather than leaving an unmatched artifact behind.
	meta := sidecar.Meta{
		ResourceID:       job.ResourceID,
		Category:         job.Category,
		SourceURL:        job.SourceURL,
		OriginalFilename: job.OriginalFilename,
		Artifact:         filename,
		SizeBytes:        info.Size(),
		EncodedAt:        time.Now().UTC().Format(time.RFC3339),
		Codec:            s.cfg.VideoCodec,
		CRF:              s.cfg.VideoCRF,
	}
	if err := sidecar.Write(dest, meta); err != nil {
		slog.Error("Failed to write sidecar", "file", dest, "error", err)
		s.failJob(job, dest)
		return
	}

	slog.Info("Processing completed", "file", dest)

	// Cleanup source
	os.Remove(src)

	// Level 3 Update: Mark original source file as totally done
	s.history.Add(job.SourceFile)
}

// failJob reverts a job that could not be completed (encoding, stat, or
// sidecar failure): it drops the session dedup flag so the source is
// retried on the next poll, quarantines the local source file into
// failed/, and removes any partial artifact/sidecar so nothing is left
// orphaned on the served volume.
func (s *Service) failJob(job Job, dest string) {
	// CRITICAL: Remove from session cache so it gets picked up by next poll
	s.mu.Lock()
	delete(s.downloaded, job.SourceFile)
	s.mu.Unlock()

	// Move source to failed/
	failedDir := filepath.Join(s.cfg.OutputRoot, "failed")
	os.MkdirAll(failedDir, 0755)
	os.Rename(job.LocalPath, filepath.Join(failedDir, filepath.Base(job.LocalPath)))

	// Cleanup partial artifact/sidecar if they exist, so a retry never
	// leaves an orphaned artifact or sidecar behind.
	os.Remove(dest)
	os.Remove(sidecar.PathFor(dest))
}

func isVideo(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".mkv" || ext == ".mp4"
}
