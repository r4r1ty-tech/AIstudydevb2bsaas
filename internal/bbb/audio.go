package bbb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"golang.org/x/net/websocket"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

type stunsICE struct {
	URLs       []string
	Username   string
	Credential string
}

type sfuAudioSession struct {
	sfuWS        *websocket.Conn
	pc           *webrtc.PeerConnection
	wsMu         sync.Mutex
	sfuSuccess   atomic.Bool
	iceConnected atomic.Bool
	closed       atomic.Bool
	rtpPackets   atomic.Uint64
	lastRTPNano  atomic.Int64

	sampleRate atomic.Uint32
	channels   atomic.Uint32

	readyCh  chan struct{}
	errCh    chan string
	readDone chan struct{}
	cancel   context.CancelFunc
}

func (a *sfuAudioSession) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	a.wsMu.Lock()
	defer a.wsMu.Unlock()
	if a.closed.Load() || a.sfuWS == nil {
		return io.ErrClosedPipe
	}
	return websocket.Message.Send(a.sfuWS, string(b))
}

func (a *sfuAudioSession) alive() bool {
	if a == nil || a.closed.Load() {
		return false
	}
	select {
	case <-a.readDone:
		return false
	default:
	}
	if !a.sfuSuccess.Load() {
		return false
	}
	if a.pc == nil {
		return false
	}
	st := a.pc.ICEConnectionState()
	return st == webrtc.ICEConnectionStateConnected || st == webrtc.ICEConnectionStateCompleted || st == webrtc.ICEConnectionStateChecking
}

func (a *sfuAudioSession) stop() {
	if a == nil || !a.closed.CompareAndSwap(false, true) {
		return
	}
	_ = a.send(map[string]any{
		"id":   "stop",
		"type": "audio",
		"role": "recv",
	})
	a.cancel()
	if a.sfuWS != nil {
		_ = a.sfuWS.Close()
	}
	if a.pc != nil {
		_ = a.pc.Close()
	}
	<-a.readDone
}

// activeSegmentRec adapts *capture.OpusRec to the worker's recorder interface
// and automatically detaches itself from directSession on Stop.
type activeSegmentRec struct {
	sess *directSession
	opus *capture.OpusRec
	once sync.Once
}

func (r *activeSegmentRec) Read(p []byte) (int, error) {
	if r == nil || r.opus == nil {
		return 0, io.EOF
	}
	return r.opus.Read(p)
}

func (r *activeSegmentRec) Stop() error {
	if r == nil {
		return nil
	}
	var err error
	r.once.Do(func() {
		if r.sess != nil {
			r.sess.recMu.Lock()
			if r.sess.curRec == r {
				r.sess.curRec = nil
			}
			r.sess.recMu.Unlock()
		}
		if r.opus != nil {
			err = r.opus.Stop()
		}
	})
	return err
}

func (r *activeSegmentRec) writeRTP(pkt *rtp.Packet) {
	if r == nil || r.opus == nil {
		return
	}
	_ = r.opus.WriteRTP(pkt)
}

// StartRecording attaches an OGG/Opus segment writer (+ PCM wake stream) to the
// live WebRTC audio track of this session.
func (s *directSession) StartRecording(ctx context.Context, segPath string) (recorder, error) {
	if s == nil {
		return nil, fmt.Errorf("nil session")
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("session closed")
	}

	var rate uint32 = 48000
	var ch uint16 = 2
	s.audioMu.Lock()
	if s.audioSess != nil {
		if sr := s.audioSess.sampleRate.Load(); sr > 0 {
			rate = sr
		}
		if c := s.audioSess.channels.Load(); c > 0 {
			ch = uint16(c)
		}
	}
	s.audioMu.Unlock()

	opusRec, err := capture.StartOpus(ctx, segPath, rate, ch)
	if err != nil {
		return nil, err
	}
	rec := &activeSegmentRec{sess: s, opus: opusRec}

	s.recMu.Lock()
	old := s.curRec
	s.curRec = rec
	s.recMu.Unlock()
	if old != nil {
		_ = old.Stop()
	}

	// If session just exited lobby and audio isn't started yet, ensure it starts now.
	if s.req.Role == RoleRecord {
		s.EnsureAudio(ctx)
	}
	return rec, nil
}

// EnsureAudio checks that the listen-only WebRTC stream is alive for RoleRecord
// and reconnects to bbb-webrtc-sfu if needed.
func (s *directSession) EnsureAudio(ctx context.Context) bool {
	if s == nil || s.req.Role != RoleRecord {
		return true
	}
	s.mu.Lock()
	closed := s.closed
	lobby := s.inLobby
	room := s.inRoom
	s.mu.Unlock()
	if closed || lobby || !room {
		return !closed
	}

	if !s.audioMu.TryLock() {
		return true
	}
	defer s.audioMu.Unlock()

	if s.audioSess != nil && s.audioSess.alive() {
		return true
	}
	if s.audioSess != nil {
		s.audioSess.stop()
		s.audioSess = nil
	}

	if ctx == nil {
		ctx = s.ctx
	}
	actx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	as, err := s.startSFUAudio(actx)
	if err != nil {
		logx.Warnf("bbb", "EnsureAudio: start sfu failed: %v", err)
		return false
	}
	s.audioSess = as
	return as.sfuSuccess.Load() || as.iceConnected.Load()
}

func (s *directSession) stopAudio() {
	s.audioMu.Lock()
	as := s.audioSess
	s.audioSess = nil
	s.audioMu.Unlock()
	if as != nil {
		as.stop()
	}
	s.recMu.Lock()
	rec := s.curRec
	s.recMu.Unlock()
	if rec != nil {
		_ = rec.Stop()
	}
}

func (s *directSession) startSFUAudio(waitCtx context.Context) (*sfuAudioSession, error) {
	s.mu.Lock()
	scheme := "wss"
	if s.finalURL != nil && s.finalURL.Scheme == "http" {
		scheme = "ws"
	}
	host := s.wsHost
	base := s.baseURL
	tok := s.sessionToken
	finalURL := s.finalURL
	s.mu.Unlock()

	sfuURL := fmt.Sprintf("%s://%s/bbb-webrtc-sfu?sessionToken=%s", scheme, host, url.QueryEscape(tok))
	sfuWS, err := s.dialWebSocket(waitCtx, sfuURL, base, s.cookieHeaderFor(finalURL))
	if err != nil {
		return nil, fmt.Errorf("sfu dial: %w", err)
	}

	rawICE := s.fetchICE(waitCtx)
	var iceServers []webrtc.ICEServer
	for _, srv := range rawICE {
		cfg := webrtc.ICEServer{URLs: srv.URLs}
		if srv.Username != "" {
			cfg.Username = srv.Username
			cfg.Credential = srv.Credential
			cfg.CredentialType = webrtc.ICECredentialTypePassword
		}
		iceServers = append(iceServers, cfg)
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		_ = sfuWS.Close()
		return nil, fmt.Errorf("peer connection: %w", err)
	}

	actx, acancel := context.WithCancel(s.ctx)
	as := &sfuAudioSession{
		sfuWS:    sfuWS,
		pc:       pc,
		readyCh:  make(chan struct{}),
		errCh:    make(chan string, 1),
		readDone: make(chan struct{}),
		cancel:   acancel,
	}

	var readyOnce sync.Once
	markReady := func() {
		if as.sfuSuccess.Load() && as.iceConnected.Load() {
			readyOnce.Do(func() { close(as.readyCh) })
		}
	}

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		logx.Debugf("bbb", "sfu ice state=%s", state.String())
		if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
			as.iceConnected.Store(true)
			markReady()
		}
	})

	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		codec := track.Codec()
		sr := codec.ClockRate
		if sr == 0 {
			sr = 48000
		}
		ch := codec.Channels
		if ch == 0 {
			ch = 2
		}
		as.sampleRate.Store(sr)
		as.channels.Store(uint32(ch))
		logx.Infof("bbb", "sfu track kind=%s codec=%s rate=%d ch=%d ssrc=%d",
			track.Kind().String(), codec.MimeType, sr, ch, track.SSRC())

		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					logx.Debugf("bbb", "sfu track ReadRTP: %v", err)
				}
				return
			}
			as.rtpPackets.Add(1)
			as.lastRTPNano.Store(time.Now().UnixNano())
			s.recMu.Lock()
			rec := s.curRec
			s.recMu.Unlock()
			if rec != nil {
				rec.writeRTP(pkt)
			}
		}
	})

	type sfuMsg struct {
		ID        string          `json:"id"`
		Response  string          `json:"response,omitempty"`
		SDPAnswer string          `json:"sdpAnswer,omitempty"`
		Candidate json.RawMessage `json:"candidate,omitempty"`
		Reason    string          `json:"reason,omitempty"`
	}

	var negotiated atomic.Bool
	var iceMu sync.Mutex
	var iceQueue []webrtc.ICECandidateInit

	flushICE := func() {
		iceMu.Lock()
		q := iceQueue
		iceQueue = nil
		iceMu.Unlock()
		for _, c := range q {
			_ = pc.AddICECandidate(c)
		}
	}

	setErr := func(msg string) {
		select {
		case as.errCh <- msg:
		default:
		}
	}

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-actx.Done():
				return
			case <-ticker.C:
				_ = as.send(map[string]any{"id": "ping"})
			}
		}
	}()

	go func() {
		defer close(as.readDone)
		for {
			if actx.Err() != nil {
				return
			}
			_ = sfuWS.SetReadDeadline(time.Now().Add(45 * time.Second))
			var raw string
			if err := websocket.Message.Receive(sfuWS, &raw); err != nil {
				if strings.Contains(err.Error(), "timeout") {
					continue
				}
				return
			}
			var msg sfuMsg
			if err := json.Unmarshal([]byte(raw), &msg); err != nil {
				continue
			}
			switch msg.ID {
			case "pong":
			case "startResponse":
				if msg.Response != "accepted" {
					setErr("startResponse rejected: " + msg.Reason)
					continue
				}
				if err := pc.SetRemoteDescription(webrtc.SessionDescription{
					Type: webrtc.SDPTypeOffer,
					SDP:  msg.SDPAnswer,
				}); err != nil {
					setErr("SetRemoteDescription: " + err.Error())
					continue
				}
				ans, err := pc.CreateAnswer(nil)
				if err != nil {
					setErr("CreateAnswer: " + err.Error())
					continue
				}
				gatherDone := webrtc.GatheringCompletePromise(pc)
				if err := pc.SetLocalDescription(ans); err != nil {
					setErr("SetLocalDescription: " + err.Error())
					continue
				}
				select {
				case <-gatherDone:
				case <-time.After(4 * time.Second):
				case <-actx.Done():
					return
				}
				negotiated.Store(true)
				flushICE()
				if err := as.send(map[string]any{
					"id":       "subscriberAnswer",
					"type":     "audio",
					"role":     "recv",
					"sdpOffer": pc.LocalDescription().SDP,
				}); err != nil {
					setErr("subscriberAnswer: " + err.Error())
				}
			case "iceCandidate":
				if len(msg.Candidate) > 0 && string(msg.Candidate) != "null" {
					var init webrtc.ICECandidateInit
					if msg.Candidate[0] == '"' {
						var cand string
						if json.Unmarshal(msg.Candidate, &cand) == nil {
							init.Candidate = cand
						}
					} else {
						_ = json.Unmarshal(msg.Candidate, &init)
					}
					if init.Candidate != "" {
						if negotiated.Load() {
							_ = pc.AddICECandidate(init)
						} else {
							iceMu.Lock()
							iceQueue = append(iceQueue, init)
							iceMu.Unlock()
						}
					}
				}
			case "webRTCAudioSuccess":
				as.sfuSuccess.Store(true)
				markReady()
			case "webRTCAudioError", "error":
				setErr(raw)
			}
		}
	}()

	_ = as.send(map[string]any{"id": "ping"})
	if err := as.send(map[string]any{
		"id":                  "start",
		"type":                "audio",
		"role":                "recv",
		"clientSessionNumber": 1,
		"extension":           nil,
	}); err != nil {
		as.stop()
		return nil, fmt.Errorf("sfu start: %w", err)
	}

	select {
	case <-as.readyCh:
		logx.Infof("bbb", "sfu listen-only audio connected")
		return as, nil
	case errStr := <-as.errCh:
		as.stop()
		return nil, fmt.Errorf("sfu error: %s", errStr)
	case <-as.readDone:
		as.stop()
		return nil, fmt.Errorf("sfu closed during setup")
	case <-waitCtx.Done():
		as.stop()
		return nil, fmt.Errorf("sfu timeout: %w", waitCtx.Err())
	}
}
