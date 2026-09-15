package bbb

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
)

const testSessionKey = "test"

func (w *Worker) tickTest(ctx context.Context, now time.Time, wanted map[string]struct{}) {
	if w == nil || w.Store == nil {
		return
	}
	tj, err := w.Store.GetTestJoin()
	if err != nil {
		return
	}
	if strings.TrimSpace(tj.URL) == "" || tj.Want == model.TestWantOff {
		return
	}
	if wanted != nil {
		wanted[testSessionKey] = struct{}{}
	}
	w.ensureTest(ctx, tj, now)
}

func (w *Worker) ensureTest(ctx context.Context, tj model.TestJoin, now time.Time) {
	if !w.beginJoin(testSessionKey) {
		return
	}
	w.joinWG.Add(1)
	go func() {
		defer w.joinWG.Done()
		defer w.endJoin(testSessionKey)
		w.ensureTestN(ctx, tj, now, 0)
	}()
}

func (w *Worker) pauseTest(ctx context.Context) {
	tj, err := w.Store.GetTestJoin()
	if err != nil {
		return
	}
	active := tj.Want != model.TestWantOff &&
		(tj.Status == model.TestJoining || tj.Status == model.TestLobby || tj.Status == model.TestRoom)
	w.stopTest(ctx, "lecture", false)
	if !active {
		return
	}
	w.mu.Lock()
	already := w.testPaused
	w.testPaused = true
	w.mu.Unlock()
	if already {
		return
	}
	latest, err := w.Store.GetTestJoin()
	if err != nil {
		return
	}
	latest.Want = model.TestWantOff
	latest.Status = model.TestError
	latest.Mode = ""
	latest.Message = "идёт пара: Chrome занят лекцией"
	_ = w.Store.PutTestJoin(latest)
	logx.Warnf("bbb", "test paused: lecture in progress")
	notify.Admin(ctx, w.Cfg, "тест отложен: идёт пара, один Chrome занят лекцией. Запусти заново после пары.")
}

func (w *Worker) resumeTest() {
	w.mu.Lock()
	w.testPaused = false
	w.mu.Unlock()
}

func (w *Worker) ensureTestN(ctx context.Context, tj model.TestJoin, now time.Time, depth int) {
	if depth > 2 {
		return
	}
	w.mu.Lock()
	sess, live := w.sessions[testSessionKey]
	w.mu.Unlock()

	if live {
		if tj.Want != "" && tj.Mode != "" && tj.Mode != tj.Want {
			w.stopTest(ctx, "switch", false)
			live = false
		} else {
			w.watchTest(ctx, sess, tj, now)
			return
		}
	}

	url := strings.TrimSpace(tj.URL)
	if url == "" {
		return
	}
	role := RolePresence
	if tj.Want == model.TestWantListen {
		role = RoleRecord
	}
	tj.Status = model.TestJoining
	tj.Message = "захожу"
	_ = w.Store.PutTestJoin(tj)
	logx.Infof("bbb", "test join want=%s name=%q url=%s", tj.Want, tj.GuestName(), url)

	sess, err := w.Joiner.Join(ctx, JoinReq{URL: url, FIO: tj.GuestName(), Role: role})
	if err != nil {
		tj.Status = model.TestError
		tj.Mode = ""
		tj.Message = err.Error()
		_ = w.Store.PutTestJoin(tj)
		logx.Warnf("bbb", "test join fail: %v", err)
		notify.Admin(ctx, w.Cfg, "тест BBB: не зашёл — "+err.Error())
		return
	}
	if latest, e := w.Store.GetTestJoin(); e == nil {
		if latest.Want == model.TestWantOff {
			_ = sess.Close()
			latest.Status = model.TestIdle
			latest.Mode = ""
			latest.Message = "вышел"
			_ = w.Store.PutTestJoin(latest)
			return
		}
		if latest.Want != tj.Want {
			_ = sess.Close()
			w.ensureTestN(ctx, latest, now, depth+1)
			return
		}
	}
	if tj.Want == model.TestWantListen {
		sess = w.attachTestRecorder(ctx, sess)
	}

	lobby, err := sess.InLobby(ctx)
	if err != nil {
		_ = sess.Close()
		tj.Status = model.TestError
		tj.Mode = ""
		tj.Message = err.Error()
		_ = w.Store.PutTestJoin(tj)
		notify.Admin(ctx, w.Cfg, "тест BBB: не зашёл — "+err.Error())
		return
	}
	room, err := sess.InRoom(ctx)
	if err != nil {
		_ = sess.Close()
		tj.Status = model.TestError
		tj.Mode = ""
		tj.Message = err.Error()
		_ = w.Store.PutTestJoin(tj)
		notify.Admin(ctx, w.Cfg, "тест BBB: не зашёл — "+err.Error())
		return
	}
	if !lobby && !room {
		_ = sess.Close()
		tj.Status = model.TestError
		tj.Mode = ""
		tj.Message = "страница не комната и не лобби"
		_ = w.Store.PutTestJoin(tj)
		notify.Admin(ctx, w.Cfg, "тест BBB: не зашёл — страница не комната и не лобби")
		return
	}

	state := model.TestRoom
	msg := "в комнате"
	if lobby {
		state = model.TestLobby
		msg = "лобби, жду модератора"
	}
	w.hogs().Hold()
	w.mu.Lock()
	w.sessions[testSessionKey] = sess
	if state == model.TestLobby {
		w.lobbyAt[testSessionKey] = now
	}
	w.mu.Unlock()

	tj.Status = state
	tj.Mode = tj.Want
	tj.Message = msg
	_ = w.Store.PutTestJoin(tj)
	logx.Infof("bbb", "test seated state=%s mode=%s name=%q", state, tj.Mode, tj.GuestName())
	_ = w.Store.AddEvent(model.Event{
		At: now, Type: model.EventJoin, TelegramID: w.adminID(), Message: "тест " + tj.Want,
	})
	if state == model.TestLobby {
		notify.Admin(ctx, w.Cfg, "тест: лобби как «"+tj.GuestName()+"». Пусти из модерации.")
		return
	}
	notify.Admin(ctx, w.Cfg, testInRoomText(tj))
}

func testInRoomText(tj model.TestJoin) string {
	if tj.Want == model.TestWantListen {
		return "тест: в комнате как «" + tj.GuestName() + "», слушаю. Скажи вейкворд."
	}
	return "тест: в комнате как «" + tj.GuestName() + "», болванчик без звука."
}

func (w *Worker) watchTest(ctx context.Context, sess Session, tj model.TestJoin, now time.Time) {
	if sess == nil {
		return
	}
	lobby, err := sess.InLobby(ctx)
	if err != nil {
		w.stopTest(ctx, "dead", true)
		return
	}
	room, err := sess.InRoom(ctx)
	if err != nil || (!lobby && !room) {
		w.stopTest(ctx, "dead", true)
		return
	}
	if lobby {
		if tj.Status != model.TestLobby {
			tj.Status = model.TestLobby
			tj.Message = "лобби, жду модератора"
			_ = w.Store.PutTestJoin(tj)
		}
		return
	}
	if tj.Status == model.TestLobby {
		notify.Admin(ctx, w.Cfg, "тест: пустили как «"+tj.GuestName()+"».")
	}
	if tj.Status != model.TestRoom {
		tj.Status = model.TestRoom
		tj.Message = "в комнате"
		_ = w.Store.PutTestJoin(tj)
	}
}

func (w *Worker) stopTest(ctx context.Context, reason string, ping bool) {
	w.mu.Lock()
	sess, ok := w.sessions[testSessionKey]
	delete(w.sessions, testSessionKey)
	delete(w.lobbyAt, testSessionKey)
	w.mu.Unlock()
	if ok && sess != nil {
		_ = sess.Close()
		w.hogs().Release()
	}
	tj, err := w.Store.GetTestJoin()
	if err == nil {
		if reason == "off" {
			tj.Want = model.TestWantOff
		}
		tj.Status = model.TestIdle
		tj.Mode = ""
		tj.Message = "вышел"
		_ = w.Store.PutTestJoin(tj)
	}
	if ok {
		_ = w.Store.AddEvent(model.Event{
			At: time.Now(), Type: model.EventLeave, TelegramID: w.adminID(), Message: "тест " + reason,
		})
		if ping {
			notify.Admin(ctx, w.Cfg, "тест: вышел из комнаты.")
		}
	}
}

func (w *Worker) attachTestRecorder(ctx context.Context, sess Session) Session {
	if w == nil || sess == nil {
		return sess
	}
	dir := filepath.Join(w.recRoot(), "test")
	seg := capture.SegmentPath(dir, time.Now().UnixNano())
	rec, err := capture.Start(ctx, seg)
	if err != nil {
		logx.Warnf("bbb", "test ffmpeg: %v", err)
		notify.Admin(ctx, w.Cfg, "тест: звук не стартанул — "+err.Error())
		return sess
	}
	users, err := w.Store.ListUsers()
	if err != nil {
		users = nil
	}
	lesson := model.Lesson{Discipline: "тест"}
	go w.startSpotter(ctx, rec, lesson, users)
	return &closeHook{Session: sess, fn: func() {
		_ = rec.Stop()
		if _, err := capture.MergeSegments(context.Background(), dir, filepath.Join(dir, "audio.ogg")); err != nil {
			logx.Warnf("bbb", "test merge: %v", err)
		}
	}}
}
