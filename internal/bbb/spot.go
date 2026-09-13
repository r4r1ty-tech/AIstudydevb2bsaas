package bbb

import (
	"context"
	"io"
	"log"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/wake"
)

func (w *Worker) lectureUsers(lesson model.Lesson, now time.Time) []model.User {
	if w == nil || w.Store == nil {
		return nil
	}
	users, err := w.Store.ListUsers()
	if err != nil {
		return nil
	}
	out := make([]model.User, 0, len(users))
	for _, u := range users {
		if !u.Active(now) || !lesson.MatchesSubgroup(u.Subgroup) {
			continue
		}
		intent, err := w.Store.GetIntent(u.TelegramID, lesson.ID)
		if err != nil || !WantsJoin(intent) {
			continue
		}
		out = append(out, u)
	}
	return out
}

func (w *Worker) startSpotter(ctx context.Context, rec *capture.Rec, lesson model.Lesson, users []model.User) {
	if rec == nil {
		return
	}
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
		log.Printf("wake: %v — пейджер молчит", err)
	} else {
		eng.Rec = vosk
		if c, ok := vosk.(io.Closer); ok {
			defer func() { _ = c.Close() }()
		}
	}

	buf := make([]byte, capture.WakeRate*2/5) // 200ms s16le
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
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
			return
		}
	}
}

func (w *Worker) onWake(ctx context.Context, lesson model.Lesson, users []model.User, word string) {
	if w == nil || word == "" {
		return
	}
	msg := wake.Message(lesson.Discipline, word)
	log.Printf("wake: %s", msg)
	if w.Store != nil {
		_ = w.Store.AddEvent(model.Event{
			At: time.Now(), Type: model.EventWake, LessonID: lesson.ID, Message: word,
		})
	}
	for _, u := range wake.WhoGets(word, users) {
		notify.User(ctx, w.Cfg, u.TelegramID, msg)
	}
}
