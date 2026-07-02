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
- **Resilient**: Graceful shutdown handling and error management.

## Project Structure

- `cmd/optimizer`: Main entry point.
- `internal/app`: Service orchestration (Polling, Worker).
- `internal/fetcher`: HTTP client and download logic.
- `internal/extractor`: Zip extraction with "Zip Slip" protection.
- `internal/processor`: FFmpeg wrapper.

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

