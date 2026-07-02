package fetcher

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type FileItem struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
}

// APIResponse matches the user provided JSON structure
type APIResponse struct {
	Count     int        `json:"count"`
	Resources []Resource `json:"resources"`
}

type Resource struct {
	ID          int    `json:"id"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	Description string `json:"description"`
	DownloadURL string `json:"download_url"`
	FileType    any    `json:"file_type"` // can be null
	IsActive    bool   `json:"is_active"`
	CreatedAt   string `json:"created_at"`
}

type Fetcher struct {
	Endpoint       string
	PollClient     *http.Client
	DownloadClient *http.Client
	Concurrency    int
	ChunkSize      int64
}

func New(endpoint string, concurrency int, chunkSizeMB int64) *Fetcher {
	// Custom Transport for Connection Pooling
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20, // increased for concurrent chunks
		IdleConnTimeout:     90 * time.Second,
	}

	return &Fetcher{
		Endpoint:       endpoint,
		PollClient:     &http.Client{Timeout: 30 * time.Second},
		DownloadClient: &http.Client{Timeout: 0, Transport: transport}, // No timeout for downloads, custom transport
		Concurrency:    concurrency,
		ChunkSize:      chunkSizeMB * 1024 * 1024,
	}
}

// Poll fetches the list of files from the endpoint
func (f *Fetcher) Poll() ([]FileItem, error) {
	resp, err := f.PollClient.Get(f.Endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var fileItems []FileItem
	for _, res := range apiResp.Resources {
		// Only process valid download URLs
		if res.DownloadURL == "" {
			continue
		}
		
		// Extract filename from URL
		u, err := url.Parse(res.DownloadURL)
		if err != nil {
			continue
		}
		filename := filepath.Base(u.Path)
		if filename == "" || filename == "." || filename == "/" {
			continue
		}

		fileItems = append(fileItems, FileItem{
			URL:      res.DownloadURL,
			Filename: filename,
		})
	}

	return fileItems, nil
}

// Download downloads a file using concurrent chunks if possible
func (f *Fetcher) Download(urlStr string, dest string) error {
	// 1. Get Content-Length
	headReq, err := http.NewRequest("HEAD", urlStr, nil)
	if err != nil {
		return err
	}
	headResp, err := f.DownloadClient.Do(headReq)
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}
	headResp.Body.Close()

	if headResp.StatusCode != http.StatusOK && headResp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HEAD request failed: %d", headResp.StatusCode)
	}

	contentLength := headResp.ContentLength
	acceptRanges := headResp.Header.Get("Accept-Ranges")
	etag := strings.Trim(headResp.Header.Get("ETag"), "\"") // Remove quotes

	// If server doesn't support ranges or size is unknown, fallback to single stream
	var dlErr error
	if contentLength <= 0 || acceptRanges != "bytes" {
		slog.Info("Server does not support range requests or size unknown, falling back to single stream", "url", urlStr)
		dlErr = f.downloadSingle(urlStr, dest)
	} else {
		dlErr = f.downloadConcurrent(urlStr, dest, contentLength)
	}

	if dlErr != nil {
		return dlErr
	}

	// Verify Checksum if ETag is present and looks like MD5 (32 hex chars)
	if len(etag) == 32 {
		slog.Info("Verifying file integrity...", "file", dest, "etag", etag)
		if verifyErr := verifyMD5(dest, etag); verifyErr != nil {
			os.Remove(dest) // Corrupted file, delete it
			return fmt.Errorf("integrity check failed: %w", verifyErr)
		}
		slog.Info("Integrity check passed", "file", dest)
	}

	return nil
}

func verifyMD5(path, expectedHash string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}

	checksum := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(checksum, expectedHash) {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expectedHash, checksum)
	}
	return nil
}

func (f *Fetcher) downloadConcurrent(urlStr string, dest string, totalSize int64) error {
	// Pre-allocate file
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	if err := file.Truncate(totalSize); err != nil {
		return fmt.Errorf("failed to truncate file: %w", err)
	}

	// Calculate chunks
	numChunks := int(math.Ceil(float64(totalSize) / float64(f.ChunkSize)))
	type job struct {
		index int
		start int64
		end   int64
	}

	jobs := make(chan job, numChunks)
	results := make(chan error, numChunks)
	
	// Create worker pool
	var wg sync.WaitGroup
	for i := 0; i < f.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := f.downloadChunk(urlStr, file, j.start, j.end); err != nil {
					results <- fmt.Errorf("chunk %d failed: %w", j.index, err)
					return // worker exits on error, simple error handling for now
				}
			}
		}()
	}

	// Push jobs
	for i := 0; i < numChunks; i++ {
		start := int64(i) * f.ChunkSize
		end := start + f.ChunkSize - 1
		if end >= totalSize {
			end = totalSize - 1
		}
		jobs <- job{index: i, start: start, end: end}
	}
	close(jobs)

	// Wait for completion (in separate goroutine to allow checking results)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case err := <-results:
		return err // Return first error encountered
	case <-done:
		// Check if any errors occurred that might have closed done?
		// Assuming if results has no error and done is closed, we are good.
		// Wait, multiple errors might be sent?
		// Better approach: errgroup. But standard lib only.
		// If 'results' is empty here, we succeeded.
		select {
		case err := <-results:
			return err
		default:
			return nil
		}
	}
}

func (f *Fetcher) downloadChunk(urlStr string, file *os.File, start, end int64) error {
	// Retry loop
	maxRetries := 5
	for i := 0; i < maxRetries; i++ {
		err := f.downloadChunkAttempt(urlStr, file, start, end)
		if err == nil {
			return nil
		}
		slog.Warn("Chunk download failed, retrying", "start", start, "attempt", i+1, "error", err)
		time.Sleep(time.Duration(math.Pow(2, float64(i))) * time.Second) // Expo backoff
	}
	return fmt.Errorf("max retries exceeded for chunk %d-%d", start, end)
}

func (f *Fetcher) downloadChunkAttempt(urlStr string, file *os.File, start, end int64) error {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return err
	}
	
	byteRange := fmt.Sprintf("bytes=%d-%d", start, end)
	req.Header.Set("Range", byteRange)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := f.DownloadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Watchdog for this chunk
	var lastActivity int64 = time.Now().Unix()
	idleTimeout := 60 * time.Second

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				last := atomic.LoadInt64(&lastActivity)
				if time.Now().Unix()-last > int64(idleTimeout.Seconds()) {
					cancel()
					return
				}
			}
		}
	}()

	buf := make([]byte, 32*1024)
	var currentOffset = start
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			atomic.StoreInt64(&lastActivity, time.Now().Unix())
			// WriteAt is thread-safe
			if _, wErr := file.WriteAt(buf[:n], currentOffset); wErr != nil {
				close(done)
				return wErr
			}
			currentOffset += int64(n)
		}
		if err != nil {
			close(done)
			if err == io.EOF {
				return nil
			}
			if ctx.Err() != nil {
				return fmt.Errorf("timeout")
			}
			return err
		}
	}
}

func (f *Fetcher) downloadSingle(url string, dest string) error {
	// Fallback to original single-threaded download logic (with Watchdog)
	// ... (Previous implementation)
	// Re-implementing briefly for completeness
	req, err := http.NewRequest("GET", url, nil)
	if err != nil { return err }
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := f.DownloadClient.Do(req)
	if err != nil { return err }
	defer resp.Body.Close()

	out, err := os.Create(dest)
	if err != nil { return err }
	defer out.Close()

	var lastActivity int64 = time.Now().Unix()
	idleTimeout := 60 * time.Second
	
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done: return
			case <-ticker.C:
				if time.Now().Unix()-atomic.LoadInt64(&lastActivity) > int64(idleTimeout.Seconds()) {
					cancel()
					return
				}
			}
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			atomic.StoreInt64(&lastActivity, time.Now().Unix())
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				close(done)
				return wErr
			}
		}
		if err != nil {
			close(done)
			if err == io.EOF { return nil }
			if ctx.Err() != nil { return fmt.Errorf("timeout") }
			return err
		}
	}
}

