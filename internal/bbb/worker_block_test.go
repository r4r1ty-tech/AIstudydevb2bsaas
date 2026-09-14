package bbb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

type stubSess struct {
	lobby bool
	room  bool
	err   error
}

func (s stubSess) InLobby(context.Context) (bool, error) { return s.lobby, s.err }
func (s stubSess) InRoom(context.Context) (bool, error)  { return s.room, s.err }
func (stubSess) Greet(context.Context) error             { return nil }
func (stubSess) Close() error                            { return nil }
func (stubSess) GrabSlides(context.Context, string) (int, error) {
	return 0, nil
}

type stubJoiner struct {
	sess Session
	err  error
	n    int
}

func (j *stubJoiner) Join(context.Context, JoinReq) (Session, error) {
	j.n++
	if j.err != nil {
		return nil, j.err
	}
	return j.sess, nil
}

func TestJoinFailBlocksUntilYes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2026, 9, 14, 10, 5, 0, 0, time.UTC)
	if err := st.ReplaceLessons([]model.Lesson{{
		Date: "2026-09-14", Start: "10:00", End: "11:35",
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
	u := &model.User{
		TelegramID: 1, FIO: "Иванов Иван", Subgroup: 1,
		Onboarded: true, Enabled: true,
	}
	if err := st.UpsertUser(u); err != nil {
		t.Fatal(err)
	}
	decided := now.Add(-10 * time.Minute)
	if err := st.PutIntent(model.JoinIntent{
		TelegramID: 1, LessonID: lesson.ID, Decision: model.JoinYes,
		AskedAt: decided, DecidedAt: &decided,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLessonBBB(lesson.ID, "https://bbb.ssau.ru/b/x"); err != nil {
		t.Fatal(err)
	}

	key := sessionKey(1, lesson.ID)
	j := &stubJoiner{err: errors.New("форма гостя")}
	w := NewWorker(&config.Config{BBBDryRun: true}, st, time.UTC)
	w.Joiner = j

	w.ensureIn(context.Background(), *u, lesson, "https://bbb.ssau.ru/b/x", key, now, false)
	if j.n != 1 {
		t.Fatalf("joins %d", j.n)
	}
	intent, _ := st.GetIntent(1, lesson.ID)
	if !w.isBlocked(key, intent) {
		t.Fatal("should block after fail")
	}

	w.ensureIn(context.Background(), *u, lesson, "https://bbb.ssau.ru/b/x", key, now.Add(time.Minute), false)
	if j.n != 1 {
		t.Fatalf("blocked still joined %d", j.n)
	}

	if err := st.SetIntentDecision(1, lesson.ID, model.JoinYes); err != nil {
		t.Fatal(err)
	}
	intent, _ = st.GetIntent(1, lesson.ID)
	j.err = nil
	j.sess = stubSess{room: true}
	if w.isBlocked(key, intent) {
		t.Fatal("JoinYes after block should clear")
	}
	w.ensureIn(context.Background(), *u, lesson, "https://bbb.ssau.ru/b/x", key, now.Add(3*time.Minute), false)
	if j.n != 2 {
		t.Fatalf("rearm joins %d", j.n)
	}
}

func TestDropDeadOneSilentRetry(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	w := NewWorker(&config.Config{BBBDryRun: true}, st, time.UTC)
	key := "7:9"
	u := model.User{TelegramID: 7, FIO: "А"}
	lesson := model.Lesson{ID: 9, Discipline: "Физ"}
	w.sessions[key] = stubSess{room: true}

	w.dropDead(context.Background(), u, lesson, key, "страница не комната")
	w.mu.Lock()
	_, live := w.sessions[key]
	n := w.dropRetry[key]
	_, blocked := w.blockedAt[key]
	w.mu.Unlock()
	if live || n != 1 || blocked {
		t.Fatalf("first drop: live=%v n=%d blocked=%v", live, n, blocked)
	}

	w.sessions[key] = stubSess{}
	w.dropDead(context.Background(), u, lesson, key, "страница не комната")
	w.mu.Lock()
	_, blocked = w.blockedAt[key]
	w.mu.Unlock()
	if !blocked {
		t.Fatal("second drop must block")
	}
}
