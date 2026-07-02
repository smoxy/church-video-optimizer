package extractor

import (
	"archive/zip"
	"os"
	"path/filepath"
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
