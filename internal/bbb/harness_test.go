package bbb

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/tgtest"
)

const (
	adminTG   int64 = 1
	studentTG int64 = 2
)

// fakeSess is a mutable Session: tests flip lobby/room/err between ticks.
type fakeSess struct {
	mu       sync.Mutex
	lobby    bool
	room     bool
	err      error
	greeted  int
	closed   int
	slides   int
	slideErr error
}

func (s *fakeSess) set(lobby, room bool, err error) {
	s.mu.Lock()
	s.lobby, s.room, s.err = lobby, room, err
	s.mu.Unlock()
}

func (s *fakeSess) InLobby(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lobby, s.err
}

func (s *fakeSess) InRoom(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.room, s.err
}

func (s *fakeSess) Greet(context.Context) error {
	s.mu.Lock()
	s.greeted++
	s.mu.Unlock()
	return nil
}

func (s *fakeSess) Close() error {
	s.mu.Lock()
	s.closed++
	s.mu.Unlock()
	return nil
}

func (s *fakeSess) GrabSlides(_ context.Context, dir string) (int, error) {
	s.mu.Lock()
	n, err := s.slides, s.slideErr
	s.mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	return n, err
}

func (s *fakeSess) counts() (greeted, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.greeted, s.closed
}

// fakeJoiner hands out one session and records the requests.
type fakeJoiner struct {
	mu   sync.Mutex
	sess Session
	err  error
	reqs []JoinReq
}

func (j *fakeJoiner) Join(_ context.Context, req JoinReq) (Session, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.reqs = append(j.reqs, req)
	if j.err != nil {
		return nil, j.err
	}
	return j.sess, nil
}

func (j *fakeJoiner) requests() []JoinReq {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]JoinReq(nil), j.reqs...)
}

// fakeRec stands in for ffmpeg: it writes a segment file, serves pcm once,
// then blocks until Stop, like a live capture.
type fakeRec struct {
	pcm     []byte
	stopped chan struct{}
	once    sync.Once
	mu      sync.Mutex
	stops   int
}

func (r *fakeRec) Read(p []byte) (int, error) {
	r.mu.Lock()
	if len(r.pcm) > 0 {
		n := copy(p, r.pcm)
		r.pcm = r.pcm[n:]
		r.mu.Unlock()
		return n, nil
	}
	r.mu.Unlock()
	<-r.stopped
	return 0, io.EOF
}

func (r *fakeRec) Stop() error {
	r.mu.Lock()
	r.stops++
	r.mu.Unlock()
	r.once.Do(func() { close(r.stopped) })
	return nil
}

// useFakeRecorder swaps startRecorder; the returned slice collects every
// recorder started, and fail makes the next start return an error.
func useFakeRecorder(t *testing.T, pcm []byte) (started *[]*fakeRec, fail *error) {
	t.Helper()
	var mu sync.Mutex
	var list []*fakeRec
	var failErr error
	old := startRecorder
	startRecorder = func(_ context.Context, path string) (recorder, error) {
		mu.Lock()
		defer mu.Unlock()
		if failErr != nil {
			return nil, failErr
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("O", 30000)), 0o600); err != nil {
			return nil, err
		}
		r := &fakeRec{pcm: append([]byte(nil), pcm...), stopped: make(chan struct{})}
		list = append(list, r)
		return r, nil
	}
	t.Cleanup(func() { startRecorder = old })
	return &list, &failErr
}

type workerHarness struct {
	t   *testing.T
	w   *Worker
	st  *store.Store
	api *tgtest.Server
	j   *fakeJoiner
	s   *fakeSess
}

func newWorkerHarness(t *testing.T) *workerHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	api := tgtest.New(t)
	t.Cleanup(notify.SetTestAPI(api.Opts()))
	cfg := &config.Config{
		BotToken:      tgtest.Token,
		AdminID:       adminTG,
		RecordingsDir: t.TempDir(),
		BBBDryRun:     true,
	}
	w := NewWorker(cfg, st, time.UTC)
	s := &fakeSess{room: true}
	j := &fakeJoiner{sess: s}
	w.Joiner = j
	return &workerHarness{t: t, w: w, st: st, api: api, j: j, s: s}
}

func (h *workerHarness) user(id int64, fio string) model.User {
	h.t.Helper()
	u := model.User{TelegramID: id, FIO: fio, Subgroup: 1, Enabled: true, Onboarded: true, OnboardStage: model.StageDone}
	if err := h.st.UpsertUser(&u); err != nil {
		h.t.Fatal(err)
	}
	return u
}

// lesson stores a lesson that began `ago` before now and lasts 90 minutes.
func (h *workerHarness) lesson(discipline, typ string, ago time.Duration) model.Lesson {
	h.t.Helper()
	begin := time.Now().UTC().Add(-ago).Truncate(time.Second)
	l := model.Lesson{
		Date: begin.Format("2006-01-02"), Start: begin.Format("15:04"), End: begin.Add(90 * time.Minute).Format("15:04"),
		Begin: begin, Finish: begin.Add(90 * time.Minute),
		Discipline: discipline, Type: typ, Online: true,
	}
	all, _ := h.st.ListLessons()
	if err := h.st.ReplaceLessons(append(all, l)); err != nil {
		h.t.Fatal(err)
	}
	all, _ = h.st.ListLessons()
	for _, got := range all {
		if got.Discipline == discipline && got.Begin.Equal(begin) {
			return got
		}
	}
	h.t.Fatal("lesson not stored")
	return model.Lesson{}
}

func (h *workerHarness) yes(tg int64, l model.Lesson) {
	h.t.Helper()
	at := time.Now().Add(-20 * time.Minute)
	if err := h.st.PutIntent(model.JoinIntent{TelegramID: tg, LessonID: l.ID, Decision: model.JoinYes, AskedAt: at, DecidedAt: &at}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *workerHarness) link(l model.Lesson) string {
	h.t.Helper()
	u := "https://bbb.ssau.ru/b/aaa-bbb-ccc"
	if err := h.st.SetLessonBBB(l.ID, u); err != nil {
		h.t.Fatal(err)
	}
	return u
}

// sentTo returns texts sent to a chat.
func (h *workerHarness) sentTo(chat int64) []string {
	var out []string
	for _, c := range h.api.Calls("sendMessage") {
		if c.Param("chat_id") == itoa(chat) {
			out = append(out, c.Param("text"))
		}
	}
	return out
}

func (h *workerHarness) wantSent(chat int64, sub string) {
	h.t.Helper()
	for _, s := range h.sentTo(chat) {
		if strings.Contains(s, sub) {
			return
		}
	}
	h.t.Fatalf("no message to %d containing %q; got %q", chat, sub, h.sentTo(chat))
}

func (h *workerHarness) events(typ string) []model.Event {
	h.t.Helper()
	all, err := h.st.ListEvents(500)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []model.Event
	for _, e := range all {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (h *workerHarness) session(key string) Session {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	return h.w.sessions[key]
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

var errBoom = errors.New("boom")
