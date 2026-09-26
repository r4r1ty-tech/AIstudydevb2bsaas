package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestTestGetAndGuestName(t *testing.T) {
	s, _ := newServer(t, nil)
	rec := call(t, s, http.MethodGet, "/api/test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	var j model.TestJoin
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || j.Want != model.TestWantOff {
		t.Fatalf("fresh test join = %+v %v", j, err)
	}

	long := strings.Repeat("я", 80)
	for in, want := range map[string]string{"  Робот ": "Робот", "-": model.TestGuestName, "": model.TestGuestName, long: strings.Repeat("я", 64)} {
		body, _ := json.Marshal(map[string]any{"name": in})
		if rec := call(t, s, http.MethodPost, "/api/test", string(body)); rec.Code != http.StatusOK {
			t.Fatalf("post name %q: %d", in, rec.Code)
		}
		rec = call(t, s, http.MethodGet, "/api/test", "")
		_ = json.Unmarshal(rec.Body.Bytes(), &j)
		if j.Name != want {
			t.Errorf("name %q stored as %q, want %q", in, j.Name, want)
		}
	}
	if rec := call(t, s, http.MethodPost, "/api/test", "{bad"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", rec.Code)
	}
	if rec := call(t, s, http.MethodPost, "/api/test", ""); rec.Code != http.StatusOK {
		t.Fatalf("empty body is a no-op: %d", rec.Code)
	}
}

func TestStoreFailureIs500(t *testing.T) {
	s, st := newServer(t, nil)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/test", ""},
		{http.MethodPost, "/api/test", `{"want":"off"}`},
		{http.MethodGet, "/api/people", ""},
	} {
		rec := call(t, s, c.method, c.path, c.body)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s on a dead db = %d, want 500", c.method, c.path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	writeStoreErr(rec, errors.New("x"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("writeStoreErr = %d", rec.Code)
	}
}

func TestLessonsListsPackRecordingsWithSize(t *testing.T) {
	s, st := newServer(t, nil)
	s.recordingsDir = t.TempDir()
	l := seedLesson(t, st, true)
	p, err := st.EnsurePack(l, "", s.recordingsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.recordingsDir, p.Dir, "audio.ogg"), make([]byte, 1500), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.recordingsDir, p.Dir, "slides", "01.png"), make([]byte, 500), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodGet, "/api/lessons", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("lessons: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Recordings []model.Recording `json:"recordings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Recordings) != 1 || out.Recordings[0].Size != 2000 || out.Recordings[0].Status != model.PackRecording {
		t.Fatalf("recordings = %+v", out.Recordings)
	}
	if dirSize(filepath.Join(s.recordingsDir, "missing")) != 0 {
		t.Fatal("missing dir has no size")
	}
	if !s.anyRecording() {
		t.Fatal("a recording pack exists")
	}
	var nilS *Server
	if nilS.anyRecording() || len(nilS.listPackRecordings()) != 0 {
		t.Fatal("nil server")
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestListenServesAndShutsDown(t *testing.T) {
	s, _ := newServer(t, nil)
	s.cfg.ListenAddr = freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Listen(ctx) }()

	var res *http.Response
	var err error
	for i := 0; i < 50; i++ {
		res, err = http.Get("http://" + s.cfg.ListenAddr + "/")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server not up: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("index: %d", res.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Listen: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Listen ignored cancel")
	}
}

func TestListenPortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := New(&config.Config{ListenAddr: ln.Addr().String()}, nil, nil, "")
	if err := s.Listen(context.Background()); err == nil {
		t.Fatal("busy port must fail")
	}
}
