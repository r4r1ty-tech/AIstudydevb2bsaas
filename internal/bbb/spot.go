package bbb

import (
	"context"
	"fmt"
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

// recorder is a running capture: 16 kHz PCM for the spotter, Stop to finish.
type recorder interface {
	io.Reader
	Stop() error
}

// startRecorder starts ffmpeg on ssau_rec.monitor; tests swap it so they
// never touch the PulseAudio of the machine they run on.
var startRecorder = func(ctx context.Context, path string) (recorder, error) {
	r, err := capture.Start(ctx, path)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (w *Worker) startSpotter(ctx context.Context, rec recorder, lesson model.Lesson, users []model.User) {
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

	meter := capture.NewMeter(capture.WakeRate, 60, silenceAlertMinutes)
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
			w.onLevel(ctx, lesson, meter, meter.Feed(buf[:n]))
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

// silenceAlertMinutes of silence in a row mean the recording tab most likely
// never got audio (listen-only not joined, stream not routed to ssau_rec).
const silenceAlertMinutes = 5

func (w *Worker) onLevel(ctx context.Context, lesson model.Lesson, m *capture.Meter, ev capture.LevelEvent) {
	if ev == capture.LevelNone || m == nil {
		return
	}
	peak, silent, windows := m.Last()
	logx.Infof("capture", "audio level %q min=%d peak=%d (%.1f dBFS) silent_streak=%d", lesson.Discipline, windows, peak, capture.DBFS(peak), silent)
	if windows == 1 {
		capture.LogRoute("capture", "first-minute")
	}
	switch ev {
	case capture.LevelSilent:
		logx.Warnf("capture", "recording %q silent for %d min — аудио, похоже, не подключилось", lesson.Discipline, silent)
		capture.LogRoute("capture", "silent")
		if w != nil {
			notify.Admin(ctx, w.Cfg, fmt.Sprintf("запись «%s»: %d мин тишины. Либо лектор молчит, либо вкладка не подключила звук — глянь журнал (audio route silent).", lesson.Discipline, silent))
		}
	case capture.LevelBack:
		logx.Infof("capture", "recording %q: sound is back, peak=%d", lesson.Discipline, peak)
		if w != nil {
			notify.Admin(ctx, w.Cfg, "запись «"+lesson.Discipline+"»: звук появился.")
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
