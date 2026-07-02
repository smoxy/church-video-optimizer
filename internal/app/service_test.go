package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-optimizer/internal/config"
	"video-optimizer/internal/fetcher"
	"video-optimizer/internal/processor"
	"video-optimizer/internal/sidecar"
)

// --- test helpers -----------------------------------------------------

// zipEntry is one file to write into a test archive.
type zipEntry struct {
	name    string
	content string
}

// buildOrderedZip creates a zip archive at dest with one entry per element
// of entries, written in slice order. Unlike a map-based builder, this
// keeps entry order deterministic, which matters for asserting on the
// progressive "n" index assigned per adr-0008.
func buildOrderedZip(t *testing.T, dest string, entries []zipEntry) {
	t.Helper()

	f, err := os.Create(dest)
	if err != nil {
		t.Fatalf("failed to create zip: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatalf("failed to add zip entry %q: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.content)); err != nil {
			t.Fatalf("failed to write zip entry %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}
}

// captureSlog redirects the default slog logger to buf and returns a
// restore func to put the previous default back (call via defer/t.Cleanup).
func captureSlog(buf *bytes.Buffer) func() {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	return func() { slog.SetDefault(prev) }
}

// fakeEncoder is a processor.Encoder that never shells out to ffmpeg: it
// simulates a successful encode by writing deterministic placeholder bytes
// to dest (so size_bytes assertions are meaningful), or returns a
// pre-configured error to simulate an encoding failure.
type fakeEncoder struct {
	err error
}

func (f *fakeEncoder) Encode(_ context.Context, src, dest string, _ processor.EncodeParams) error {
	if f.err != nil {
		return f.err
	}
	content := fmt.Sprintf("fake-encoded:%s", filepath.Base(src))
	return os.WriteFile(dest, []byte(content), 0644)
}

// newTestService builds a Service suitable for exercising processItem
// directly (bypassing NewService, which wires up a real fetcher.Fetcher and
// an async cleanup goroutine that are irrelevant here).
func newTestService(t *testing.T, enc processor.Encoder) *Service {
	t.Helper()

	root := t.TempDir()
	cfg := &config.Config{
		OutputRoot:  root,
		VideoCodec:  "libx265",
		VideoCRF:    27,
		VideoPreset: "medium",
		AudioCodec:  "aac",
	}

	hist, err := NewHistory(filepath.Join(root, "processed.json"))
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}

	return &Service{
		cfg:        cfg,
		encoder:    enc,
		history:    hist,
		downloaded: make(map[string]bool),
		ctx:        context.Background(),
	}
}

func outputWeekDir(root string) string {
	year, week := time.Now().ISOWeek()
	return filepath.Join(root, fmt.Sprintf("%d-W%02d", year, week))
}

// --- weekFolderName: content week vs processing week (fallback) ---------

func TestWeekFolderName_ContentWeekAndFallback(t *testing.T) {
	// Fixed "now" in ISO week 2026-W27, distinct from every content week
	// used below, so content-vs-processing mixups can't pass by accident.
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name         string
		weekDate     string
		wantFolder   string
		wantFallback bool
	}{
		{
			name:     "valid content week drives the folder (W19, maggio)",
			weekDate: "2026-05-09", wantFolder: "2026-W19", wantFallback: false,
		},
		{
			name:     "ISO week-year boundary: Dec 29 2025 is 2026-W01",
			weekDate: "2025-12-29", wantFolder: "2026-W01", wantFallback: false,
		},
		{
			name:     "empty week_date (older mail-parser): processing-week fallback",
			weekDate: "", wantFolder: "2026-W27", wantFallback: true,
		},
		{
			name:     "unparsable week_date: processing-week fallback, no error",
			weekDate: "not-a-date", wantFolder: "2026-W27", wantFallback: true,
		},
		{
			name:     "wrong layout (DD/MM/YYYY): processing-week fallback",
			weekDate: "09/05/2026", wantFolder: "2026-W27", wantFallback: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			folder, usedFallback := weekFolderName(c.weekDate, now)
			if folder != c.wantFolder {
				t.Errorf("weekFolderName(%q) folder = %q, want %q", c.weekDate, folder, c.wantFolder)
			}
			if usedFallback != c.wantFallback {
				t.Errorf("weekFolderName(%q) usedFallback = %v, want %v", c.weekDate, usedFallback, c.wantFallback)
			}
		})
	}
}

// TestProcessItem_WeekDate_ArtifactLandsInContentWeekFolder is the
// end-to-end guard for the week-folder drift fix (backlog drift-noti): a
// job carrying the content's week_date (a May/W19 resource processed
// months later) must produce its artifact+sidecar under the CONTENT's week
// folder, not under time.Now()'s.
func TestProcessItem_WeekDate_ArtifactLandsInContentWeekFolder(t *testing.T) {
	tmpDir := t.TempDir()
	videoPath := filepath.Join(tmpDir, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("video-bytes"), 0644); err != nil {
		t.Fatalf("failed to write fake video: %v", err)
	}

	item := fetcher.FileItem{
		URL:        "https://example.com/download/clip.mp4",
		Filename:   "clip.mp4",
		ResourceID: 7,
		Category:   "vga",
		Title:      "Risorsa di maggio",
		WeekDate:   "2026-05-09", // ISO week 2026-W19
	}
	jobs, err := jobsForItem(item, videoPath, tmpDir)
	if err != nil {
		t.Fatalf("jobsForItem() error = %v", err)
	}
	if len(jobs) != 1 || jobs[0].WeekDate != "2026-05-09" {
		t.Fatalf("expected 1 job inheriting WeekDate, got %+v", jobs)
	}

	svc := newTestService(t, &fakeEncoder{})
	svc.processItem(jobs[0])

	contentDir := filepath.Join(svc.cfg.OutputRoot, "2026-W19")
	artifact := filepath.Join(contentDir, "vga_7_1_clip.mp4")
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("expected artifact in content-week folder %q: %v", contentDir, err)
	}
	if _, err := os.Stat(sidecar.PathFor(artifact)); err != nil {
		t.Fatalf("expected sidecar next to artifact in content-week folder: %v", err)
	}

	// And nothing leaked into the processing-week folder (skip the check in
	// the vanishingly unlikely case the test actually runs in 2026-W19).
	if nowDir := outputWeekDir(svc.cfg.OutputRoot); nowDir != contentDir {
		if _, err := os.Stat(nowDir); !os.IsNotExist(err) {
			t.Errorf("processing-week folder %q should not exist, stat err: %v", nowDir, err)
		}
	}
}

// TestProcessItem_NoWeekDate_FallsBackToProcessingWeekAndLogs pins the
// rollout-safety contract: against an older mail-parser that doesn't emit
// week_date yet, processing must keep working exactly as before (artifact
// under time.Now()'s week folder) and say so in the logs.
func TestProcessItem_NoWeekDate_FallsBackToProcessingWeekAndLogs(t *testing.T) {
	var logBuf bytes.Buffer
	restore := captureSlog(&logBuf)
	defer restore()

	tmpDir := t.TempDir()
	videoPath := filepath.Join(tmpDir, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("video-bytes"), 0644); err != nil {
		t.Fatalf("failed to write fake video: %v", err)
	}

	item := fetcher.FileItem{
		URL:        "https://example.com/download/clip.mp4",
		Filename:   "clip.mp4",
		ResourceID: 7,
		Category:   "vga",
		Title:      "Senza week_date",
		// WeekDate deliberately empty: server pre-week_date.
	}
	jobs, err := jobsForItem(item, videoPath, tmpDir)
	if err != nil {
		t.Fatalf("jobsForItem() error = %v", err)
	}

	svc := newTestService(t, &fakeEncoder{})
	svc.processItem(jobs[0])

	artifact := filepath.Join(outputWeekDir(svc.cfg.OutputRoot), "vga_7_1_clip.mp4")
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("expected artifact in processing-week folder (fallback): %v", err)
	}

	if !strings.Contains(logBuf.String(), "fallback alla settimana di elaborazione") {
		t.Errorf("expected an explicit fallback log line, got logs:\n%s", logBuf.String())
	}
}

// --- jobsForItem: resource metadata inheritance (adr-0008) -------------

func TestJobsForItem_ZipMultiVideo_InheritsResourceMetadataWithProgressiveIndex(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "archive.zip")
	extractDir := filepath.Join(tmpDir, "extract")

	buildOrderedZip(t, zipPath, []zipEntry{
		{name: "Interview Pt1.MP4", content: "video-one"},
		{name: "cover.jpg", content: "not-a-video"}, // must be ignored
		{name: "seconda parte.mkv", content: "video-two"},
	})

	item := fetcher.FileItem{
		URL:        "https://example.com/download/archive.zip",
		Filename:   "archive.zip",
		ResourceID: 42,
		Category:   "vga",
		Title:      "Serata VGA di Luglio",
	}

	jobs, err := jobsForItem(item, zipPath, extractDir)
	if err != nil {
		t.Fatalf("jobsForItem() error = %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs (non-video entry ignored), got %d: %+v", len(jobs), jobs)
	}

	want := []Job{
		{
			SourceFile: "archive.zip", LocalPath: filepath.Join(extractDir, "Interview Pt1.MP4"),
			ResourceID: 42, Category: "vga", Title: "Serata VGA di Luglio",
			SourceURL: item.URL, OriginalFilename: "Interview Pt1.MP4", Index: 1,
		},
		{
			SourceFile: "archive.zip", LocalPath: filepath.Join(extractDir, "seconda parte.mkv"),
			ResourceID: 42, Category: "vga", Title: "Serata VGA di Luglio",
			SourceURL: item.URL, OriginalFilename: "seconda parte.mkv", Index: 2,
		},
	}
	for i := range want {
		if jobs[i] != want[i] {
			t.Errorf("job[%d] = %+v, want %+v", i, jobs[i], want[i])
		}
	}

	// extractor.ExtractAndClean removes the source zip after extraction.
	if _, err := os.Stat(zipPath); !os.IsNotExist(err) {
		t.Errorf("expected zip to be removed after extraction, stat err: %v", err)
	}
}

func TestJobsForItem_DirectVideo_SingleJobIndex1(t *testing.T) {
	tmpDir := t.TempDir()
	videoPath := filepath.Join(tmpDir, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("video-bytes"), 0644); err != nil {
		t.Fatalf("failed to write fake video: %v", err)
	}

	item := fetcher.FileItem{
		URL:        "https://example.com/d/clip.mp4",
		Filename:   "clip.mp4",
		ResourceID: 7,
		Category:   "gcv",
		Title:      "Prima Decima",
	}

	jobs, err := jobsForItem(item, videoPath, tmpDir)
	if err != nil {
		t.Fatalf("jobsForItem() error = %v", err)
	}

	want := Job{
		SourceFile: "clip.mp4", LocalPath: videoPath,
		ResourceID: 7, Category: "gcv", Title: "Prima Decima",
		SourceURL: item.URL, OriginalFilename: "clip.mp4", Index: 1,
	}
	if len(jobs) != 1 || jobs[0] != want {
		t.Fatalf("jobsForItem() = %+v, want [%+v]", jobs, want)
	}
}

func TestJobsForItem_ZipWithoutVideos_SkipsAndLogsWarning(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "docs.zip")
	extractDir := filepath.Join(tmpDir, "extract")

	buildOrderedZip(t, zipPath, []zipEntry{
		{name: "readme.txt", content: "hello"},
		{name: "cover.jpg", content: "image-bytes"},
	})

	item := fetcher.FileItem{
		URL: "https://example.com/d/docs.zip", Filename: "docs.zip",
		ResourceID: 1, Category: "vga",
	}

	var logBuf bytes.Buffer
	defer captureSlog(&logBuf)()

	jobs, err := jobsForItem(item, zipPath, extractDir)
	if err != nil {
		t.Fatalf("jobsForItem() error = %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs for a zip without videos, got %d: %+v", len(jobs), jobs)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "WARN") {
		t.Errorf("expected a WARNING to be logged, got: %s", logged)
	}
	if !strings.Contains(logged, "docs.zip") {
		t.Errorf("expected the WARNING to mention the zip filename, got: %s", logged)
	}
}

// TestJobsForItem_ZipWithNestedVideos_FindsAllAtAnyDepthInDeterministicOrder
// covers the categorie-video-non-mappate backlog (owner decision
// 2026-07-02): video search must be recursive over the whole extracted
// tree, not just the zip's top level, and the resulting "n" (Job.Index)
// must be stable/reproducible rather than following the archive's own
// (unspecified) entry order. Entries are written in an order that is
// neither alphabetical nor depth-first so the assertion can't pass by
// accident, and the category ("scuola") is one the backlog explicitly
// calls out as unmapped/never filtered.
func TestJobsForItem_ZipWithNestedVideos_FindsAllAtAnyDepthInDeterministicOrder(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "archive.zip")
	extractDir := filepath.Join(tmpDir, "extract")

	buildOrderedZip(t, zipPath, []zipEntry{
		{name: "z-top.mp4", content: "top-z"},
		{name: "sub/a-nested.mkv", content: "nested-a"},
		{name: "sub/deep/b-deep.mp4", content: "deep-b"},
		{name: "cover.jpg", content: "not-a-video"},
		{name: "sub/notes.txt", content: "not-a-video-either"},
	})

	item := fetcher.FileItem{
		URL:        "https://example.com/download/archive.zip",
		Filename:   "archive.zip",
		ResourceID: 7,
		Category:   "scuola",
		Title:      "Lezione",
	}

	jobs, err := jobsForItem(item, zipPath, extractDir)
	if err != nil {
		t.Fatalf("jobsForItem() error = %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs (only videos, found at any depth), got %d: %+v", len(jobs), jobs)
	}

	// Sorted order over the full extracted tree, not the archive's entry order.
	wantLocalPaths := []string{
		filepath.Join(extractDir, "sub", "a-nested.mkv"),
		filepath.Join(extractDir, "sub", "deep", "b-deep.mp4"),
		filepath.Join(extractDir, "z-top.mp4"),
	}
	wantOriginalFilenames := []string{"a-nested.mkv", "b-deep.mp4", "z-top.mp4"}

	for i, job := range jobs {
		if job.LocalPath != wantLocalPaths[i] {
			t.Errorf("jobs[%d].LocalPath = %q, want %q", i, job.LocalPath, wantLocalPaths[i])
		}
		if job.OriginalFilename != wantOriginalFilenames[i] {
			t.Errorf("jobs[%d].OriginalFilename = %q, want %q (base name only, nested folders must not leak in)", i, job.OriginalFilename, wantOriginalFilenames[i])
		}
		if job.Index != i+1 {
			t.Errorf("jobs[%d].Index = %d, want %d (stable progressive n)", i, job.Index, i+1)
		}
		if job.Category != "scuola" {
			t.Errorf("jobs[%d].Category = %q, want %q (categories are open, adr-0008: no filter)", i, job.Category, "scuola")
		}
	}
}

// TestJobsForItem_ZipWithNestedZipEntry_IgnoresItButProcessesSiblingVideos
// covers the explicit non-goal (categorie-video-non-mappate backlog):
// zip-in-zip is not supported. A nested zip entry, at any depth, must be
// skipped and logged rather than extracted, while real videos elsewhere in
// the same archive are still turned into jobs normally.
func TestJobsForItem_ZipWithNestedZipEntry_IgnoresItButProcessesSiblingVideos(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "archive.zip")
	extractDir := filepath.Join(tmpDir, "extract")

	buildOrderedZip(t, zipPath, []zipEntry{
		{name: "sub/inner.zip", content: "not-really-opened-as-an-archive"},
		{name: "video.mp4", content: "real-video"},
	})

	item := fetcher.FileItem{
		URL: "https://example.com/d/archive.zip", Filename: "archive.zip",
		ResourceID: 3, Category: "vga",
	}

	var logBuf bytes.Buffer
	defer captureSlog(&logBuf)()

	jobs, err := jobsForItem(item, zipPath, extractDir)
	if err != nil {
		t.Fatalf("jobsForItem() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job (nested zip ignored, sibling video kept), got %d: %+v", len(jobs), jobs)
	}
	if jobs[0].OriginalFilename != "video.mp4" {
		t.Errorf("jobs[0].OriginalFilename = %q, want %q", jobs[0].OriginalFilename, "video.mp4")
	}

	if _, err := os.Stat(filepath.Join(extractDir, "sub", "inner.zip")); !os.IsNotExist(err) {
		t.Errorf("expected the nested zip to never be extracted to disk, stat err: %v", err)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "WARN") || !strings.Contains(logged, "inner.zip") {
		t.Errorf("expected a WARNING mentioning the ignored nested zip, got: %s", logged)
	}
}

// --- processItem: naming + sidecar, end to end (fake encoder) ----------

func TestProcessItem_ZipMultiVideo_ProducesArtifactsAndSidecarsWithInheritedResourceID(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "archive.zip")
	extractDir := filepath.Join(tmpDir, "extract")

	buildOrderedZip(t, zipPath, []zipEntry{
		{name: "Interview Pt1.MP4", content: "video-one-bytes"},
		{name: "seconda parte.mkv", content: "video-two-bytes-a-bit-longer"},
	})

	item := fetcher.FileItem{
		URL:        "https://example.com/download/archive.zip",
		Filename:   "archive.zip",
		ResourceID: 42,
		Category:   "vga",
		Title:      "Serata VGA di Luglio",
	}

	jobs, err := jobsForItem(item, zipPath, extractDir)
	if err != nil {
		t.Fatalf("jobsForItem() error = %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	svc := newTestService(t, &fakeEncoder{})
	for _, job := range jobs {
		svc.processItem(job)
	}

	outDir := outputWeekDir(svc.cfg.OutputRoot)

	cases := []struct {
		artifact         string
		originalFilename string
	}{
		{"vga_42_1_interview-pt1.mp4", "Interview Pt1.MP4"},
		{"vga_42_2_seconda-parte.mp4", "seconda parte.mkv"},
	}

	for _, c := range cases {
		artifactPath := filepath.Join(outDir, c.artifact)
		info, err := os.Stat(artifactPath)
		if err != nil {
			t.Fatalf("expected artifact %q: %v", c.artifact, err)
		}

		raw, err := os.ReadFile(sidecar.PathFor(artifactPath))
		if err != nil {
			t.Fatalf("expected sidecar for %q: %v", c.artifact, err)
		}
		var meta sidecar.Meta
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("sidecar for %q is not valid JSON: %v", c.artifact, err)
		}

		if meta.Schema != sidecar.SchemaVersion {
			t.Errorf("%s: sidecar Schema = %d, want %d", c.artifact, meta.Schema, sidecar.SchemaVersion)
		}
		if meta.ResourceID != 42 {
			t.Errorf("%s: sidecar ResourceID = %d, want 42 (inherited from the zip's resource)", c.artifact, meta.ResourceID)
		}
		if meta.Category != "vga" {
			t.Errorf("%s: sidecar Category = %q, want %q", c.artifact, meta.Category, "vga")
		}
		if meta.SourceURL != item.URL {
			t.Errorf("%s: sidecar SourceURL = %q, want %q", c.artifact, meta.SourceURL, item.URL)
		}
		if meta.OriginalFilename != c.originalFilename {
			t.Errorf("%s: sidecar OriginalFilename = %q, want %q", c.artifact, meta.OriginalFilename, c.originalFilename)
		}
		if meta.Artifact != c.artifact {
			t.Errorf("%s: sidecar Artifact = %q, want %q", c.artifact, meta.Artifact, c.artifact)
		}
		if meta.SizeBytes != info.Size() {
			t.Errorf("%s: sidecar SizeBytes = %d, want %d (actual artifact size)", c.artifact, meta.SizeBytes, info.Size())
		}
		if meta.Codec != "libx265" || meta.CRF != 27 {
			t.Errorf("%s: sidecar Codec/CRF = %s/%d, want libx265/27", c.artifact, meta.Codec, meta.CRF)
		}
		if _, err := time.Parse(time.RFC3339, meta.EncodedAt); err != nil {
			t.Errorf("%s: sidecar EncodedAt = %q is not ISO8601/RFC3339: %v", c.artifact, meta.EncodedAt, err)
		}

		if perm := info.Mode().Perm(); perm != 0644 {
			t.Errorf("%s: artifact permissions = %v, want 0644", c.artifact, perm)
		}
	}

	// Both extracted sources are consumed once their job succeeds.
	for _, job := range jobs {
		if _, err := os.Stat(job.LocalPath); !os.IsNotExist(err) {
			t.Errorf("expected source %q to be removed after successful processing", job.LocalPath)
		}
	}

	// The shared SourceFile (the zip) is marked done in history.
	if !svc.history.Has("archive.zip") {
		t.Errorf("expected history to contain the zip's SourceFile after successful processing")
	}
}

// TestProcessItem_NestedVideoInZip_ProducesCanonicalArtifactFromBaseFilename
// closes the loop end to end for the categorie-video-non-mappate backlog: a
// video found deep in the extracted tree, under an unmapped category
// ("materiale", per the backlog's owner decision), must still become a
// canonical artifact + sidecar exactly like a top-level one, with the slug
// derived from the entry's base filename only (the nested folders it lived
// in inside the zip must not leak into the artifact name).
func TestProcessItem_NestedVideoInZip_ProducesCanonicalArtifactFromBaseFilename(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "archive.zip")
	extractDir := filepath.Join(tmpDir, "extract")

	buildOrderedZip(t, zipPath, []zipEntry{
		{name: "sub/deep/Interview Pt1.MP4", content: "video-one-bytes"},
	})

	item := fetcher.FileItem{
		URL:        "https://example.com/download/archive.zip",
		Filename:   "archive.zip",
		ResourceID: 42,
		Category:   "materiale",
		Title:      "Serata di Luglio",
	}

	jobs, err := jobsForItem(item, zipPath, extractDir)
	if err != nil {
		t.Fatalf("jobsForItem() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job for the nested video, got %d: %+v", len(jobs), jobs)
	}

	svc := newTestService(t, &fakeEncoder{})
	svc.processItem(jobs[0])

	outDir := outputWeekDir(svc.cfg.OutputRoot)
	artifact := "materiale_42_1_interview-pt1.mp4"
	artifactPath := filepath.Join(outDir, artifact)

	if _, err := os.Stat(artifactPath); err != nil {
		t.Fatalf("expected canonical artifact for the nested video: %v", err)
	}

	raw, err := os.ReadFile(sidecar.PathFor(artifactPath))
	if err != nil {
		t.Fatalf("expected sidecar for the nested video artifact: %v", err)
	}
	var meta sidecar.Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("sidecar is not valid JSON: %v", err)
	}
	if meta.OriginalFilename != "Interview Pt1.MP4" {
		t.Errorf("sidecar OriginalFilename = %q, want base filename %q (nested folders must not leak in)", meta.OriginalFilename, "Interview Pt1.MP4")
	}
	if meta.Category != "materiale" {
		t.Errorf("sidecar Category = %q, want %q (categories are open, adr-0008: no filter)", meta.Category, "materiale")
	}
	if meta.Artifact != artifact {
		t.Errorf("sidecar Artifact = %q, want %q", meta.Artifact, artifact)
	}
}

func TestProcessItem_EncodeFailure_NoOrphanArtifactOrSidecar(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "clip.mp4")
	if err := os.WriteFile(srcPath, []byte("source-bytes"), 0644); err != nil {
		t.Fatalf("failed to write fake source: %v", err)
	}

	job := Job{
		SourceFile: "clip.mp4", LocalPath: srcPath,
		ResourceID: 9, Category: "mis", Title: "Missione",
		SourceURL: "https://example.com/clip.mp4", OriginalFilename: "clip.mp4", Index: 1,
	}

	svc := newTestService(t, &fakeEncoder{err: fmt.Errorf("boom")})
	svc.downloaded[job.SourceFile] = true

	svc.processItem(job)

	artifactPath := filepath.Join(outputWeekDir(svc.cfg.OutputRoot), "mis_9_1_clip.mp4")

	if _, err := os.Stat(artifactPath); !os.IsNotExist(err) {
		t.Errorf("expected no artifact after encode failure, stat err: %v", err)
	}
	if _, err := os.Stat(sidecar.PathFor(artifactPath)); !os.IsNotExist(err) {
		t.Errorf("expected no sidecar after encode failure, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(svc.cfg.OutputRoot, "failed", "clip.mp4")); err != nil {
		t.Errorf("expected source to be moved to failed/: %v", err)
	}
	if svc.history.Has("clip.mp4") {
		t.Errorf("a failed job must not be marked done in history")
	}
	if svc.downloaded["clip.mp4"] {
		t.Errorf("expected the session dedup flag to be cleared after failure (allow retry on next poll)")
	}
}
