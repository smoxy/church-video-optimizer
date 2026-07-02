package fetcher

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestFetcher returns a minimal Fetcher wired for Download's
// single-stream fallback path (no connection-pooling transport needed for
// these tests).
func newTestFetcher() *Fetcher {
	return &Fetcher{
		DownloadClient: &http.Client{},
		Concurrency:    1,
		ChunkSize:      1024 * 1024,
	}
}

func TestDownload_HEAD200_Succeeds(t *testing.T) {
	const body = "hello world"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := newTestFetcher().Download(srv.URL, dest); err != nil {
		t.Fatalf("Download() error = %v, want nil", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}
	if string(got) != body {
		t.Fatalf("downloaded content = %q, want %q", got, body)
	}
}

// TestDownload_HEAD206WithoutRangeRequest_FailsFast covers the audit finding:
// the HEAD request Download sends carries no Range header, so per RFC 7233
// §4.1 a compliant server must never answer 206 (206 is only legal in
// response to a Range request). A server/proxy/CDN doing so anyway is
// misbehaving, and even taken at face value its Content-Length describes
// only the range it unilaterally chose to return, not the full resource
// (Content-Range is never parsed) - trusting it as totalSize would silently
// truncate/corrupt the download. Download must fail fast instead.
func TestDownload_HEAD206WithoutRangeRequest_FailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			if rng := r.Header.Get("Range"); rng != "" {
				t.Errorf("unexpected Range header on HEAD request: %q", rng)
			}
			// Deliberately a small Content-Length, distinct from any "real"
			// full-file size, to show it must never be trusted as totalSize.
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", "3")
			w.WriteHeader(http.StatusPartialContent)
			return
		}
		t.Errorf("unexpected %s request: Download should have failed right after the HEAD", r.Method)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	err := newTestFetcher().Download(srv.URL, dest)
	if err == nil {
		t.Fatal("Download() error = nil, want an error for an unsolicited 206 HEAD response")
	}
	if !strings.Contains(err.Error(), "206") {
		t.Errorf("Download() error = %v, want it to mention status 206", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Errorf("Download() left a file at %s despite failing", dest)
	}
}

func TestDownload_HEADNon200Status_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := newTestFetcher().Download(srv.URL, dest); err == nil {
		t.Fatal("Download() error = nil, want an error for a 404 HEAD response")
	}
}

// TestPoll_WeekDatePropagation pins the additive week_date contract
// (contract-resources-api): when the source API emits `week_date` (content
// week) it must reach FileItem verbatim, and when it's absent (older
// mail-parser) FileItem.WeekDate must be the empty string — the zero value
// downstream code treats as "use the processing-week fallback".
func TestPoll_WeekDatePropagation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"count": 2,
			"resources": [
				{"id": 42, "category": "vga", "title": "Con week_date",
				 "download_url": "https://cdn.example.com/files/archive.zip",
				 "is_active": true, "created_at": "2026-07-02T10:00:00Z",
				 "week_date": "2026-05-09"},
				{"id": 43, "category": "mis", "title": "Server vecchio senza week_date",
				 "download_url": "https://cdn.example.com/files/video.mp4",
				 "is_active": true, "created_at": "2026-07-02T10:00:00Z"}
			]
		}`))
	}))
	defer srv.Close()

	f := &Fetcher{Endpoint: srv.URL, PollClient: srv.Client()}
	items, err := f.Poll()
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("Poll() returned %d items, want 2", len(items))
	}

	if items[0].ResourceID != 42 || items[0].WeekDate != "2026-05-09" {
		t.Errorf("item[0] = %+v, want ResourceID 42 with WeekDate %q", items[0], "2026-05-09")
	}
	if items[1].ResourceID != 43 || items[1].WeekDate != "" {
		t.Errorf("item[1] = %+v, want ResourceID 43 with empty WeekDate (additive field absent)", items[1])
	}
}
