# Video Optimizer Microservice

A high-efficiency, containerized microservice for automated video optimization using Go and FFmpeg.

## Features

- **Automated Ingestion**: Polls a remote endpoint for new content (`.zip`, `.mkv`, `.mp4`).
- **Efficient Processing**: 
  - Queued FIFO processing to prevent resource exhaustion.
  - Automatic ZIP extraction and cleanup.
  - Low memory footprint (Go binary).
- **Video Optimization**: 
  - Re-encodes to HEVC (H.265) by default.
  - Configurable quality (CRF) and preset.
- **Structured Storage**: Organizes output by ISO Week (e.g., `2026-W05`).
- **Provenance metadata**: every artifact is matched back to its source
  resource by id, not by filename (see below).
- **Resilient**: Graceful shutdown handling and error management.

## Project Structure

- `cmd/optimizer`: Main entry point.
- `internal/app`: Service orchestration (Polling, Worker).
- `internal/fetcher`: HTTP client and download logic.
- `internal/extractor`: Zip extraction with "Zip Slip" protection.
- `internal/processor`: FFmpeg wrapper (behind an `Encoder` interface so it
  can be faked in tests).
- `internal/naming`: Canonical artifact filename + slug generation.
- `internal/sidecar`: Per-artifact `.meta.json` provenance sidecar.

## Provenance metadata (matching per provenienza)

Every polled resource carries `id`/`category`/`title` from the source API.
Each video it produces (the file itself, or one extracted from a zip)
inherits that provenance and is named:

```
{category}_{resource_id}_{n}_{slug}.mp4
```

`n` is a 1-based progressive index (a zip with several videos yields several
artifacts); `slug` is a human-readable, `[a-z0-9-]`-only slug (max 40 chars)
derived from the original filename, falling back to the resource title.

Next to every artifact, once encoding succeeds, a sidecar
`<artifact>.meta.json` is written atomically (temp file + rename in the same
directory):

```json
{
  "schema": 1,
  "resource_id": 42,
  "category": "vga",
  "source_url": "https://...",
  "original_filename": "nome interno allo zip.mp4",
  "artifact": "vga_42_1_decime-luglio.mp4",
  "size_bytes": 12345678,
  "encoded_at": "2026-07-02T13:45:00Z",
  "codec": "libx265",
  "crf": 27
}
```

Downstream consumers (mail-parser) match artifacts to resources by reading
`resource_id` from the sidecar instead of parsing filenames.

## Configuration

The service is configured entirely via environment variables:

| Variable | Description | Default |
|----------|-------------|---------|
| `SOURCE_ENDPOINT` | **Required**. URL returning JSON list of files `[{"url": "...", "filename": "..."}]`. | N/A |
| `OUTPUT_ROOT` | Path to mount the partial volume. | `/data/output` |
| `POLLING_INTERVAL` | Seconds between poll cycles. | `300` |
| `VIDEO_CODEC` | Target video codec (e.g., `libx265`, `libsvtav1`). | `libx265` |
| `VIDEO_CRF` | Content Rate Factor (Quality). Lower is better. | `27` |
| `MAX_THREADS` | Max CPU threads for FFmpeg (0 = auto). | `0` |

## Deployment

### Docker

1. **Build the image**:
   ```bash
   docker build -t video-optimizer .
   ```

2. **Run the container**:
   ```bash
   docker run -d \
     --name video-optimizer \
     -e SOURCE_ENDPOINT="https://example.com/api/files" \
     -v $(pwd)/output:/data/output \
     video-optimizer
   ```

### Local Development

Prerequisites: Go 1.21+, FFmpeg 6.0+.

```bash
export SOURCE_ENDPOINT="https://mock.api/files"
go run ./cmd/optimizer
```

## Security

- Runs as non-root user (`appuser`) inside the container.
- Validates Zip paths to prevent directory traversal attacks.
- cleans up temporary files immediately after processing.

