package bbb

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// Full lecture: tick joins the recording tab, starts the recorder, a later
// leave stops it, merges the segment and marks the pack recorded.
func TestTickRecordsLectureEndToEnd(t *testing.T) {
	h := newWorkerHarness(t)
	recs, _ := useFakeRecorder(t, make([]byte, 3200))
	u := h.user(studentTG, "Иванов Иван")
	l := h.lesson("Теория информации", "Лекция", 5*time.Minute)
	h.yes(studentTG, l)
	h.link(l)

	h.w.tick(context.Background())
	h.w.WaitIdle()

	reqs := h.j.requests()
	if len(reqs) != 1 || reqs[0].Role != RoleRecord || reqs[0].FIO != "Иванов Иван" {
		t.Fatalf("join requests = %+v", reqs)
	}
	key := sessionKey(u.TelegramID, l.ID)
	if h.session(key) == nil {
		t.Fatal("session not stored")
	}
	if len(*recs) != 1 {
		t.Fatalf("recorders started = %d", len(*recs))
	}
	pack, err := h.st.PackByLesson(l.ID)
	if err != nil || pack == nil || pack.Status != model.PackRecording {
		t.Fatalf("pack = %+v %v", pack, err)
	}
	if p, _ := h.st.GetPresence(studentTG); p == nil || p.State != model.PresenceRoom {
		t.Fatalf("presence = %+v", p)
	}
	if greeted, _ := h.s.counts(); greeted != 1 {
		t.Fatalf("greeted = %d", greeted)
	}
	if len(h.events(model.EventJoin)) != 1 || len(h.events(model.EventRecord)) != 1 {
		t.Fatal("join/record events missing")
	}

	// Second tick while seated: watchLobby only, no second join.
	h.w.tick(context.Background())
	h.w.WaitIdle()
	if n := len(h.j.requests()); n != 1 {
		t.Fatalf("re-joined while seated: %d", n)
	}

	h.w.leave(context.Background(), studentTG, l.ID, key, "time")
	if (*recs)[0].stops != 1 {
		t.Fatal("recorder not stopped on leave")
	}
	pack, _ = h.st.PackByID(pack.ID)
	if pack.Status != model.PackRecorded {
		t.Fatalf("pack after leave = %s", pack.Status)
	}
	if _, err := os.Stat(filepath.Join(h.w.recRoot(), pack.Audio)); err != nil {
		t.Fatalf("merged audio missing: %v", err)
	}
	if _, closed := h.s.counts(); closed != 1 {
		t.Fatalf("session closed %d times", closed)
	}
	if p, _ := h.st.GetPresence(studentTG); p != nil {
		t.Fatalf("presence not cleared: %+v", p)
	}
	if len(h.events(model.EventLeave)) != 1 {
		t.Fatal("leave event missing")
	}
	h.w.leave(context.Background(), studentTG, l.ID, key, "again") // no session: no-op
}

func TestRecorderStartFailureNotifiesAdmin(t *testing.T) {
	h := newWorkerHarness(t)
	_, fail := useFakeRecorder(t, nil)
	*fail = errBoom
	h.user(studentTG, "Иванов Иван")
	l := h.lesson("Сети", "Лекция", time.Minute)
	h.yes(studentTG, l)
	h.link(l)
	h.w.tick(context.Background())
	h.w.WaitIdle()
	h.wantSent(adminTG, "запись «Сети» не стартовала")
	if h.session(sessionKey(studentTG, l.ID)) == nil {
		t.Fatal("attendance must go on without the recorder")
	}
}

func TestPracticeIsAttendedButNotRecorded(t *testing.T) {
	h := newWorkerHarness(t)
	recs, _ := useFakeRecorder(t, nil)
	h.user(studentTG, "Иванов Иван")
	l := h.lesson("Сети", "Практика", time.Minute)
	h.yes(studentTG, l)
	h.link(l)
	h.w.tick(context.Background())
	h.w.WaitIdle()
	if reqs := h.j.requests(); len(reqs) != 1 || reqs[0].Role != RolePresence {
		t.Fatalf("reqs = %+v", reqs)
	}
	if len(*recs) != 0 {
		t.Fatal("practice must not be recorded")
	}
}

func TestMissingBBBNotifiesOnce(t *testing.T) {
	h := newWorkerHarness(t)
	h.user(studentTG, "Иванов Иван")
	l := h.lesson("Сети", "Лекция", time.Minute)
	h.yes(studentTG, l)

	h.w.tick(context.Background())
	h.w.tick(context.Background())
	h.w.WaitIdle()
	if n := len(h.sentTo(studentTG)); n != 1 {
		t.Fatalf("user notified %d times, want once", n)
	}
	h.wantSent(studentTG, "Нет ссылки на комнату «Сети»")
	h.wantSent(adminTG, "нет ссылки BBB")
	if p, _ := h.st.GetPresence(studentTG); p == nil || p.State != model.PresenceError {
		t.Fatalf("presence = %+v", p)
	}
	if len(h.events(model.EventNoBBB)) != 1 {
		t.Fatal("no_bbb event missing")
	}
	if len(h.j.requests()) != 0 {
		t.Fatal("must not join without a link")
	}
}

func TestLobbyWaitAlertThenPromotion(t *testing.T) {
	h := newWorkerHarness(t)
	h.s.set(true, false, nil)
	u := h.user(studentTG, "Иванов Иван")
	l := h.lesson("Сети", "Практика", time.Minute)
	url := h.link(l)
	key := sessionKey(u.TelegramID, l.ID)
	now := time.Now()

	h.w.ensureIn(context.Background(), u, l, url, key, now, false)
	if p, _ := h.st.GetPresence(studentTG); p == nil || p.State != model.PresenceLobby {
		t.Fatalf("presence = %+v", p)
	}
	if g, _ := h.s.counts(); g != 0 {
		t.Fatal("must not greet from the lobby")
	}
	h.w.watchLobby(context.Background(), u, l, key, now.Add(time.Minute))
	if len(h.sentTo(studentTG)) != 0 {
		t.Fatal("no alert before 2 minutes")
	}
	h.w.watchLobby(context.Background(), u, l, key, now.Add(3*time.Minute))
	h.wantSent(adminTG, "не пустили из лобби")
	h.wantSent(studentTG, "всё ещё лобби")
	if len(h.events(model.EventLobby)) != 1 {
		t.Fatal("lobby event missing")
	}

	h.s.set(false, true, nil)
	h.w.watchLobby(context.Background(), u, l, key, now.Add(4*time.Minute))
	if p, _ := h.st.GetPresence(studentTG); p.State != model.PresenceRoom {
		t.Fatalf("not promoted: %+v", p)
	}
	if g, _ := h.s.counts(); g != 1 {
		t.Fatal("greet after promotion")
	}
}

func TestDropDeadNeedsRepeatedProbeErrors(t *testing.T) {
	h := newWorkerHarness(t)
	u := h.user(studentTG, "Иванов Иван")
	l := h.lesson("Сети", "Практика", time.Minute)
	url := h.link(l)
	key := sessionKey(u.TelegramID, l.ID)
	now := time.Now()

	h.w.ensureIn(context.Background(), u, l, url, key, now, false)
	h.s.set(false, false, errBoom)
	for i := 1; i < maxProbeErrs; i++ {
		h.w.watchLobby(context.Background(), u, l, key, now)
	}
	if h.session(key) == nil {
		t.Fatal("single slow probes must not drop the session")
	}
	h.w.watchLobby(context.Background(), u, l, key, now)
	if h.session(key) != nil {
		t.Fatal("dead session kept")
	}
	if h.w.isBlocked(key, nil) {
		t.Fatal("first drop must retry, not block")
	}
	if ev := h.events(model.EventError); len(ev) != 1 || !strings.HasPrefix(ev[0].Message, "retry:") {
		t.Fatalf("retry event = %+v", ev)
	}

	h.s.set(false, true, nil)
	h.w.ensureIn(context.Background(), u, l, url, key, now, false)
	h.s.set(false, false, nil) // page is neither room nor lobby
	for i := 0; i < maxProbeErrs; i++ {
		h.w.watchLobby(context.Background(), u, l, key, now)
	}
	if !h.w.isBlocked(key, nil) {
		t.Fatal("second drop must block")
	}
	h.wantSent(studentTG, "Не зашёл на «Сети»")
	h.w.dropDead(context.Background(), u, l, key, "x") // nothing live: no-op
}

// Выход по времени не должен давать перезахода с новым случайным leaveAt
// и не снимает стоп после неудач.
func TestTimeLeaveDoesNotRejoin(t *testing.T) {
	h := newWorkerHarness(t)
	h.user(studentTG, "Иванов Иван")
	l := h.lesson("Сети", "Практика", 80*time.Minute) // идёт 80 мин из 90
	h.yes(studentTG, l)
	h.link(l)
	key := sessionKey(studentTG, l.ID)
	h.w.tick(context.Background())
	h.w.WaitIdle()
	if h.session(key) == nil {
		t.Fatal("must join")
	}
	h.w.mu.Lock()
	h.w.leaveAt[key] = time.Now().Add(-time.Second)
	h.w.mu.Unlock()
	for i := 0; i < 20; i++ {
		h.w.tick(context.Background())
		h.w.WaitIdle()
	}
	if n := len(h.j.requests()); n != 1 {
		t.Fatalf("re-joined after time leave: %d joins", n)
	}
	if !h.w.isDone(key) {
		t.Fatal("key must stay done until the lesson leaves the window")
	}
}

func TestLobbyAlertOnceAndCountsFromLessonStart(t *testing.T) {
	h := newWorkerHarness(t)
	h.s.set(true, false, nil)
	u := h.user(studentTG, "Иванов Иван")
	l := h.lesson("Сети", "Практика", -10*time.Minute) // зашли за 10 мин до начала
	key := sessionKey(u.TelegramID, l.ID)
	now := time.Now()
	h.w.ensureIn(context.Background(), u, l, h.link(l), key, now, false)
	h.w.watchLobby(context.Background(), u, l, key, now.Add(5*time.Minute))
	if len(h.sentTo(adminTG)) != 0 {
		t.Fatal("lobby before the lesson start is normal")
	}
	for m := 13; m <= 30; m += 2 {
		h.w.watchLobby(context.Background(), u, l, key, now.Add(time.Duration(m)*time.Minute))
	}
	if n := len(h.sentTo(studentTG)); n != 1 {
		t.Fatalf("user lobby alerts = %d, want 1", n)
	}
	if n := len(h.sentTo(adminTG)); n != 1 {
		t.Fatalf("admin lobby alerts = %d, want 1 per 20 min", n)
	}
}

func TestEnsureInUnknownPageFails(t *testing.T) {
	h := newWorkerHarness(t)
	h.s.set(false, false, nil)
	u := h.user(studentTG, "Иванов Иван")
	l := h.lesson("Сети", "Практика", time.Minute)
	key := sessionKey(u.TelegramID, l.ID)
	h.w.ensureIn(context.Background(), u, l, h.link(l), key, time.Now(), false)
	if !h.w.isBlocked(key, nil) {
		t.Fatal("unknown page must count as a failed join")
	}
	if _, closed := h.s.counts(); closed != 1 {
		t.Fatal("failed session must be closed")
	}

	h2 := newWorkerHarness(t)
	h2.s.set(false, false, errBoom)
	u2 := h2.user(studentTG, "X")
	l2 := h2.lesson("Сети", "Практика", time.Minute)
	k2 := sessionKey(u2.TelegramID, l2.ID)
	h2.w.ensureIn(context.Background(), u2, l2, h2.link(l2), k2, time.Now(), false)
	if !h2.w.isBlocked(k2, nil) {
		t.Fatal("seat probe error must count as a failed join")
	}
}

func TestSlotOverLeavesAndRunStops(t *testing.T) {
	h := newWorkerHarness(t)
	h.w.sessions["5:77"] = h.s
	h.w.tick(context.Background()) // nothing wanted: stale session leaves
	if h.session("5:77") != nil {
		t.Fatal("stale session kept")
	}
	if _, closed := h.s.counts(); closed != 1 {
		t.Fatal("stale session not closed")
	}

	h.w.sessions["6:78"] = h.s
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run ignored cancel")
	}
	if len(h.w.sessions) != 0 {
		t.Fatal("closeAll must drop sessions")
	}
}

func TestSplitKey(t *testing.T) {
	t.Parallel()
	if a, b := splitKey("12:34"); a != 12 || b != 34 {
		t.Fatalf("splitKey = %d %d", a, b)
	}
	if a, b := splitKey("junk"); a != 0 || b != 0 {
		t.Fatalf("junk = %d %d", a, b)
	}
}

func TestLoadProxies(t *testing.T) {
	dir := t.TempDir()
	if p := loadProxies(filepath.Join(dir, "missing.txt")); p != nil {
		t.Fatal("missing file → direct")
	}
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, []byte("# none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := loadProxies(empty); p != nil {
		t.Fatal("empty list → direct")
	}
	ok := filepath.Join(dir, "ok.txt")
	if err := os.WriteFile(ok, []byte("user:pass@127.0.0.1:1080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := loadProxies(ok)
	if p == nil || p.Len() != 1 {
		t.Fatalf("pool = %v", p)
	}
	_ = p.Close()
}

func pcmLevel(sample int16, n int) []byte {
	b := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(sample))
	}
	return b
}

func TestOnLevelAlertsOnSilenceAndRecovery(t *testing.T) {
	h := newWorkerHarness(t)
	l := model.Lesson{Discipline: "Сети"}
	m := &capture.Meter{WindowBytes: 4, AlertAfter: 2}
	for _, pcm := range [][]byte{pcmLevel(0, 2), pcmLevel(0, 2)} {
		h.w.onLevel(context.Background(), l, m, m.Feed(pcm))
	}
	h.wantSent(adminTG, "запись «Сети»: 2 мин тишины")
	h.w.onLevel(context.Background(), l, m, m.Feed(pcmLevel(9000, 2)))
	h.wantSent(adminTG, "звук появился")
	before := len(h.sentTo(adminTG))
	h.w.onLevel(context.Background(), l, m, m.Feed(pcmLevel(9000, 2)))
	h.w.onLevel(context.Background(), l, m, capture.LevelNone)
	h.w.onLevel(context.Background(), l, nil, capture.LevelSilent)
	if len(h.sentTo(adminTG)) != before {
		t.Fatal("ticks must not notify")
	}
}

func TestOnWakeNotifiesListeners(t *testing.T) {
	h := newWorkerHarness(t)
	users := []model.User{
		{TelegramID: studentTG, FIO: "Иванов Иван", Enabled: true, Onboarded: true},
		{TelegramID: 3, FIO: "Петров Пётр", Enabled: true, Onboarded: true},
	}
	h.w.onWake(context.Background(), model.Lesson{ID: 9, Discipline: "Сети"}, users, "иванов")
	if len(h.sentTo(studentTG)) != 1 || len(h.sentTo(3)) != 0 {
		t.Fatalf("surname ping went to %q / %q", h.sentTo(studentTG), h.sentTo(3))
	}
	h.w.onWake(context.Background(), model.Lesson{ID: 9, Discipline: "Сети"}, users, "контрольная")
	if len(h.sentTo(3)) != 1 {
		t.Fatal("common word must reach everyone")
	}
	h.w.onWake(context.Background(), model.Lesson{Discipline: "тест"}, users, "мудл")
	h.wantSent(adminTG, "тест услышал: «мудл»")
	if n := len(h.events(model.EventWake)); n != 3 {
		t.Fatalf("wake events = %d", n)
	}
	h.w.onWake(context.Background(), model.Lesson{}, users, "")
}

func TestStartSpotterReadsUntilStop(t *testing.T) {
	h := newWorkerHarness(t)
	rec := &fakeRec{pcm: pcmLevel(0, capture.WakeRate), stopped: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		h.w.startSpotter(context.Background(), rec, model.Lesson{Discipline: "Сети"}, nil)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	_ = rec.Stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("spotter did not stop on EOF")
	}
	h.w.startSpotter(context.Background(), nil, model.Lesson{}, nil)
}

func TestMaybeHarvestAndNotesSkipWhileBusy(t *testing.T) {
	h := newWorkerHarness(t)
	h.w.sessions["1:1"] = h.s
	now := time.Now()
	h.w.maybeHarvest(context.Background(), now)
	h.w.maybeNotes(context.Background(), now)
	h.w.WaitIdle()
	if len(h.j.requests()) != 0 {
		t.Fatal("no slide harvest while a room is live")
	}
}

func TestLectureUsersFiltersIntent(t *testing.T) {
	h := newWorkerHarness(t)
	h.user(studentTG, "Иванов")
	h.user(3, "Петров")
	l := h.lesson("Сети", "Лекция", time.Minute)
	at := time.Now()
	if err := h.st.PutIntent(model.JoinIntent{TelegramID: 3, LessonID: l.ID, Decision: model.JoinNo, AskedAt: at, DecidedAt: &at}); err != nil {
		t.Fatal(err)
	}
	got := h.w.lectureUsers(l, time.Now())
	if len(got) != 1 || got[0].TelegramID != studentTG {
		t.Fatalf("lectureUsers = %+v", got)
	}
	var nilW *Worker
	if nilW.lectureUsers(l, time.Now()) != nil {
		t.Fatal("nil worker")
	}
}

func TestAttachRecorderGuards(t *testing.T) {
	h := newWorkerHarness(t)
	if got, rec := h.w.attachRecorder(context.Background(), nil, model.Lesson{}, ""); got != nil || rec {
		t.Fatal("nil session passes through")
	}
	var nilW *Worker
	if got, rec := nilW.attachRecorder(context.Background(), h.s, model.Lesson{}, ""); got != h.s || rec {
		t.Fatal("nil worker passes the session through")
	}
	if archive.AudioFile("x") == "" {
		t.Fatal("archive helper")
	}
}
