package media

import (
	"context"
	"path/filepath"
	"testing"
)

func TestExtractAudio(t *testing.T) {
	tmpDir := t.TempDir()
	videoDir := filepath.Join(tmpDir, "video")
	audioDir := filepath.Join(tmpDir, "audio")

	var executedName string
	var executedArgs []string

	mockRunner := func(ctx context.Context, name string, args ...string) error {
		executedName = name
		executedArgs = args
		return nil
	}

	rec := NewRecorder(videoDir, audioDir)
	rec.Runner = mockRunner

	videoPath := filepath.Join(videoDir, "lecture1.mp4")
	audioPath, err := rec.ExtractAudio(context.Background(), videoPath, "lecture1.ogg")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedAudioPath := filepath.Join(audioDir, "lecture1.ogg")
	if audioPath != expectedAudioPath {
		t.Errorf("expected %s, got %s", expectedAudioPath, audioPath)
	}

	if executedName != "ffmpeg" {
		t.Errorf("expected command ffmpeg, got %s", executedName)
	}

	if len(executedArgs) == 0 || executedArgs[2] != videoPath {
		t.Errorf("expected ffmpeg input to be %s, got args %v", videoPath, executedArgs)
	}
}

func TestBuildFFmpegRecordArgs(t *testing.T) {
	args := BuildFFmpegRecordArgs(":99", "out.mp4", 3600)
	if len(args) == 0 {
		t.Fatal("empty args")
	}
	if args[2] != "x11grab" {
		t.Errorf("expected format x11grab, got %s", args[2])
	}
}
