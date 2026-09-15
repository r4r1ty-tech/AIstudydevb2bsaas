package capture

import (
	"context"
	"io"
	"testing"
)

func TestFFmpegArgs(t *testing.T) {
	args := FFmpegArgs("/tmp/out.ogg")
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	for _, want := range []string{SinkName + ".monitor", "/tmp/out.ogg", "libopus", "pipe:1"} {
		if !contains(args, want) {
			t.Errorf("args missing %q: %v", want, joined)
		}
	}
}

func TestPulseEnv(t *testing.T) {
	env := PulseEnv()
	if !contains(env, "PULSE_SINK="+SinkName) || !contains(env, "PULSE_SOURCE="+SinkName+".monitor") {
		t.Fatalf("env = %v", env)
	}
}

func TestEnsureSinkWithoutPactl(t *testing.T) {
	t.Setenv("PATH", "")
	if err := EnsureSink(); err == nil {
		t.Fatal("expected error with empty PATH")
	}
}

func TestStartWithoutFFmpeg(t *testing.T) {
	t.Setenv("PATH", "")
	if _, err := Start(context.Background(), "/tmp/x.ogg"); err == nil {
		t.Fatal("expected error with empty PATH")
	}
}

func TestRecNilSafety(t *testing.T) {
	var nilRec *Rec
	if err := nilRec.Stop(); err != nil {
		t.Fatalf("nil rec stop: %v", err)
	}
	if _, err := nilRec.Read(make([]byte, 4)); err != io.EOF {
		t.Fatalf("nil rec read: %v", err)
	}
	r := &Rec{}
	if _, err := r.Read(make([]byte, 4)); err != io.EOF {
		t.Fatalf("empty rec read: %v", err)
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("empty rec stop: %v", err)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
