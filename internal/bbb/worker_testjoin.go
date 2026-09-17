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
		logx.Errorf("bbb", "tickTest: get test join: %v", err)
		return
	}
	if strings.TrimSpace(tj.URL) == "" || tj.Want == model.TestWantOff {
		logx.Debugf("bbb", "tickTest: idle want=%s url_empty=%v", tj.Want, strings.TrimSpace(tj.URL) == "")
		return
	}
	logx.Debugf("bbb", "tickTest: want=%s status=%s url=%s", tj.Want, tj.Status, redactURL(tj.URL))
	if wanted != nil {
		wanted[testSessionKey] = struct{}{}
	}
	w.ensureTest(ctx, tj, now)
}

func (w *Worker) ensureTest(ctx context.Context, tj model.TestJoin, now time.Time) {
	if !w.beginJoin(testSessionKey) {
		logx.Debugf("bbb", "ensureTest: join already in flight")
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
		logx.Errorf("bbb", "pauseTest: get test join: %v", err)
		return
	}
	active := tj.Want != model.TestWantOff &&
		(tj.Status == model.TestJoining || tj.Status == model.TestLobby || tj.Status == model.TestRoom)
	logx.Debugf("bbb", "pauseTest: active=%v status=%s", active, tj.Status)
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
		logx.Errorf("bbb", "pauseTest: reload test join: %v", err)
		return
	}
	latest.Want = model.TestWantOff
	latest.Status = model.TestError
	latest.Mode = ""
	latest.Message = "идёт пара: Chrome занят лекцией"
	if err := w.Store.PutTestJoin(latest); err != nil {
		logx.Errorf("bbb", "pauseTest: put test join: %v", err)
	}
	logx.Warnf("bbb", "test paused: lecture in progress")
	notify.Admin(ctx, w.Cfg, "тест отложен: идёт пара, один Chrome занят лекцией. Запусти заново после пары.")
}

func (w *Worker) resumeTest() {
	w.mu.Lock()
	w.testPaused = false
	w.mu.Unlock()
	logx.Debugf("bbb", "resumeTest: resumed")
}

func (w *Worker) ensureTestN(ctx context.Context, tj model.TestJoin, now time.Time, depth int) {
	if depth > 2 {
		logx.Debugf("bbb", "ensureTestN: depth=%d too deep", depth)
		return
	}
	logx.Debugf("bbb", "ensureTestN: depth=%d want=%s status=%s url=%s", depth, tj.Want, tj.Status, redactURL(tj.URL))
	w.mu.Lock()
	sess, live := w.sessions[testSessionKey]
	w.mu.Unlock()

	if live {
		if tj.Want != "" && tj.Mode != "" && tj.Mode != tj.Want {
			logx.Debugf("bbb", "ensureTestN: mode switch %s -> %s", tj.Mode, tj.Want)
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
	if err := w.Store.PutTestJoin(tj); err != nil {
		logx.Errorf("bbb", "ensureTestN: put joining: %v", err)
	}
	logx.Infof("bbb", "test join want=%s name=%q url=%s", tj.Want, tj.GuestName(), url)

	sess, err := w.Joiner.Join(ctx, JoinReq{URL: url, FIO: tj.GuestName(), Role: role})
	if err != nil {
		tj.Status = model.TestError
		tj.Mode = ""
		tj.Message = err.Error()
		if perr := w.Store.PutTestJoin(tj); perr != nil {
			logx.Errorf("bbb", "ensureTestN: put error: %v", perr)
		}
		logx.Warnf("bbb", "test join fail: %v", err)
		notify.Admin(ctx, w.Cfg, "тест BBB: не зашёл — "+err.Error())
		return
	}
	if latest, e := w.Store.GetTestJoin(); e != nil {
		logx.Debugf("bbb", "ensureTestN: reload test join: %v", e)
	} else {
		if latest.Want == model.TestWantOff {
			if cerr := sess.Close(); cerr != nil {
				logx.Debugf("bbb", "ensureTestN: close after off: %v", cerr)
			}
			latest.Status = model.TestIdle
			latest.Mode = ""
			latest.Message = "вышел"
			if perr := w.Store.PutTestJoin(latest); perr != nil {
				logx.Errorf("bbb", "ensureTestN: put idle: %v", perr)
			}
			return
		}
		if latest.Want != tj.Want {
			if cerr := sess.Close(); cerr != nil {
				logx.Debugf("bbb", "ensureTestN: close after want change: %v", cerr)
			}
			logx.Debugf("bbb", "ensureTestN: want changed %s -> %s, retry", tj.Want, latest.Want)
			w.ensureTestN(ctx, latest, now, depth+1)
			return
		}
	}
	if tj.Want == model.TestWantListen {
		sess = w.attachTestRecorder(ctx, sess)
	}

	lobby, err := sess.InLobby(ctx)
	if err != nil {
		if cerr := sess.Close(); cerr != nil {
			logx.Debugf("bbb", "ensureTestN: close after lobby err: %v", cerr)
		}
		logx.Errorf("bbb", "ensureTestN: InLobby: %v", err)
		tj.Status = model.TestError
		tj.Mode = ""
		tj.Message = err.Error()
		if perr := w.Store.PutTestJoin(tj); perr != nil {
			logx.Errorf("bbb", "ensureTestN: put error: %v", perr)
		}
		notify.Admin(ctx, w.Cfg, "тест BBB: не зашёл — "+err.Error())
		return
	}
	room, err := sess.InRoom(ctx)
	if err != nil {
		if cerr := sess.Close(); cerr != nil {
			logx.Debugf("bbb", "ensureTestN: close after room err: %v", cerr)
		}
		logx.Errorf("bbb", "ensureTestN: InRoom: %v", err)
		tj.Status = model.TestError
		tj.Mode = ""
		tj.Message = err.Error()
		if perr := w.Store.PutTestJoin(tj); perr != nil {
			logx.Errorf("bbb", "ensureTestN: put error: %v", perr)
		}
		notify.Admin(ctx, w.Cfg, "тест BBB: не зашёл — "+err.Error())
		return
	}
	if !lobby && !room {
		if cerr := sess.Close(); cerr != nil {
			logx.Debugf("bbb", "ensureTestN: close after unknown seat: %v", cerr)
		}
		logx.Warnf("bbb", "ensureTestN: not room and not lobby")
		tj.Status = model.TestError
		tj.Mode = ""
		tj.Message = "страница не комната и не лобби"
		if perr := w.Store.PutTestJoin(tj); perr != nil {
			logx.Errorf("bbb", "ensureTestN: put error: %v", perr)
		}
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
	if err := w.Store.PutTestJoin(tj); err != nil {
		logx.Errorf("bbb", "ensureTestN: put seated: %v", err)
	}
	logx.Infof("bbb", "test seated state=%s mode=%s name=%q", state, tj.Mode, tj.GuestName())
	if err := w.Store.AddEvent(model.Event{
		At: now, Type: model.EventJoin, TelegramID: w.adminID(), Message: "тест " + tj.Want,
	}); err != nil {
		logx.Debugf("bbb", "ensureTestN: add event: %v", err)
	}
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
	logx.Debugf("bbb", "watchTest: status=%s want=%s", tj.Status, tj.Want)
	lobby, err := sess.InLobby(ctx)
	if err != nil {
		logx.Errorf("bbb", "watchTest: InLobby: %v", err)
		w.stopTest(ctx, "dead", true)
		return
	}
	room, err := sess.InRoom(ctx)
	if err != nil || (!lobby && !room) {
		logx.Warnf("bbb", "watchTest: InRoom lobby=%v: %v", lobby, err)
		w.stopTest(ctx, "dead", true)
		return
	}
	if lobby {
		if tj.Status != model.TestLobby {
			tj.Status = model.TestLobby
			tj.Message = "лобби, жду модератора"
			if perr := w.Store.PutTestJoin(tj); perr != nil {
				logx.Errorf("bbb", "watchTest: put lobby: %v", perr)
			}
		}
		return
	}
	if tj.Status == model.TestLobby {
		notify.Admin(ctx, w.Cfg, "тест: пустили как «"+tj.GuestName()+"».")
	}
	if tj.Status != model.TestRoom {
		tj.Status = model.TestRoom
		tj.Message = "в комнате"
		if perr := w.Store.PutTestJoin(tj); perr != nil {
			logx.Errorf("bbb", "watchTest: put room: %v", perr)
		}
	}
}

func (w *Worker) stopTest(ctx context.Context, reason string, ping bool) {
	logx.Debugf("bbb", "stopTest: reason=%s ping=%v", reason, ping)
	w.mu.Lock()
	sess, ok := w.sessions[testSessionKey]
	delete(w.sessions, testSessionKey)
	delete(w.lobbyAt, testSessionKey)
	w.mu.Unlock()
	if ok && sess != nil {
		if err := sess.Close(); err != nil {
			logx.Debugf("bbb", "stopTest: close: %v", err)
		}
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
		if perr := w.Store.PutTestJoin(tj); perr != nil {
			logx.Errorf("bbb", "stopTest: put idle: %v", perr)
		}
	} else {
		logx.Errorf("bbb", "stopTest: get test join: %v", err)
	}
	if ok {
		if err := w.Store.AddEvent(model.Event{
			At: time.Now(), Type: model.EventLeave, TelegramID: w.adminID(), Message: "тест " + reason,
		}); err != nil {
			logx.Debugf("bbb", "stopTest: add event: %v", err)
		}
		if ping {
			notify.Admin(ctx, w.Cfg, "тест: вышел из комнаты.")
		}
	}
	logx.Infof("bbb", "test stopped reason=%s was_live=%v", reason, ok)
}

func (w *Worker) attachTestRecorder(ctx context.Context, sess Session) Session {
	if w == nil || sess == nil {
		return sess
	}
	logx.Debugf("bbb", "attachTestRecorder: enter")
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
		logx.Debugf("bbb", "attachTestRecorder: list users: %v", err)
		users = nil
	}
	lesson := model.Lesson{Discipline: "тест"}
	go w.startSpotter(ctx, rec, lesson, users)
	return &closeHook{Session: sess, fn: func() {
		if err := rec.Stop(); err != nil {
			logx.Warnf("bbb", "test stop rec: %v", err)
		}
		if _, err := capture.MergeSegments(context.Background(), dir, filepath.Join(dir, "audio.ogg")); err != nil {
			logx.Warnf("bbb", "test merge: %v", err)
		}
	}}
}
