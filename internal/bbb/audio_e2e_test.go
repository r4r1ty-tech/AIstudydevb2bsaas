package bbb

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
)

// TestRecordTabAudioReachesFFmpeg drives the real recording path: headless
// Chromium with PULSE_SINK plays a tone, ffmpeg records ssau_rec.monitor, and
// the PCM that the spotter reads must not be silence. It creates sinks and
// sets the default sink, so it runs only with SSAU_AUDIO_E2E=1 against a
// throwaway PulseAudio (see docs/TESTING.md), never the one on the VDS.
func TestRecordTabAudioReachesFFmpeg(t *testing.T) {
	if os.Getenv("SSAU_AUDIO_E2E") != "1" {
		t.Skip("set SSAU_AUDIO_E2E=1 with an isolated PulseAudio")
	}
	bin := FindChrome("")
	if bin == "" {
		t.Skip("no chromium")
	}
	for _, tool := range []string{"ffmpeg", "pactl", "pulseaudio"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	if err := capture.EnsureSink(); err != nil {
		t.Skipf("pulse not usable here: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><div data-test="userListItem">room</div>
<button data-test="listenOnlyBtn" onclick="
  const ac = new AudioContext(); const o = ac.createOscillator();
  o.frequency.value = 440; o.connect(ac.destination); o.start();
  document.body.dataset.playing = '1'">Listen only</button>`)
	}))
	t.Cleanup(srv.Close)

	j := NewChromeJoiner(bin)
	t.Cleanup(func() { _ = j.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sess, err := j.Join(ctx, JoinReq{URL: srv.URL + "/", FIO: "Тест", Role: RoleRecord})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	cs := sess.(*chromeSession)
	res, err := cs.page.Eval(`() => document.body.dataset.playing || ''`)
	if err != nil || res.Value.Str() != "1" {
		t.Fatalf("listen-only was not clicked: %v %q", err, res.Value.Str())
	}

	rec, err := capture.Start(ctx, filepath.Join(t.TempDir(), "audio-1.ogg"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rec.Stop() })

	pcm := make([]byte, capture.WakeRate*2*2) // 2s
	if _, err := io.ReadFull(rec, pcm); err != nil {
		t.Fatal(err)
	}
	peak := capture.Peak(pcm[capture.WakeRate:]) // skip the first 0.5s
	t.Logf("peak=%d (%.1f dBFS)", peak, capture.DBFS(peak))
	if peak < capture.SilencePeak {
		capture.LogRoute("test", "silent")
		t.Fatalf("recording is silent: peak=%d — Chromium audio does not reach %s.monitor", peak, capture.SinkName)
	}
}
