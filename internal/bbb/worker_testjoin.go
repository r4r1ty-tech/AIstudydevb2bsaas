package bbb

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
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
	w.ensureTestN(ctx, tj, now, 0)
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

	log.Printf("bbb: test join begin want=%s name=%q url=%s", tj.Want, tj.GuestName(), url)
	sess, err := w.Joiner.Join(ctx, JoinReq{URL: url, FIO: tj.GuestName(), Role: role})
	if err != nil {
		if aborted, latest := w.testJoinAborted(tj.Want); aborted {
			log.Printf("bbb: test join aborted(leave) after err=%v", err)
			_ = w.Store.PutTestJoin(latest)
			return
		}
		log.Printf("bbb: test join fail: %v", err)
		tj.Status = model.TestError
		tj.Mode = ""
		tj.Message = err.Error()
		_ = w.Store.PutTestJoin(tj)
		notify.Admin(ctx, w.Cfg, "тест BBB: не зашёл — "+err.Error())
		return
	}
	log.Printf("bbb: test Join() ok, classifying lobby/meeting")
	if aborted, latest := w.testJoinAborted(tj.Want); aborted {
		_ = sess.Close()
		_ = w.Store.PutTestJoin(latest)
		return
	} else if latest.Want != "" && latest.Want != tj.Want {
		_ = sess.Close()
		w.ensureTestN(ctx, latest, now, depth+1)
		return
	}
	if tj.Want == model.TestWantListen {
		sess = w.attachTestRecorder(ctx, sess)
	}

	state := model.TestRoom
	msg := "в комнате"
	if lobby, _ := sess.InLobby(ctx); lobby {
		state = model.TestLobby
		msg = "лобби, жду модератора"
	} else if ok, _ := sess.InMeeting(ctx); !ok {
		_ = sess.Close()
		if aborted, latest := w.testJoinAborted(tj.Want); aborted {
			_ = w.Store.PutTestJoin(latest)
			return
		}
		tj.Status = model.TestError
		tj.Mode = ""
		tj.Message = "форма прошла, но комнаты нет"
		_ = w.Store.PutTestJoin(tj)
		notify.Admin(ctx, w.Cfg, "тест BBB: не зашёл — нет UI комнаты")
		return
	}
	if aborted, latest := w.testJoinAborted(tj.Want); aborted {
		_ = sess.Close()
		_ = w.Store.PutTestJoin(latest)
		return
	} else if latest.Want != "" {
		tj.URL = latest.URL
		tj.Name = latest.Name
		tj.Want = latest.Want
	}

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
	log.Printf("bbb: test status=%s want=%s name=%q", state, tj.Want, tj.GuestName())
	_ = w.Store.AddEvent(model.Event{
		At: now, Type: model.EventJoin, TelegramID: w.adminID(), Message: "тест " + tj.Want,
	})
	if state == model.TestLobby {
		notify.Admin(ctx, w.Cfg, "тест: лобби как «"+tj.GuestName()+"». Пусти из модерации.")
		return
	}
	notify.Admin(ctx, w.Cfg, testInRoomText(tj))
}

// testJoinAborted reports leave/off while Join was in flight and returns a
// store-ready idle row so a late PutTestJoin cannot revive want/status.
func (w *Worker) testJoinAborted(wantBefore string) (bool, model.TestJoin) {
	latest, err := w.Store.GetTestJoin()
	if err != nil {
		return false, latest
	}
	if latest.Want == model.TestWantOff {
		latest.Status = model.TestIdle
		latest.Mode = ""
		latest.Message = "вышел"
		return true, latest
	}
	if wantBefore != "" && latest.Want != "" && latest.Want != wantBefore {
		return false, latest
	}
	return false, latest
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
	if ok, _ := sess.InMeeting(ctx); !ok {
		w.stopTest(ctx, "lost", false)
		if latest, e := w.Store.GetTestJoin(); e == nil {
			latest.Status = model.TestError
			latest.Want = model.TestWantOff
			latest.Mode = ""
			latest.Message = "пропал из комнаты"
			_ = w.Store.PutTestJoin(latest)
		}
		notify.Admin(ctx, w.Cfg, "тест BBB: пропал из комнаты (UI нет).")
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
	out := filepath.Join(w.recRoot(), "test", "audio.ogg")
	rec, err := capture.Start(ctx, out)
	if err != nil {
		log.Printf("bbb: test ffmpeg: %v", err)
		notify.Admin(ctx, w.Cfg, "тест: звук не стартанул — "+err.Error())
		return sess
	}
	users, err := w.Store.ListUsers()
	if err != nil {
		users = nil
	}
	lesson := model.Lesson{Discipline: "тест"}
	go w.startSpotter(ctx, rec, lesson, users)
	return &closeHook{Session: sess, fn: func() { _ = rec.Stop() }}
}
