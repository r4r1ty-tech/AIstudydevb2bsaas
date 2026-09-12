package media

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CommandRunner allows mocking exec.Command for testing.
type CommandRunner func(ctx context.Context, name string, args ...string) error

func DefaultRunner(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type Recorder struct {
	VideoDir string
	AudioDir string
	Runner   CommandRunner
}

func NewRecorder(videoDir, audioDir string) *Recorder {
	if videoDir == "" {
		videoDir = "recordings/video"
	}
	if audioDir == "" {
		audioDir = "recordings/audio"
	}
	_ = os.MkdirAll(videoDir, 0755)
	_ = os.MkdirAll(audioDir, 0755)
	return &Recorder{
		VideoDir: videoDir,
		AudioDir: audioDir,
		Runner:   DefaultRunner,
	}
}

// ExtractAudio extracts audio from video file to ogg/mp3 format.
func (r *Recorder) ExtractAudio(ctx context.Context, videoPath string, audioFilename string) (string, error) {
	if audioFilename == "" {
		base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
		audioFilename = base + ".ogg"
	}

	outputPath := filepath.Join(r.AudioDir, audioFilename)

	// ffmpeg command to extract audio: ffmpeg -y -i input.mp4 -vn -c:a libopus output.ogg
	args := []string{
		"-y",
		"-i", videoPath,
		"-vn",
		"-c:a", "libopus",
		"-b:a", "64k",
		outputPath,
	}

	runner := r.Runner
	if runner == nil {
		runner = DefaultRunner
	}

	if err := runner(ctx, "ffmpeg", args...); err != nil {
		return "", fmt.Errorf("extract audio via ffmpeg failed: %w", err)
	}

	return outputPath, nil
}

// BuildFFmpegRecordArgs constructs args for display recording on Linux VDS.
func BuildFFmpegRecordArgs(display, outputPath string, durationSeconds int) []string {
	if display == "" {
		display = ":99"
	}
	return []string{
		"-y",
		"-f", "x11grab",
		"-video_size", "1280x720",
		"-framerate", "15",
		"-i", display,
		"-t", fmt.Sprintf("%d", durationSeconds),
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-crf", "28",
		outputPath,
	}
}
