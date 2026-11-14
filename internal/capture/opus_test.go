package capture

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func dummyOpusPacket(seq uint16, ts uint32) *rtp.Packet {
	// Valid 1-frame Opus TOC byte (0xF8 = config 31, 20ms, stereo, code 0) + silent frame bytes.
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           0x12345678,
		},
		Payload: []byte{0xF8, 0xFF, 0xFE},
	}
}

func TestOpusRecWritesOGGWithoutFFmpeg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	out := filepath.Join(t.TempDir(), "seg", "audio-1.ogg")
	rec, err := StartOpus(context.Background(), out, 48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := rec.WriteRTP(dummyOpusPacket(uint16(i+1), uint32(i*960))); err != nil {
			t.Fatal(err)
		}
	}
	n, err := rec.Read(make([]byte, 64))
	if n != 0 || err != io.EOF {
		t.Fatalf("expected EOF without ffmpeg, got n=%d err=%v", n, err)
	}
	if err := rec.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := rec.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	pkts, bytes := rec.Stats()
	if pkts != 10 || bytes != 30 {
		t.Fatalf("stats = (%d, %d)", pkts, bytes)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() < 100 {
		t.Fatalf("ogg file invalid: stat=%v err=%v", st, err)
	}
}

func TestOpusRecNilSafety(t *testing.T) {
	var rec *OpusRec
	if err := rec.Stop(); err != nil {
		t.Fatalf("nil Stop: %v", err)
	}
	if err := rec.WriteRTP(dummyOpusPacket(1, 0)); err != nil {
		t.Fatalf("nil WriteRTP: %v", err)
	}
	n, err := rec.Read(make([]byte, 16))
	if n != 0 || err != io.EOF {
		t.Fatalf("nil Read: n=%d err=%v", n, err)
	}
}

func TestOpusRecDecodesPCMWithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg installed")
	}
	out := filepath.Join(t.TempDir(), "audio-1.ogg")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rec, err := StartOpus(ctx, out, 48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := rec.WriteRTP(dummyOpusPacket(uint16(i+1), uint32(i*960))); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Stop(); err != nil {
		t.Fatal(err)
	}
	pcm, err := io.ReadAll(rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) == 0 {
		t.Fatal("expected decoded PCM bytes from ffmpeg")
	}
	dur, _, err := Probe(ctx, out)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if dur < 500*time.Millisecond {
		t.Fatalf("duration = %s, want >= 500ms", dur)
	}
}
