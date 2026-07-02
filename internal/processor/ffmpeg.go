package processor

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
)

type EncodeParams struct {
	VideoCodec  string
	VideoCRF    int
	VideoPreset string
	AudioCodec  string
	MaxThreads  int
}

// Encoder abstracts the actual transcoding step. Production code uses
// FFmpegEncoder; tests can supply a fake implementation to exercise the
// surrounding orchestration (naming, sidecar writing, dedup/history) without
// invoking the real ffmpeg binary.
type Encoder interface {
	Encode(ctx context.Context, src, dest string, params EncodeParams) error
}

// FFmpegEncoder is the production Encoder: it shells out to ffmpeg via
// ProcessVideo.
type FFmpegEncoder struct{}

func (FFmpegEncoder) Encode(ctx context.Context, src, dest string, params EncodeParams) error {
	return ProcessVideo(ctx, src, dest, params)
}

// ProcessVideo runs ffmpeg to re-encode src to dest with given params
func ProcessVideo(ctx context.Context, src, dest string, params EncodeParams) error {
	args := []string{
		"-y", // overwrite output
		"-i", src,
		"-c:v", params.VideoCodec,
		"-crf", strconv.Itoa(params.VideoCRF),
		"-preset", params.VideoPreset,
		"-c:a", params.AudioCodec,
	}

	if params.MaxThreads > 0 {
		args = append(args, "-threads", strconv.Itoa(params.MaxThreads))
	}

	// For specific codecs, we might need extra params (e.g. tag:v hvc1 for apple compatibility with hevc)
	// keeping it simple as per spec for now.
	if params.VideoCodec == "libx265" {
		args = append(args, "-tag:v", "hvc1") 
	}

	args = append(args, dest)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	
	// Capture stderr as ffmpeg logs there
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("FFmpeg failed", "output", string(output), "error", err)
		return fmt.Errorf("ffmpeg failed: %w", err)
	}

	return nil
}
