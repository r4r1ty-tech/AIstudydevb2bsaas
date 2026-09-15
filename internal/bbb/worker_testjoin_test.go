package bbb

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func TestTickTestDryJoin(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.PutTestJoin(model.TestJoin{
		URL:    "https://bbb.ssau.ru/b/test-room",
		Want:   model.TestWantDummy,
		Status: model.TestJoining,
	}); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(&config.Config{BBBDryRun: true, AdminID: 1074442235}, st, time.UTC)
	w.tick(context.Background())
	w.WaitIdle()
	got, err := st.GetTestJoin()
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.TestRoom || got.Mode != model.TestWantDummy {
		t.Fatalf("after join: %+v", got)
	}
	w.mu.Lock()
	_, live := w.sessions[testSessionKey]
	w.mu.Unlock()
	if !live {
		t.Fatal("expected test session")
	}
	got.Want = model.TestWantOff
	if err := st.PutTestJoin(got); err != nil {
		t.Fatal(err)
	}
	w.tick(context.Background())
	w.mu.Lock()
	_, live = w.sessions[testSessionKey]
	w.mu.Unlock()
	if live {
		t.Fatal("test session should leave")
	}
}

func TestTickPausesTestDuringLecture(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	if err := st.ReplaceLessons([]model.Lesson{{
		Date: now.Format("2006-01-02"), Start: "10:00", End: "11:35",
		Begin: now.Add(-5 * time.Minute), Finish: now.Add(90 * time.Minute),
		Discipline: "Матан", Online: true, Type: "Лекция",
	}}); err != nil {
		t.Fatal(err)
	}
	lessons, err := st.ListLessons()
	if err != nil || len(lessons) != 1 {
		t.Fatalf("lessons: %v %v", lessons, err)
	}
	lesson := lessons[0]
	if err := st.UpsertUser(&model.User{
		TelegramID: 1, FIO: "Иванов Иван", Subgroup: 1, Onboarded: true, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	asked := now.Add(-20 * time.Minute)
	if err := st.PutIntent(model.JoinIntent{
		TelegramID: 1, LessonID: lesson.ID, Decision: model.JoinYes, AskedAt: asked,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLessonBBB(lesson.ID, "https://bbb.ssau.ru/b/x"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTestJoin(model.TestJoin{
		URL:    "https://bbb.ssau.ru/b/test-room",
		Want:   model.TestWantDummy,
		Status: model.TestJoining,
	}); err != nil {
		t.Fatal(err)
	}

	w := NewWorker(&config.Config{BBBDryRun: true, AdminID: 1074442235}, st, time.UTC)
	w.Joiner = &stubJoiner{sess: stubSess{room: true}}
	w.tick(context.Background())
	w.WaitIdle()

	got, err := st.GetTestJoin()
	if err != nil {
		t.Fatal(err)
	}
	if got.Want != model.TestWantOff || got.Status != model.TestError {
		t.Fatalf("test should be paused: %+v", got)
	}
	if !strings.Contains(got.Message, "пара") {
		t.Fatalf("pause reason missing: %q", got.Message)
	}
}

func TestInRoomCopy(t *testing.T) {
	dummy := testInRoomText(model.TestJoin{Name: "тест", Want: model.TestWantDummy})
	if dummy == "" {
		t.Fatal("empty dummy")
	}
	listen := testInRoomText(model.TestJoin{Name: "тест", Want: model.TestWantListen})
	if listen == dummy {
		t.Fatal("listen should differ")
	}
}
