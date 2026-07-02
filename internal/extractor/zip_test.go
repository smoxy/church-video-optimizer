package extractor

import (
	"archive/zip"
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// buildZip creates a zip archive at dest with one entry per name/content pair.
// Entry names are written verbatim (no cleaning), so callers can construct
// malicious archives (e.g. "../evil.txt") to exercise Zip Slip protection.
func buildZip(t *testing.T, dest string, files map[string]string) {
	t.Helper()

	f, err := os.Create(dest)
	if err != nil {
		t.Fatalf("failed to create zip: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("failed to add zip entry %q: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write zip entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}
}

// zipEntry is one file to write into a test archive, in explicit order.
type zipEntry struct {
	name    string
	content string
}

// buildOrderedZip creates a zip archive at dest with one entry per element of
// entries, written in slice order (unlike buildZip's map, whose iteration
// order Go deliberately randomizes). Tests asserting on the order
// ExtractAndClean returns need entries written in a specific, known,
// non-alphabetical order, so that a passing test can't be an accident of map
// iteration or of the archive already happening to be sorted.
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

// captureSlog redirects the default slog logger to buf and returns a restore
// func to put the previous default back (call via defer/t.Cleanup).
func captureSlog(buf *bytes.Buffer) func() {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	return func() { slog.SetDefault(prev) }
}

func TestExtractAndClean_RejectsZipSlip(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "malicious.zip")
	destDir := filepath.Join(tmpDir, "dest")

	buildZip(t, zipPath, map[string]string{
		"../evil.txt": "payload",
	})

	if _, err := ExtractAndClean(zipPath, destDir); err == nil {
		t.Fatal("expected error for path-traversal zip entry, got nil")
	}

	// The entry must never land outside destDir.
	escaped := filepath.Join(tmpDir, "evil.txt")
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("zip slip entry escaped destDir: %s", escaped)
	}
}

func TestExtractAndClean_ExtractsValidZip(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "valid.zip")
	destDir := filepath.Join(tmpDir, "dest")

	buildZip(t, zipPath, map[string]string{
		"video.mp4": "fake-video-content",
	})

	files, err := ExtractAndClean(zipPath, destDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 extracted file, got %d", len(files))
	}

	want := filepath.Join(destDir, "video.mp4")
	if files[0] != want {
		t.Fatalf("expected extracted path %q, got %q", want, files[0])
	}

	content, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(content) != "fake-video-content" {
		t.Fatalf("unexpected content: %q", content)
	}

	// The source archive must be removed on success.
	if _, err := os.Stat(zipPath); !os.IsNotExist(err) {
		t.Fatalf("expected source zip to be removed, stat err: %v", err)
	}
}

// TestExtractAndClean_FindsNestedEntriesRecursivelyInDeterministicOrder
// proves two things at once (categorie-video-non-mappate backlog, owner
// decision 2026-07-02: any category/any depth can carry video):
//  1. entries at any nesting depth are extracted, not just the top level;
//  2. the returned order is sorted, not the archive's own (unspecified,
//     writer-dependent) central-directory order — callers deriving a stable
//     progressive index ("n" in the canonical artifact name) from this list
//     need that order to be reproducible across re-runs/re-extractions.
//
// Entries are written in an order that is deliberately neither alphabetical
// nor depth-first, so a test that only checked set membership (ignoring
// order) could not accidentally pass.
func TestExtractAndClean_FindsNestedEntriesRecursivelyInDeterministicOrder(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "nested.zip")
	destDir := filepath.Join(tmpDir, "dest")

	entries := []zipEntry{
		{name: "z-top.mp4", content: "top-z"},
		{name: "sub/a-nested.mkv", content: "nested-a"},
		{name: "sub/deep/b-deep.mp4", content: "deep-b"},
		{name: "a-top.mkv", content: "top-a"},
	}
	buildOrderedZip(t, zipPath, entries)

	files, err := ExtractAndClean(zipPath, destDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := make([]string, len(entries))
	for i, e := range entries {
		want[i] = filepath.Join(destDir, e.name)
	}
	sort.Strings(want)

	if len(files) != len(want) {
		t.Fatalf("expected %d extracted files (all depths), got %d: %v", len(want), len(files), files)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Errorf("files[%d] = %q, want %q (sorted order); full: %v", i, files[i], want[i], files)
		}
	}

	// Every entry must actually be readable on disk at its nested path.
	for _, e := range entries {
		p := filepath.Join(destDir, e.name)
		content, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("expected nested file %q on disk: %v", p, err)
			continue
		}
		if string(content) != e.content {
			t.Errorf("nested file %q content = %q, want %q", p, content, e.content)
		}
	}
}

// TestExtractAndClean_IgnoresNestedZipEntryWithLog covers the explicit
// non-goal (categorie-video-non-mappate backlog): a zip entry that is
// itself a .zip archive is never extracted (no zip-in-zip support) — it
// must be skipped and logged, at any nesting depth, while sibling entries
// are still extracted normally.
func TestExtractAndClean_IgnoresNestedZipEntryWithLog(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "outer.zip")
	destDir := filepath.Join(tmpDir, "dest")

	buildOrderedZip(t, zipPath, []zipEntry{
		{name: "video.mp4", content: "real-video"},
		{name: "sub/inner.ZIP", content: "not-actually-opened-as-an-archive"},
	})

	var logBuf bytes.Buffer
	defer captureSlog(&logBuf)()

	files, err := ExtractAndClean(zipPath, destDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("expected only the real video to be extracted, got %d: %v", len(files), files)
	}
	want := filepath.Join(destDir, "video.mp4")
	if files[0] != want {
		t.Fatalf("files[0] = %q, want %q", files[0], want)
	}

	// The nested zip must never be written to destDir.
	if _, statErr := os.Stat(filepath.Join(destDir, "sub", "inner.ZIP")); !os.IsNotExist(statErr) {
		t.Errorf("expected nested zip entry to never be written to disk, stat err: %v", statErr)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "WARN") {
		t.Errorf("expected a WARNING to be logged for the ignored nested zip, got: %s", logged)
	}
	if !strings.Contains(logged, "inner.ZIP") {
		t.Errorf("expected the WARNING to mention the ignored entry, got: %s", logged)
	}
}
