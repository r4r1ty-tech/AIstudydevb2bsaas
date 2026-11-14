package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// WakeRate is the 16 kHz mono PCM sample rate expected by Vosk and LevelMeter.
const WakeRate = 16000

var stopGrace = 5 * time.Second

// OpusRec writes incoming WebRTC Opus RTP packets directly to an OGG file on
// disk and optionally decodes a parallel 16 kHz mono s16le PCM stream via
// ffmpeg (without PulseAudio) for the wake-word spotter and silence meter.
type OpusRec struct {
	path string

	mu      sync.Mutex
	stopped bool
	diskOgg *oggwriter.OggWriter
	pcmCh   chan *rtp.Packet

	packets atomic.Uint64
	bytes   atomic.Uint64

	pcmOut   io.ReadCloser
	pcmDone  chan struct{}
	cmd      *exec.Cmd
	waitDone chan error
	cancel   context.CancelFunc
}

// StartOpus opens path for direct OGG/Opus recording and starts an optional
// ffmpeg pipe decoder to expose 16 kHz PCM via Read.
func StartOpus(ctx context.Context, outPath string, sampleRate uint32, channels uint16) (*OpusRec, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sampleRate == 0 {
		sampleRate = 48000
	}
	if channels == 0 {
		channels = 2
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return nil, fmt.Errorf("StartOpus: mkdir: %w", err)
	}
	diskOgg, err := oggwriter.New(outPath, sampleRate, channels)
	if err != nil {
		return nil, fmt.Errorf("StartOpus: oggwriter: %w", err)
	}

	r := &OpusRec{
		path:    outPath,
		diskOgg: diskOgg,
	}

	if ffmpeg, err := exec.LookPath("ffmpeg"); err == nil {
		r.initPCMDecoder(ctx, ffmpeg, sampleRate, channels)
	} else {
		logx.Debugf("capture", "StartOpus: ffmpeg not found (%v), PCM wake stream disabled", err)
	}
	logx.Infof("capture", "opus rec started -> %s (%dHz ch=%d pcm=%v)", outPath, sampleRate, channels, r.pcmOut != nil)
	return r, nil
}

func (r *OpusRec) initPCMDecoder(ctx context.Context, ffmpeg string, sampleRate uint32, channels uint16) {
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, ffmpeg,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "ogg", "-i", "pipe:0",
		"-ac", "1", "-ar", fmt.Sprintf("%d", WakeRate),
		"-c:a", "pcm_s16le", "-f", "s16le", "pipe:1",
	)
	cmd.Stderr = logx.LineWriter(logx.LevelWarn, "capture", "ffmpeg-pcm: ", nil)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		logx.Debugf("capture", "initPCMDecoder: stdin pipe: %v", err)
		return
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		logx.Debugf("capture", "initPCMDecoder: stdout pipe: %v", err)
		return
	}
	cmd.Stdout = stdoutW
	pcmOgg, err := oggwriter.NewWith(stdin, sampleRate, channels)
	if err != nil {
		_ = stdin.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		cancel()
		logx.Debugf("capture", "initPCMDecoder: pcm oggwriter: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		_ = pcmOgg.Close()
		_ = stdin.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		cancel()
		logx.Debugf("capture", "initPCMDecoder: start ffmpeg: %v", err)
		return
	}
	_ = stdoutW.Close()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	pcmCh := make(chan *rtp.Packet, 256)
	pcmDone := make(chan struct{})
	go func() {
		defer close(pcmDone)
		defer func() {
			_ = pcmOgg.Close()
			_ = stdin.Close()
		}()
		for pkt := range pcmCh {
			if err := pcmOgg.WriteRTP(pkt); err != nil {
				logx.Debugf("capture", "pcmOgg.WriteRTP: %v", err)
				return
			}
		}
	}()

	r.pcmCh = pcmCh
	r.pcmDone = pcmDone
	r.pcmOut = stdoutR
	r.cmd = cmd
	r.waitDone = waitDone
	r.cancel = cancel
}

// WriteRTP writes a single Opus RTP packet to the OGG file and forwards a
// copy to the optional PCM decoder queue without blocking disk writes.
func (r *OpusRec) WriteRTP(pkt *rtp.Packet) error {
	if r == nil || pkt == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.diskOgg == nil {
		return io.ErrClosedPipe
	}
	if err := r.diskOgg.WriteRTP(pkt); err != nil {
		return err
	}
	r.packets.Add(1)
	r.bytes.Add(uint64(len(pkt.Payload)))
	if r.pcmCh != nil {
		// Copy header/payload so downstream OGG writers cannot race on packet mutation.
		cp := *pkt
		if len(pkt.Payload) > 0 {
			cp.Payload = append([]byte(nil), pkt.Payload...)
		}
		select {
		case r.pcmCh <- &cp:
		default:
		}
	}
	return nil
}

// Read reads decoded 16 kHz mono s16le PCM bytes for the wake-word spotter.
func (r *OpusRec) Read(p []byte) (int, error) {
	if r == nil {
		return 0, io.EOF
	}
	r.mu.Lock()
	pcmOut := r.pcmOut
	r.mu.Unlock()
	if pcmOut == nil {
		return 0, io.EOF
	}

	n, err := pcmOut.Read(p)
	if err != nil {
		r.mu.Lock()
		stopped := r.stopped
		r.mu.Unlock()
		if stopped && errors.Is(err, os.ErrClosed) {
			return n, io.EOF
		}
	}
	return n, err
}

// Stats returns total RTP packets and payload bytes written to disk.
func (r *OpusRec) Stats() (packets, payloadBytes uint64) {
	if r == nil {
		return 0, 0
	}
	return r.packets.Load(), r.bytes.Load()
}

// Stop flushes the OGG trailer to disk and terminates the PCM decoder pipe.
func (r *OpusRec) Stop() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.stopped = true
	diskOgg := r.diskOgg
	r.diskOgg = nil
	pcmCh := r.pcmCh
	r.pcmCh = nil
	r.mu.Unlock()

	if pcmCh != nil {
		close(pcmCh)
	}
	if r.pcmDone != nil {
		<-r.pcmDone
	}
	if r.waitDone != nil {
		select {
		case <-r.waitDone:
		case <-time.After(stopGrace):
			if r.cancel != nil {
				r.cancel()
			}
			if r.cmd != nil && r.cmd.Process != nil {
				_ = r.cmd.Process.Kill()
			}
			<-r.waitDone
		}
	}
	if r.cancel != nil {
		r.cancel()
	}

	r.mu.Lock()
	pcmOut := r.pcmOut
	r.pcmOut = nil
	r.mu.Unlock()
	if pcmOut != nil {
		_ = pcmOut.Close()
	}

	var closeErr error
	if diskOgg != nil {
		closeErr = diskOgg.Close()
	}
	if st, err := os.Stat(r.path); err == nil {
		logx.Infof("capture", "OpusRec.Stop: %s size=%d packets=%d", r.path, st.Size(), r.packets.Load())
	} else {
		logx.Warnf("capture", "OpusRec.Stop: stat %s: %v", r.path, err)
	}
	return closeErr
}
