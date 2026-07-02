package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanupOldOutputDirs_RemovesArtifactsAndTheirSidecarsTogether guards
// against a regression where a future per-file (rather than per-directory)
// retention sweep would delete an aged artifact but leave its
// <artifact>.meta.json sidecar behind as an orphan.
func TestCleanupOldOutputDirs_RemovesArtifactsAndTheirSidecarsTogether(t *testing.T) {
	root := t.TempDir()

	oldWeek := filepath.Join(root, "2020-W01")
	if err := os.MkdirAll(oldWeek, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	artifact := filepath.Join(oldWeek, "vga_1_1_old-video.mp4")
	sidecarPath := artifact + ".meta.json"
	if err := os.WriteFile(artifact, []byte("old-artifact-bytes"), 0644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.WriteFile(sidecarPath, []byte(`{"schema":1,"resource_id":1}`), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	// Age the directory itself past the retention window; CleanupOldOutputDirs
	// keys retention off the directory's own ModTime.
	old := time.Now().AddDate(0, 0, -30)
	if err := os.Chtimes(oldWeek, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	recentWeek := filepath.Join(root, "2099-W01")
	if err := os.MkdirAll(recentWeek, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	recentArtifact := filepath.Join(recentWeek, "vga_2_1_new-video.mp4")
	recentSidecar := recentArtifact + ".meta.json"
	if err := os.WriteFile(recentArtifact, []byte("new-artifact-bytes"), 0644); err != nil {
		t.Fatalf("write recent artifact: %v", err)
	}
	if err := os.WriteFile(recentSidecar, []byte(`{"schema":1,"resource_id":2}`), 0644); err != nil {
		t.Fatalf("write recent sidecar: %v", err)
	}

	CleanupOldOutputDirs(root, 14)

	if _, err := os.Stat(oldWeek); !os.IsNotExist(err) {
		t.Errorf("expected old week dir to be removed, stat err: %v", err)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Errorf("expected old artifact to be removed, stat err: %v", err)
	}
	if _, err := os.Stat(sidecarPath); !os.IsNotExist(err) {
		t.Errorf("expected old sidecar to be removed together with its artifact (no orphan left behind), stat err: %v", err)
	}

	if _, err := os.Stat(recentArtifact); err != nil {
		t.Errorf("expected recent artifact to survive cleanup: %v", err)
	}
	if _, err := os.Stat(recentSidecar); err != nil {
		t.Errorf("expected recent sidecar to survive cleanup: %v", err)
	}
}
