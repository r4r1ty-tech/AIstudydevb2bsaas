package bbb

import (
	"context"
	"io"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/wake"
)

func (w *Worker) lectureUsers(lesson model.Lesson, now time.Time) []model.User {
	if w == nil || w.Store == nil {
		logx.Debugf("bbb", "lectureUsers: no store, skip")
		return nil
	}
	logx.Debugf("bbb", "lectureUsers: lesson=%d discipline=%q", lesson.ID, lesson.Discipline)
	users, err := w.Store.ListUsers()
	if err != nil {
		logx.Errorf("bbb", "lectureUsers: list users: %v", err)
		return nil
	}
	out := make([]model.User, 0, len(users))
	for _, u := range users {
		if !u.Active(now) || !lesson.MatchesSubgroup(u.Subgroup) {
			continue
		}
		intent, err := w.Store.GetIntent(u.TelegramID, lesson.ID)
		if err != nil {
			logx.Debugf("bbb", "lectureUsers: intent tg=%d lesson=%d: %v", u.TelegramID, lesson.ID, err)
			continue
		}
		if !WantsJoin(intent) {
			continue
		}
		out = append(out, u)
	}
	logx.Debugf("bbb", "lectureUsers: lesson=%d users=%d", lesson.ID, len(out))
	return out
}

func (w *Worker) startSpotter(ctx context.Context, rec *capture.Rec, lesson model.Lesson, users []model.User) {
	if rec == nil {
		logx.Debugf("bbb", "startSpotter: no rec, skip")
		return
	}
	logx.Debugf("bbb", "startSpotter: lesson=%d users=%d", lesson.ID, len(users))
	vocab := wake.Vocab(users)
	if len(vocab) == 0 {
		vocab = append([]string{}, model.CommonWakeWords...)
	}
	eng := wake.NewEngine()
	modelDir, script := "", ""
	if w != nil && w.Cfg != nil {
		modelDir = w.Cfg.VoskModel
		script = w.Cfg.VoskScript
	}
	if vosk, err := wake.Open(modelDir, script, vocab); err != nil {
		logx.Warnf("wake", "vosk: %v — пейджер молчит", err)
	} else {
		eng.Rec = vosk
		if c, ok := vosk.(io.Closer); ok {
			defer func() {
				if err := c.Close(); err != nil {
					logx.Debugf("bbb", "startSpotter: vosk close: %v", err)
				}
			}()
		}
	}

	buf := make([]byte, capture.WakeRate*2/5) // 200ms s16le
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				logx.Debugf("bbb", "startSpotter: ctx done, stop")
				return
			default:
			}
		}
		n, err := rec.Read(buf)
		if n > 0 {
			for _, h := range eng.Feed(buf[:n], capture.WakeRate, vocab) {
				w.onWake(ctx, lesson, users, h.Word)
			}
		}
		if err != nil {
			logx.Debugf("bbb", "startSpotter: read: %v", err)
			return
		}
	}
}

func (w *Worker) onWake(ctx context.Context, lesson model.Lesson, users []model.User, word string) {
	if w == nil || word == "" {
		return
	}
	logx.Debugf("bbb", "onWake: lesson=%d word=%q", lesson.ID, word)
	msg := wake.Message(lesson.Discipline, word)
	logx.Infof("wake", "%s", msg)
	if w.Store != nil {
		if err := w.Store.AddEvent(model.Event{
			At: time.Now(), Type: model.EventWake, LessonID: lesson.ID, Message: word,
		}); err != nil {
			logx.Debugf("bbb", "onWake: add event: %v", err)
		}
	}
	if lesson.Discipline == "тест" {
		notify.Admin(ctx, w.Cfg, "тест услышал: «"+word+"»")
		return
	}
	for _, u := range wake.WhoGets(word, users) {
		logx.Debugf("bbb", "onWake: notify tg=%d word=%q", u.TelegramID, word)
		notify.User(ctx, w.Cfg, u.TelegramID, msg)
	}
}
