// Package sidecar reads and writes the <artifact>.meta.json provenance
// sidecar defined by adr-0008 / contract-video-volume. The sidecar is the
// source of truth for matching a served artifact back to its originating
// resource (by resource_id), replacing filename-based matching.
package sidecar

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SchemaVersion is the current sidecar schema version.
const SchemaVersion = 1

// Suffix is appended to an artifact's filename to obtain its sidecar path.
const Suffix = ".meta.json"

// Meta is the sidecar JSON schema written next to every encoded artifact.
// Field names/order mirror contract-video-volume.md exactly.
type Meta struct {
	Schema           int    `json:"schema"`
	ResourceID       int    `json:"resource_id"`
	Category         string `json:"category"`
	SourceURL        string `json:"source_url"`
	OriginalFilename string `json:"original_filename"`
	Artifact         string `json:"artifact"`
	SizeBytes        int64  `json:"size_bytes"`
	EncodedAt        string `json:"encoded_at"` // ISO8601 UTC, e.g. time.Now().UTC().Format(time.RFC3339)
	Codec            string `json:"codec"`
	CRF              int    `json:"crf"`
}

// PathFor returns the sidecar path for a given artifact path, e.g.
// ".../vga_42_1_decime-luglio.mp4" -> ".../vga_42_1_decime-luglio.mp4.meta.json".
func PathFor(artifactPath string) string {
	return artifactPath + Suffix
}

// Write serializes meta as the sidecar for artifactPath. The write is
// atomic: it writes to a temp file in the same directory as the artifact
// (so the final rename is on the same filesystem) and renames it into place,
// so readers (mail-parser) never observe a partially-written sidecar.
//
// Callers must only invoke Write after the artifact itself has been fully
// and successfully written (contract-video-volume: "scrittura atomica dopo
// encoding riuscito").
func Write(artifactPath string, meta Meta) error {
	meta.Schema = SchemaVersion

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("sidecar: marshal %s: %w", artifactPath, err)
	}

	dir := filepath.Dir(artifactPath)
	tmp, err := os.CreateTemp(dir, ".sidecar-*.tmp")
	if err != nil {
		return fmt.Errorf("sidecar: create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup: once the rename below succeeds this is a no-op
	// (nothing left at tmpPath); on any earlier error it removes the
	// partially-written temp file so no debris is left behind.
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("sidecar: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sidecar: sync temp file: %w", err)
	}
	// Match the 0644 "web serving" permissions used for artifacts
	// (os.CreateTemp defaults to 0600).
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return fmt.Errorf("sidecar: chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sidecar: close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, PathFor(artifactPath)); err != nil {
		return fmt.Errorf("sidecar: rename into place: %w", err)
	}
	return nil
}
