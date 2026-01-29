package extractor

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractAndClean unzips src to destDir and deletes src upon success.
// Returns the list of extracted file paths.
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
	
	// Close reader before removing file (Windows compatibility mostly, but good practice)
	r.Close()

	if err := os.Remove(src); err != nil {
		return extractedFiles, fmt.Errorf("failed to remove source zip: %w", err)
	}

	return extractedFiles, nil
}
