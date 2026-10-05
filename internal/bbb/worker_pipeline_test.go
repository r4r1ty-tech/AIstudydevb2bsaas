package bbb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// pack stores a lecture pack for l with the given status and returns it.
func (h *workerHarness) pack(l model.Lesson, status string) *model.LecturePack {
	h.t.Helper()
	p, err := h.st.EnsurePack(l, "https://bbb.ssau.ru/b/aaa-bbb-ccc", h.w.recRoot())
	if err != nil {
		h.t.Fatal(err)
	}
	p.Status = status
	if err := h.st.SavePack(p); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *workerHarness) write(rel, body string) string {
	h.t.Helper()
	p := filepath.Join(h.w.recRoot(), rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func TestHarvestSlides(t *testing.T) {
	h := newWorkerHarness(t)
	h.user(studentTG, "Иванов Иван")
	l := h.lesson("Сети", "Лекция", 3*time.Hour)
	p := h.pack(l, model.PackRecorded)
	h.s.slides = 4

	h.w.Cfg.BBBDryRun = false
	h.w.harvestDay(context.Background(), p.Date)
	got, _ := h.st.PackByID(p.ID)
	if got.Status != model.PackSlides {
		t.Fatalf("status = %s", got.Status)
	}
	if reqs := h.j.requests(); len(reqs) != 1 || reqs[0].Role != RoleSlides || reqs[0].FIO != "Иванов Иван" {
		t.Fatalf("slides join = %+v", reqs)
	}
	if _, closed := h.s.counts(); closed != 1 {
		t.Fatal("slides session not closed")
	}
	h.wantSent(adminTG, "слайды сняты")
	if len(h.events(model.EventSlides)) != 1 {
		t.Fatal("slides event missing")
	}

	// Join failure keeps the pack as is.
	p2 := h.pack(h.lesson("Матан", "Лекция", 4*time.Hour), model.PackRecorded)
	h.j.err = errBoom
	h.w.harvestSlides(context.Background(), p2)
	if got, _ := h.st.PackByID(p2.ID); got.Status != model.PackRecorded {
		t.Fatalf("failed join changed status to %s", got.Status)
	}
	h.w.harvestSlides(context.Background(), nil)
	h.w.harvestSlides(context.Background(), &model.LecturePack{})
}

func TestHarvestSlidesDryRunSkips(t *testing.T) {
	h := newWorkerHarness(t)
	p := h.pack(h.lesson("Сети", "Лекция", 3*time.Hour), model.PackRecorded)
	h.w.harvestSlides(context.Background(), p)
	if len(h.j.requests()) != 0 {
		t.Fatal("dry run must not join")
	}
}

func TestBuildNotesFailureRetriesThenGivesUp(t *testing.T) {
	h := newWorkerHarness(t)
	l := h.lesson("Сети", "Лекция", 3*time.Hour)
	p := h.pack(l, model.PackRecorded)
	h.write(p.Audio, strings.Repeat("a", minRecordedBytes+1))

	h.w.buildNotes(context.Background(), p.ID) // no STT/LLM keys, no slides → error
	got, _ := h.st.PackByID(p.ID)
	if got.Status != model.PackError || got.Err == "" {
		t.Fatalf("pack = %+v", got)
	}
	h.wantSent(adminTG, "(попытка 1/10")
	if h.w.noteRetryReady(got, time.Now()) {
		t.Fatal("retry must wait")
	}
	if !h.w.noteRetryReady(got, time.Now().Add(noteRetryEvery+time.Minute)) {
		t.Fatal("retry should be ready after the pause")
	}
	for i := 1; i < noteQuickRetries; i++ {
		h.w.buildNotes(context.Background(), p.ID)
	}
	// После быстрых попыток пауза длинная: сбой провайдера на полдня переживаем.
	got, _ = h.st.PackByID(p.ID)
	if h.w.noteRetryReady(got, time.Now().Add(noteRetryEvery+time.Minute)) {
		t.Fatal("после быстрых попыток пауза должна быть длинной")
	}
	if !h.w.noteRetryReady(got, time.Now().Add(noteRetrySlow+time.Minute)) {
		t.Fatal("медленный повтор должен наступить")
	}
	before := len(h.sentTo(adminTG))
	for h.w.noteAttemptsFor(p.ID) < maxNoteAttempts {
		h.w.buildNotes(context.Background(), p.ID)
	}
	h.wantSent(adminTG, "попыток больше не будет: 10")
	if n := len(h.sentTo(adminTG)) - before; n != 1 {
		t.Fatalf("медленные повторы не должны спамить админу: %d сообщений", n)
	}
	got, _ = h.st.PackByID(p.ID)
	if h.w.noteRetryReady(got, time.Now().Add(24*time.Hour)) {
		t.Fatal("no retries after the limit")
	}
	if h.w.noteRetryReady(nil, time.Now()) {
		t.Fatal("nil pack")
	}
	h.w.noteOK(p.ID)
	if h.w.noteAttemptsFor(p.ID) != 0 {
		t.Fatal("noteOK must reset attempts")
	}
	h.w.buildNotes(context.Background(), 424242) // missing pack: logged
}

// Записи нет вовсе — это не сбой сборки: пак закрывается как empty без ретраев.
func TestBuildNotesMissingAudioIsEmpty(t *testing.T) {
	h := newWorkerHarness(t)
	p := h.pack(h.lesson("Сети", "Лекция", 3*time.Hour), model.PackRecorded)

	if !h.w.buildNotes(context.Background(), p.ID) {
		t.Fatal("пустая запись не должна считаться сбоем сборки")
	}
	got, _ := h.st.PackByID(p.ID)
	if got.Status != model.PackEmpty || got.Err == "" {
		t.Fatalf("pack = %+v", got)
	}
	if h.w.noteAttemptsFor(p.ID) != 0 {
		t.Fatal("empty не тратит попытки")
	}
	h.wantSent(adminTG, "конспекта не будет")
}

func TestBuildNotesDayRetriesErrorsAndPromotesStuck(t *testing.T) {
	h := newWorkerHarness(t)
	l := h.lesson("Сети", "Лекция", 3*time.Hour)
	p := h.pack(l, model.PackRecording) // bbb restarted mid-lecture
	h.write(p.Audio, strings.Repeat("a", minRecordedBytes+1))

	if done := h.w.buildNotesDay(context.Background(), p.Date); done {
		t.Fatal("failed notes must keep the day open for retries")
	}
	got, _ := h.st.PackByID(p.ID)
	if got.Status != model.PackError {
		t.Fatalf("stuck recording should be promoted and attempted, status=%s", got.Status)
	}

	// Нет звука — пак закрывается как empty один раз, день не держит.
	small := h.pack(h.lesson("Матан", "Лекция", 4*time.Hour), model.PackRecording)
	h.write(small.Audio, "tiny")
	if r := h.w.promoteStuckRecording(context.Background(), small); r != stuckEmpty {
		t.Fatalf("tiny audio: %v, want empty", r)
	}
	if got, _ := h.st.PackByID(small.ID); got.Status != model.PackEmpty {
		t.Fatalf("status = %s", got.Status)
	}
	h.wantSent(adminTG, "записи нет")
	if r := h.w.promoteStuckRecording(context.Background(), nil); r != stuckWait {
		t.Fatal("nil pack")
	}
}

// fakeGitHub accepts contents PUTs and remembers the paths.
func fakeGitHub(t *testing.T, fail bool) *[]string {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			http.NotFound(w, r)
		case http.MethodPut:
			if fail {
				http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
				return
			}
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"content": map[string]any{"sha": "abc"}})
		}
	}))
	t.Cleanup(srv.Close)
	old := githubAPI
	githubAPI = srv.URL
	t.Cleanup(func() { githubAPI = old })
	return &paths
}

func (h *workerHarness) donePack(discipline string) *model.LecturePack {
	h.t.Helper()
	p := h.pack(h.lesson(discipline, "Лекция", 5*time.Hour), model.PackDone)
	h.write(p.Audio, "audio")
	h.write(filepath.Join(p.Dir, "transcript.txt"), "текст")
	h.write(filepath.Join(p.Dir, "notes.pdf"), "%PDF")
	h.write(filepath.Join(p.Dir, "slides", "01.png"), "png")
	return p
}

func TestPublishSweepUploadsThenCleans(t *testing.T) {
	h := newWorkerHarness(t)
	paths := fakeGitHub(t, false)
	h.w.Cfg.GitHubToken, h.w.Cfg.GitHubOwner, h.w.Cfg.GitHubRepo, h.w.Cfg.GitHubBranch = "tok", "o", "r", "main"
	p := h.donePack("Сети")
	old := time.Now().Add(-time.Hour)
	p.UpdatedAt = old

	h.w.publishSweep(context.Background(), time.Now().Add(publishRetry+time.Minute))
	got, _ := h.st.PackByID(p.ID)
	if got.PublishStatus != "published" || got.PublishedAt == nil {
		t.Fatalf("pack = %+v", got)
	}
	if len(*paths) != 2 || !strings.HasSuffix((*paths)[0], "transcript.txt") || !strings.HasSuffix((*paths)[1], "notes.pdf") {
		t.Fatalf("uploaded = %v", *paths)
	}
	if _, err := os.Stat(filepath.Join(h.w.recRoot(), p.Audio)); !os.IsNotExist(err) {
		t.Fatal("audio must be removed after publish")
	}
	h.wantSent(adminTG, "конспект выгружен")

	h.w.publishSweep(context.Background(), time.Now().Add(publishedTTL+time.Hour))
	got, _ = h.st.PackByID(p.ID)
	if got.CleanedAt == nil {
		t.Fatal("pack not cleaned after TTL")
	}
	base := filepath.Join(h.w.recRoot(), p.Dir)
	for _, f := range []string{archive.TranscriptFile(base), archive.NotesPDF(base), archive.SlidesDir(base)} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s left after clean", f)
		}
	}
}

func TestPublishFailures(t *testing.T) {
	h := newWorkerHarness(t)
	fakeGitHub(t, true)
	h.w.Cfg.GitHubToken, h.w.Cfg.GitHubOwner, h.w.Cfg.GitHubRepo, h.w.Cfg.GitHubBranch = "tok", "o", "r", "main"
	p := h.donePack("Сети")
	h.w.publishPack(context.Background(), p)
	got, _ := h.st.PackByID(p.ID)
	if got.PublishStatus != "error" || got.Err == "" {
		t.Fatalf("api failure: %+v", got)
	}

	p2 := h.pack(h.lesson("Матан", "Лекция", 6*time.Hour), model.PackDone)
	h.w.publishPack(context.Background(), p2) // no transcript on disk
	if got, _ := h.st.PackByID(p2.ID); got.PublishStatus != "error" || !strings.Contains(got.Err, "transcript") {
		t.Fatalf("missing transcript: %+v", got)
	}
	h.write(filepath.Join(p2.Dir, "transcript.txt"), "t")
	h.w.publishPack(context.Background(), p2) // no pdf
	if got, _ := h.st.PackByID(p2.ID); !strings.Contains(got.Err, "pdf") {
		t.Fatalf("missing pdf: %+v", got)
	}

	h.w.Cfg.GitHubToken = ""
	if h.w.publisher() != nil {
		t.Fatal("no token → no publisher")
	}
	h.w.maybePublish(context.Background(), time.Now()) // disabled: no-op
}

func TestMaybePublishRunsInBackground(t *testing.T) {
	h := newWorkerHarness(t)
	paths := fakeGitHub(t, false)
	h.w.Cfg.GitHubToken, h.w.Cfg.GitHubOwner, h.w.Cfg.GitHubRepo, h.w.Cfg.GitHubBranch = "tok", "o", "r", "main"
	h.donePack("Сети")
	h.w.maybePublish(context.Background(), time.Now().Add(time.Hour))
	h.w.WaitIdle()
	if len(*paths) != 2 {
		t.Fatalf("uploaded %v", *paths)
	}
	h.w.busy = true
	h.w.maybePublish(context.Background(), time.Now().Add(time.Hour))
	h.w.busy = false
}

func TestAnnounceNotes(t *testing.T) {
	h := newWorkerHarness(t)
	p := h.donePack("Сети")
	h.w.announceNotes(context.Background(), p) // no users → admin
	h.wantSent(adminTG, "Конспект готов")

	h.user(studentTG, "Иванов")
	h.api.Reset()
	h.w.announceNotes(context.Background(), p)
	calls := h.api.Calls("sendMessage")
	if len(calls) != 1 || calls[0].Param("chat_id") != itoa(studentTG) || !strings.Contains(calls[0].Param("reply_markup"), "nt:") {
		t.Fatalf("announce = %+v", calls)
	}
	h.w.announceNotes(context.Background(), nil)
}

func TestPackFileAndSettings(t *testing.T) {
	h := newWorkerHarness(t)
	p := &model.LecturePack{Dir: "Сети/лекция-1"}
	if got := h.w.packFile(p, "", "notes.pdf"); got != filepath.Join(h.w.recRoot(), "Сети/лекция-1", "notes.pdf") {
		t.Fatalf("fallback = %s", got)
	}
	if got := h.w.packFile(p, "x/y.pdf", "notes.pdf"); got != filepath.Join(h.w.recRoot(), "x/y.pdf") {
		t.Fatalf("rel = %s", got)
	}
	h.w.setSetting("k", "1")
	if !h.w.settingOn("k") || h.w.settingOn("missing") {
		t.Fatal("settings roundtrip")
	}
	var nilW *Worker
	if nilW.settingOn("k") || nilW.adminID() != 0 || nilW.recRoot() == "" {
		t.Fatal("nil worker helpers")
	}
	nilW.setSetting("k", "v")
}

func TestTestJoinWatchAndFail(t *testing.T) {
	h := newWorkerHarness(t)
	tj, _ := h.st.GetTestJoin()
	tj.URL, tj.Want, tj.Status = "https://bbb.ssau.ru/b/t", model.TestWantDummy, model.TestJoining
	if err := h.st.PutTestJoin(tj); err != nil {
		t.Fatal(err)
	}

	h.w.testFailed(context.Background(), tj, "форма гостя")
	h.wantSent(adminTG, "тест BBB: не зашёл, пробую ещё — форма гостя")
	n := len(h.sentTo(adminTG))
	latest, _ := h.st.GetTestJoin()
	h.w.testFailed(context.Background(), latest, "форма гостя")
	if len(h.sentTo(adminTG)) != n {
		t.Fatal("same error must not be re-sent")
	}
	stale := latest
	stale.Want = model.TestWantListen
	h.w.testFailed(context.Background(), stale, "другое")
	if len(h.sentTo(adminTG)) != n {
		t.Fatal("stale want must not report")
	}

	// watchTest: lobby → room → dead.
	h.s.set(true, false, nil)
	h.w.sessions[testSessionKey] = h.s
	tj.Status = model.TestRoom
	h.w.watchTest(context.Background(), h.s, tj, time.Now())
	if got, _ := h.st.GetTestJoin(); got.Status != model.TestLobby {
		t.Fatalf("status = %s", got.Status)
	}
	h.s.set(false, true, nil)
	got, _ := h.st.GetTestJoin()
	h.w.watchTest(context.Background(), h.s, got, time.Now())
	h.wantSent(adminTG, "тест: пустили")
	h.s.set(false, false, errBoom)
	got, _ = h.st.GetTestJoin()
	h.w.watchTest(context.Background(), h.s, got, time.Now())
	h.wantSent(adminTG, "тест: вышел из комнаты")
	if h.session(testSessionKey) != nil {
		t.Fatal("dead test session kept")
	}
	h.w.watchTest(context.Background(), nil, got, time.Now())
}

func TestAttachTestRecorderMergesOnClose(t *testing.T) {
	h := newWorkerHarness(t)
	recs, fail := useFakeRecorder(t, nil)
	sess := h.w.attachTestRecorder(context.Background(), h.s)
	if sess == Session(h.s) {
		t.Fatal("expected a recording wrapper")
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if (*recs)[0].stops != 1 {
		t.Fatal("recorder not stopped")
	}
	if _, err := os.Stat(filepath.Join(h.w.recRoot(), "test", "audio.ogg")); err != nil {
		t.Fatalf("test audio not merged: %v", err)
	}

	*fail = errBoom
	if got := h.w.attachTestRecorder(context.Background(), h.s); got != Session(h.s) {
		t.Fatal("failed recorder must return the bare session")
	}
	h.wantSent(adminTG, "тест: звук не стартанул")
}

// A failed pack must keep notes_done unset while attempts remain, and the
// day closes once the attempts run out.
func TestNotesDayStaysOpenUntilAttemptsRunOut(t *testing.T) {
	h := newWorkerHarness(t)
	p := h.pack(h.lesson("Сети", "Лекция", 3*time.Hour), model.PackRecorded)
	h.write(p.Audio, strings.Repeat("a", minRecordedBytes+1))
	if done := h.w.buildNotesDay(context.Background(), p.Date); done {
		t.Fatal("first failure must keep the day open")
	}
	if done := h.w.buildNotesDay(context.Background(), p.Date); done {
		t.Fatal("retry pause must keep the day open")
	}
	for h.w.noteAttemptsFor(p.ID) < maxNoteAttempts {
		h.w.noteFail(p.ID)
	}
	if done := h.w.buildNotesDay(context.Background(), p.Date); !done {
		t.Fatal("day must close once attempts are spent")
	}
}

// Перед каждой попыткой статус в БД «захожу» — дубли и лимит считаются в памяти.
func TestTestJoinStopsAfterRepeatedFails(t *testing.T) {
	h := newWorkerHarness(t)
	tj, _ := h.st.GetTestJoin()
	tj.URL, tj.Want = "https://bbb.ssau.ru/b/t", model.TestWantDummy
	for i := 0; i < maxTestFails; i++ {
		tj.Status, tj.Message = model.TestJoining, "захожу"
		if err := h.st.PutTestJoin(tj); err != nil {
			t.Fatal(err)
		}
		h.w.testFailed(context.Background(), tj, "форма гостя")
	}
	if n := len(h.sentTo(adminTG)); n != 2 {
		t.Fatalf("admin messages = %d, want first fail + stop", n)
	}
	h.wantSent(adminTG, "останавливаюсь")
	if got, _ := h.st.GetTestJoin(); got.Want != model.TestWantOff || got.Status != model.TestError {
		t.Fatalf("test join = %+v", got)
	}
}

// Упавший позавчера конспект догоняется, а счётчик попыток переживает рестарт.
func TestNotesCatchUpOlderDaysAndPersistAttempts(t *testing.T) {
	h := newWorkerHarness(t)
	p := h.pack(h.lesson("Сети", "Лекция", 3*time.Hour), model.PackRecorded)
	h.write(p.Audio, strings.Repeat("a", minRecordedBytes+1))
	day, err := time.ParseInLocation("2006-01-02", p.Date, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	now := day.AddDate(0, 0, 3).Add(time.Hour) // пак — позавчерашний относительно «вчера»
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	days := h.w.openNoteDays(now)
	if len(days) != 2 || days[0] != p.Date || days[1] != yesterday {
		t.Fatalf("open days = %v, want [%s %s]", days, p.Date, yesterday)
	}
	h.w.buildNotes(context.Background(), p.ID)
	if n := h.w.noteAttemptsFor(p.ID); n != 1 {
		t.Fatalf("attempts = %d", n)
	}
	if raw, ok, _ := h.st.GetSetting(noteAttemptsKey + "1"); !ok || raw != "1" {
		t.Fatalf("попытки должны лежать в settings: %q ok=%v", raw, ok)
	}

	h.w.setSetting(notesDoneKey+p.Date, "1")
	if days := h.w.openNoteDays(now); len(days) != 1 || days[0] != yesterday {
		t.Fatalf("закрытый день не должен возвращаться: %v", days)
	}
	if days := h.w.openNoteDays(day.AddDate(0, 0, noteCatchUpDays+3)); len(days) != 1 {
		t.Fatalf("слишком старые дни не догоняем: %v", days)
	}
}
