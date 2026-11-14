package bbb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"golang.org/x/net/websocket"
)

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

func sendSockJS(ws *websocket.Conn, obj any) {
	b, _ := json.Marshal(obj)
	arr, _ := json.Marshal([]string{string(b)})
	_ = websocket.Message.Send(ws, "a"+string(arr))
}

func TestDirectJoinGreetSlidesAndLeave(t *testing.T) {
	var (
		mu          sync.Mutex
		greetText   string
		greetEmph   bool
		leftMeeting bool
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/b/room-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><head><meta name="csrf-token" content="tok-csrf"></head>
<body><form method="post" action="/b/room-1">
<input type="text" name="/b/room-1[join_name]" required>
<button type="submit">Join</button></form></body></html>`)
			return
		}
		_ = r.ParseForm()
		if r.FormValue("authenticity_token") != "tok-csrf" || r.FormValue("/b/room-1[join_name]") != "Иван Петров" {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/html5client/join?sessionToken=st-1", http.StatusFound)
	})
	mux.HandleFunc("/html5client/join", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/bigbluebutton/api/enter", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"returncode":     "SUCCESS",
				"fullname":       "Иван Петров",
				"meetingID":      "m-1",
				"externUserID":   "ext-1",
				"internalUserID": "int-1",
				"authToken":      "auth-1",
				"guestStatus":    "ALLOW",
			},
		})
	})
	mux.HandleFunc("/bigbluebutton/api/stuns", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"stunServers": []any{}, "turnServers": []any{}})
	})
	mux.HandleFunc("/slide/png/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	mux.HandleFunc("/slide/png/2", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/slide/svg/2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="80"><rect width="100" height="80" fill="blue"/></svg>`)
	})
	mux.Handle("/html5client/sockjs/123/abcdefgh/websocket", websocket.Handler(func(ws *websocket.Conn) {
		_ = websocket.Message.Send(ws, "o")
		for {
			var frame string
			if err := websocket.Message.Receive(ws, &frame); err != nil {
				return
			}
			var msgs []string
			if err := json.Unmarshal([]byte(frame), &msgs); err != nil {
				continue
			}
			for _, raw := range msgs {
				var m map[string]any
				_ = json.Unmarshal([]byte(raw), &m)
				switch m["msg"] {
				case "connect":
					sendSockJS(ws, map[string]any{"msg": "connected", "session": "s-1"})
				case "method":
					id, _ := m["id"].(string)
					switch m["method"] {
					case "validateAuthToken":
						sendSockJS(ws, map[string]any{
							"msg":    "result",
							"id":     id,
							"result": map[string]any{"validationStatus": 3},
						})
					case "sendGroupChatMsg":
						params, _ := m["params"].([]any)
						if len(params) == 2 {
							if body, ok := params[1].(map[string]any); ok {
								mu.Lock()
								greetText, _ = body["message"].(string)
								greetEmph, _ = body["chatEmphasizedText"].(bool)
								mu.Unlock()
								corr, _ := body["correlationId"].(string)
								sendSockJS(ws, map[string]any{
									"msg":        "added",
									"collection": "group-chat-msg",
									"id":         "msg-1",
									"fields":     map[string]any{"correlationId": corr},
								})
							}
						}
						sendSockJS(ws, map[string]any{"msg": "result", "id": id})
					case "userLeftMeeting":
						mu.Lock()
						leftMeeting = true
						mu.Unlock()
						sendSockJS(ws, map[string]any{"msg": "result", "id": id})
					}
				case "sub":
					subID, _ := m["id"].(string)
					name, _ := m["name"].(string)
					if name == "presentations" {
						sendSockJS(ws, map[string]any{
							"msg": "added", "collection": "presentations", "id": "p-doc-1",
							"fields": map[string]any{"id": "pres-1", "name": "deck.pdf", "current": true},
						})
					}
					if name == "slides" {
						sendSockJS(ws, map[string]any{
							"msg": "added", "collection": "slides", "id": "s-doc-1",
							"fields": map[string]any{"id": "s-1", "presentationId": "pres-1", "num": 1, "imageUri": "/slide/svg/1"},
						})
						sendSockJS(ws, map[string]any{
							"msg": "added", "collection": "slides", "id": "s-doc-2",
							"fields": map[string]any{"id": "s-2", "presentationId": "pres-1", "num": 2, "imageUri": "/slide/svg/2"},
						})
						// Replace /svg/1 test so PNG shortcut is exercised cleanly.
						sendSockJS(ws, map[string]any{
							"msg": "changed", "collection": "slides", "id": "s-doc-1",
							"fields": map[string]any{"imageUri": "/slide/png/1"},
						})
					}
					sendSockJS(ws, map[string]any{"msg": "ready", "subs": []string{subID}})
				}
			}
		}
	}))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	j := NewDirectJoiner()
	defer j.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sess, err := j.Join(ctx, JoinReq{URL: srv.URL + "/b/room-1", FIO: "Иван Петров", Role: RolePresence})
	if err != nil {
		t.Fatal(err)
	}
	if lobby, err := sess.InLobby(ctx); err != nil || lobby {
		t.Fatalf("InLobby = (%v, %v)", lobby, err)
	}
	if room, err := sess.InRoom(ctx); err != nil || !room {
		t.Fatalf("InRoom = (%v, %v)", room, err)
	}
	if err := sess.Greet(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotMsg, gotEmph := greetText, greetEmph
	mu.Unlock()
	if gotMsg != helloText || gotEmph {
		t.Fatalf("Greet payload = (%q, %v)", gotMsg, gotEmph)
	}

	slidesDir := t.TempDir()
	n, err := sess.GrabSlides(ctx, slidesDir)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("GrabSlides = %d, want >= 1", n)
	}
	if st, serr := os.Stat(filepath.Join(slidesDir, "01.png")); serr != nil || st.Size() != int64(len(tinyPNG)) {
		t.Fatalf("01.png invalid: stat=%v err=%v", st, serr)
	}

	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	left := leftMeeting
	mu.Unlock()
	if !left {
		t.Fatal("userLeftMeeting was not sent on Close")
	}
}

func TestDirectLobbyPromotion(t *testing.T) {
	var allow atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/room", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/bigbluebutton/api/guestWait?sessionToken=tok-lobby", http.StatusFound)
	})
	mux.HandleFunc("/bigbluebutton/api/guestWait", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "waiting for moderator")
	})
	mux.HandleFunc("/bigbluebutton/api/enter", func(w http.ResponseWriter, r *http.Request) {
		status := "WAIT"
		if allow.Load() {
			status = "ALLOW"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"returncode":     "SUCCESS",
				"fullname":       "Гость",
				"meetingID":      "m-2",
				"externUserID":   "ext-2",
				"internalUserID": "int-2",
				"authToken":      "auth-2",
				"guestStatus":    status,
			},
		})
	})
	mux.HandleFunc("/bigbluebutton/api/stuns", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	mux.Handle("/html5client/sockjs/123/abcdefgh/websocket", websocket.Handler(func(ws *websocket.Conn) {
		_ = websocket.Message.Send(ws, "o")
		for {
			var frame string
			if err := websocket.Message.Receive(ws, &frame); err != nil {
				return
			}
			var msgs []string
			if err := json.Unmarshal([]byte(frame), &msgs); err != nil {
				continue
			}
			for _, raw := range msgs {
				var m map[string]any
				_ = json.Unmarshal([]byte(raw), &m)
				switch m["msg"] {
				case "connect":
					sendSockJS(ws, map[string]any{"msg": "connected", "session": "s-2"})
				case "method":
					id, _ := m["id"].(string)
					sendSockJS(ws, map[string]any{"msg": "result", "id": id})
				case "sub":
					subID, _ := m["id"].(string)
					sendSockJS(ws, map[string]any{"msg": "ready", "subs": []string{subID}})
				}
			}
		}
	}))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	j := NewDirectJoiner()
	defer j.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := j.Join(ctx, JoinReq{URL: srv.URL + "/room", FIO: "Гость", Role: RolePresence})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if lobby, err := sess.InLobby(ctx); err != nil || !lobby {
		t.Fatalf("expected lobby=true, got (%v, %v)", lobby, err)
	}
	if room, err := sess.InRoom(ctx); err != nil || room {
		t.Fatalf("expected room=false, got (%v, %v)", room, err)
	}

	allow.Store(true)
	if lobby, err := sess.InLobby(ctx); err != nil || lobby {
		t.Fatalf("expected lobby=false after allow, got (%v, %v)", lobby, err)
	}
	if room, err := sess.InRoom(ctx); err != nil || !room {
		t.Fatalf("expected room=true after allow, got (%v, %v)", room, err)
	}
}

func TestDirectSFUAudioAndRecording(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/html5client/join?sessionToken=tok-audio", http.StatusFound)
	})
	mux.HandleFunc("/html5client/join", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/bigbluebutton/api/enter", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"returncode":     "SUCCESS",
				"fullname":       "Рекордер",
				"meetingID":      "m-audio",
				"externUserID":   "ext-a",
				"internalUserID": "int-a",
				"authToken":      "auth-a",
				"guestStatus":    "ALLOW",
			},
		})
	})
	mux.HandleFunc("/bigbluebutton/api/stuns", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	mux.Handle("/html5client/sockjs/123/abcdefgh/websocket", websocket.Handler(func(ws *websocket.Conn) {
		_ = websocket.Message.Send(ws, "o")
		for {
			var frame string
			if err := websocket.Message.Receive(ws, &frame); err != nil {
				return
			}
			var msgs []string
			if err := json.Unmarshal([]byte(frame), &msgs); err != nil {
				continue
			}
			for _, raw := range msgs {
				var m map[string]any
				_ = json.Unmarshal([]byte(raw), &m)
				switch m["msg"] {
				case "connect":
					sendSockJS(ws, map[string]any{"msg": "connected", "session": "s-a"})
				case "method":
					id, _ := m["id"].(string)
					sendSockJS(ws, map[string]any{"msg": "result", "id": id})
				}
			}
		}
	}))
	mux.Handle("/bbb-webrtc-sfu", websocket.Handler(func(ws *websocket.Conn) {
		pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			return
		}
		defer pc.Close()

		track, err := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
			"audio",
			"sfu-stream",
		)
		if err != nil {
			return
		}
		if _, err := pc.AddTrack(track); err != nil {
			return
		}

		done := make(chan struct{})
		defer close(done)

		pc.OnICEConnectionStateChange(func(st webrtc.ICEConnectionState) {
			if st == webrtc.ICEConnectionStateConnected || st == webrtc.ICEConnectionStateCompleted {
				go func() {
					ticker := time.NewTicker(20 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-done:
							return
						case <-ticker.C:
							_ = track.WriteSample(media.Sample{
								Data:     []byte{0xF8, 0xFF, 0xFE},
								Duration: 20 * time.Millisecond,
							})
						}
					}
				}()
			}
		})

		for {
			var raw string
			if err := websocket.Message.Receive(ws, &raw); err != nil {
				return
			}
			var msg map[string]any
			_ = json.Unmarshal([]byte(raw), &msg)
			switch msg["id"] {
			case "ping":
				_ = websocket.Message.Send(ws, `{"id":"pong"}`)
			case "start":
				offer, err := pc.CreateOffer(nil)
				if err != nil {
					return
				}
				gatherDone := webrtc.GatheringCompletePromise(pc)
				if err := pc.SetLocalDescription(offer); err != nil {
					return
				}
				<-gatherDone
				resp, _ := json.Marshal(map[string]any{
					"id":        "startResponse",
					"type":      "audio",
					"role":      "recv",
					"response":  "accepted",
					"sdpAnswer": pc.LocalDescription().SDP,
				})
				_ = websocket.Message.Send(ws, string(resp))
			case "subscriberAnswer":
				sdp, _ := msg["sdpOffer"].(string)
				if err := pc.SetRemoteDescription(webrtc.SessionDescription{
					Type: webrtc.SDPTypeAnswer,
					SDP:  sdp,
				}); err != nil {
					return
				}
				_ = websocket.Message.Send(ws, `{"type":"audio","id":"webRTCAudioSuccess","success":"MEDIA_FLOWING"}`)
			case "stop":
				return
			}
		}
	}))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	j := NewDirectJoiner()
	defer j.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sess, err := j.Join(ctx, JoinReq{URL: srv.URL + "/join", FIO: "Рекордер", Role: RoleRecord})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ac, ok := sess.(AudioChecker)
	if !ok || !ac.EnsureAudio(ctx) {
		t.Fatalf("expected EnsureAudio true")
	}

	sr, ok := sess.(StreamRecorder)
	if !ok {
		t.Fatal("directSession must implement StreamRecorder")
	}
	segPath := filepath.Join(t.TempDir(), "audio-1.ogg")
	rec, err := sr.StartRecording(ctx, segPath)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := rec.Stop(); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(segPath)
	if err != nil || st.Size() < 100 {
		t.Fatalf("expected non-empty recorded ogg, stat=%v err=%v", st, err)
	}
	if _, err := fmt.Fprintf(io.Discard, "recorded=%d", st.Size()); err != nil {
		t.Fatal(err)
	}
}

func TestDirectSessionMultiPresentationIncrementalGrab(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/slide1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	mux.HandleFunc("/slide2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	mux.HandleFunc("/slide3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})
	mux.HandleFunc("/slide4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := &directSession{
		client:      srv.Client(),
		baseURL:     srv.URL,
		inRoom:      true,
		pres:        make(map[string]presMeta),
		slides:      make(map[string]slideMeta),
		allPres:     make(map[string]presMeta),
		allSlides:   make(map[string]slideMeta),
		savedSlides: make(map[string]string),
		slidesSub:   make(chan struct{}),
	}
	close(s.slidesSub)

	// Add presentation 1
	var dummyVoice string
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p1",
		"fields":     map[string]any{"id": "pres-1", "name": "deck1.pdf", "current": true},
	}, "user1", &dummyVoice)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s1",
		"fields":     map[string]any{"id": "s1", "presentationId": "pres-1", "num": 1.0, "imageUri": "/slide1"},
	}, "user1", &dummyVoice)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s2",
		"fields":     map[string]any{"id": "s2", "presentationId": "pres-1", "num": 2.0, "imageUri": "/slide2"},
	}, "user1", &dummyVoice)

	dir := t.TempDir()

	// First grab
	n1, err := s.GrabSlides(ctx, dir)
	if err != nil {
		t.Fatalf("first GrabSlides: %v", err)
	}
	if n1 != 2 {
		t.Fatalf("first GrabSlides count = %d, want 2", n1)
	}
	f1Stat, err := os.Stat(filepath.Join(dir, "01.png"))
	if err != nil {
		t.Fatalf("01.png missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "02.png")); err != nil {
		t.Fatalf("02.png missing: %v", err)
	}

	// Add presentation 2
	s.handleDDPCollection("added", map[string]any{
		"collection": "presentations",
		"id":         "p2",
		"fields":     map[string]any{"id": "pres-2", "name": "deck2.pdf", "current": true},
	}, "user1", &dummyVoice)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s3",
		"fields":     map[string]any{"id": "s3", "presentationId": "pres-2", "num": 1.0, "imageUri": "/slide3"},
	}, "user1", &dummyVoice)
	s.handleDDPCollection("added", map[string]any{
		"collection": "slides",
		"id":         "s4",
		"fields":     map[string]any{"id": "s4", "presentationId": "pres-2", "num": 2.0, "imageUri": "/slide4"},
	}, "user1", &dummyVoice)

	// Second grab: incremental
	n2, err := s.GrabSlides(ctx, dir)
	if err != nil {
		t.Fatalf("second GrabSlides: %v", err)
	}
	if n2 != 4 {
		t.Fatalf("second GrabSlides count = %d, want 4", n2)
	}
	if _, err := os.Stat(filepath.Join(dir, "03.png")); err != nil {
		t.Fatalf("03.png missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "04.png")); err != nil {
		t.Fatalf("04.png missing: %v", err)
	}

	// 01.png must not have been overwritten (check mod time)
	f1After, _ := os.Stat(filepath.Join(dir, "01.png"))
	if f1After.ModTime() != f1Stat.ModTime() {
		t.Fatalf("01.png was overwritten on second grab")
	}

	// Third grab: no new slides
	n3, err := s.GrabSlides(ctx, dir)
	if err != nil {
		t.Fatalf("third GrabSlides: %v", err)
	}
	if n3 != 4 {
		t.Fatalf("third GrabSlides count = %d, want 4", n3)
	}
}
