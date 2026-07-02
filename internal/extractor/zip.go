package extractor

import (
	"archive/zip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExtractAndClean unzips src to destDir and deletes src upon success.
//
// A zip archive's entries are a flat list of paths (there is no real
// directory tree to walk), so every entry is extracted regardless of
// nesting depth in a single pass — callers do not need to recurse
// themselves to reach videos in subfolders (categorie-video-non-mappate
// backlog, owner decision 2026-07-02: any category/any depth can carry
// video). The returned paths are sorted lexicographically so callers that
// derive a stable, reproducible order from them (e.g. the progressive
// artifact index "n", contract-video-volume) don't depend on the archive's
// own central-directory order, which varies by the tool that created it.
//
// Entries that are themselves zip archives (by extension, case-insensitive)
// are never extracted — zip-in-zip is not supported — they are skipped and
// logged instead of being written to destDir or opened as an archive.
func ExtractAndClean(src, destDir string) ([]string, error) {
	extractedFiles := []string{}

	r, err := zip.OpenReader(src)
	if err != nil {
		return nil, fmt.Errorf("failed to open zip: %w", err)
	}
	defer r.Close()

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create dest dir: %w", err)
	}

	for _, f := range r.File {
		fpath := filepath.Join(destDir, f.Name)

		// Zip Slip protection
		if !strings.HasPrefix(fpath, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal file path: %s", fpath)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		// Nested zip: never extracted (no zip-in-zip support). Skipped and
		// logged rather than silently dropped, so a producer that starts
		// nesting archives is visible in the logs instead of just losing
		// videos with no trace.
		if strings.ToLower(filepath.Ext(f.Name)) == ".zip" {
			slog.Warn("Nested zip entry ignored (zip-in-zip not supported)", "zip", src, "entry", f.Name)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return nil, err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return nil, err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return nil, err
		}

		_, err = io.Copy(outFile, rc)

		outFile.Close()
		rc.Close()

		if err != nil {
			return nil, err
		}

		extractedFiles = append(extractedFiles, fpath)
	}

	// Deterministic order (see doc comment) regardless of the archive's own
	// entry order or of the outcome of the source-removal step below.
	sort.Strings(extractedFiles)

	// Close reader before removing file (Windows compatibility mostly, but good practice)
	r.Close()

	if err := os.Remove(src); err != nil {
		return extractedFiles, fmt.Errorf("failed to remove source zip: %w", err)
	}

	return extractedFiles, nil
}
