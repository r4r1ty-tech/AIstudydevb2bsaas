package capture

import (
	"strings"
	"testing"
)

func TestFFmpegArgsSplitRecordAndWake(t *testing.T) {
	t.Parallel()
	args := FFmpegArgs("/tmp/audio.ogg")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"asplit=2[rec][wake]",
		"libopus",
		"/tmp/audio.ogg",
		"pcm_s16le",
		"pipe:1",
		"16000",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}
