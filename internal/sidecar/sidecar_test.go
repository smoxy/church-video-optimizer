package sidecar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrite_SchemaAndContent(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "vga_42_1_decime-luglio.mp4")
	if err := os.WriteFile(artifact, []byte("fake-encoded-bytes"), 0644); err != nil {
		t.Fatalf("failed to create fake artifact: %v", err)
	}

	meta := Meta{
		ResourceID:       42,
		Category:         "vga",
		SourceURL:        "https://example.com/download/archive.zip",
		OriginalFilename: "nome interno allo zip.mp4",
		Artifact:         "vga_42_1_decime-luglio.mp4",
		SizeBytes:        12345678,
		EncodedAt:        "2026-07-02T13:45:00Z",
		Codec:            "libx265",
		CRF:              27,
	}

	if err := Write(artifact, meta); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	sidecarPath := PathFor(artifact)
	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("failed to read sidecar: %v", err)
	}

	// 1. Round-trips into the Meta struct with every field intact
	// (schema forced to SchemaVersion regardless of the input value).
	var got Meta
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("sidecar is not valid JSON: %v", err)
	}
	want := meta
	want.Schema = SchemaVersion
	if got != want {
		t.Fatalf("sidecar content mismatch:\n got:  %+v\n want: %+v", got, want)
	}

	// 2. The exact JSON key names from contract-video-volume.md are present
	// (guards against struct tag typos/renames breaking the contract).
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("sidecar is not valid JSON (generic): %v", err)
	}
	for _, key := range []string{
		"schema", "resource_id", "category", "source_url",
		"original_filename", "artifact", "size_bytes", "encoded_at",
		"codec", "crf",
	} {
		if _, ok := generic[key]; !ok {
			t.Errorf("sidecar JSON missing expected key %q; got keys %v", key, generic)
		}
	}

	// 3. schema is exactly the numeric SchemaVersion (1), not e.g. a string.
	schemaVal, ok := generic["schema"].(float64)
	if !ok || int(schemaVal) != SchemaVersion {
		t.Errorf("sidecar schema field = %v (%T), want numeric %d", generic["schema"], generic["schema"], SchemaVersion)
	}
}

func TestWrite_PathForConvention(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "mis_7_1_missione-estiva.mp4")
	if err := os.WriteFile(artifact, []byte("x"), 0644); err != nil {
		t.Fatalf("failed to create fake artifact: %v", err)
	}

	if err := Write(artifact, Meta{ResourceID: 7, Category: "mis"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	want := artifact + ".meta.json"
	if PathFor(artifact) != want {
		t.Fatalf("PathFor(%q) = %q, want %q", artifact, PathFor(artifact), want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected sidecar at %q: %v", want, err)
	}
}

func TestWrite_AtomicNoLeftoverTempFiles(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "gcv_1_1_prima-decima.mp4")
	if err := os.WriteFile(artifact, []byte("x"), 0644); err != nil {
		t.Fatalf("failed to create fake artifact: %v", err)
	}

	if err := Write(artifact, Meta{ResourceID: 1, Category: "gcv"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.Contains(e.Name(), ".tmp") || strings.HasPrefix(e.Name(), ".sidecar-") {
			t.Errorf("leftover temp file after Write(): %s", e.Name())
		}
	}

	wantSidecar := filepath.Base(artifact) + ".meta.json"
	found := false
	for _, n := range names {
		if n == wantSidecar {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %q among directory entries, got %v", wantSidecar, names)
	}

	// Final sidecar file is world/group readable (0644), like artifacts.
	info, err := os.Stat(filepath.Join(dir, wantSidecar))
	if err != nil {
		t.Fatalf("Stat sidecar: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Errorf("sidecar permissions = %v, want 0644", perm)
	}
}

func TestWrite_OverwritesExistingSidecarAtomically(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "vga_5_1_replay.mp4")
	if err := os.WriteFile(artifact, []byte("x"), 0644); err != nil {
		t.Fatalf("failed to create fake artifact: %v", err)
	}

	if err := Write(artifact, Meta{ResourceID: 5, Category: "vga", SizeBytes: 111}); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	if err := Write(artifact, Meta{ResourceID: 5, Category: "vga", SizeBytes: 222}); err != nil {
		t.Fatalf("second Write() error = %v", err)
	}

	raw, err := os.ReadFile(PathFor(artifact))
	if err != nil {
		t.Fatalf("failed to read sidecar: %v", err)
	}
	var got Meta
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("sidecar is not valid JSON: %v", err)
	}
	if got.SizeBytes != 222 {
		t.Fatalf("sidecar SizeBytes = %d, want 222 (second write must fully replace the first)", got.SizeBytes)
	}

	// Still exactly one sidecar file, no leftovers from either write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), Suffix) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 sidecar file, found %d", count)
	}
}
