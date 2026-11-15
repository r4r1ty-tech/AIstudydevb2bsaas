package bbb

import (
	"context"
	"fmt"
	"io"
	"sync"
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
		if !u.Active(now) || !lesson.For(u) {
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

// subgroupUsers — все активные студенты подгруппы пары (для словаря вейквордов).
func (w *Worker) subgroupUsers(lesson model.Lesson, now time.Time) []model.User {
	if w == nil || w.Store == nil {
		return nil
	}
	users, err := w.Store.ListUsers()
	if err != nil {
		logx.Errorf("bbb", "subgroupUsers: list users: %v", err)
		return nil
	}
	out := make([]model.User, 0, len(users))
	for _, u := range users {
		if u.Active(now) && lesson.For(u) {
			out = append(out, u)
		}
	}
	return out
}

// recorder is a running capture: 16 kHz PCM for the spotter, Stop to finish.
type recorder interface {
	io.Reader
	Stop() error
}

// startRecorder остаётся точкой подмены для юнит-тестов воркера; в бою
// запись запускается через StreamRecorder самой прямой WebRTC-сессии.
var startRecorder = func(_ context.Context, _ string) (recorder, error) {
	return nil, fmt.Errorf("запись доступна только для StreamRecorder-сессии")
}

func startSessionRecorder(ctx context.Context, sess Session, path string) (recorder, error) {
	if sr, ok := sess.(StreamRecorder); ok {
		return sr.StartRecording(ctx, path)
	}
	return startRecorder(ctx, path)
}

// restartRec — запись, которая переживает смерть ffmpeg посреди лекции
// (сбой декодера, OOM): Read при неожиданном конце потока поднимает новый
// сегмент (MergeSegments потом склеит все), Stop — останавливает насовсем.
type restartRec struct {
	mu      sync.Mutex
	cur     recorder
	stopped bool
	start   func() (recorder, error)
	onDied  func(err error, restarted bool)
	n       int
}

const maxRecRestarts = 5

func (r *restartRec) Read(p []byte) (int, error) {
	for {
		r.mu.Lock()
		cur := r.cur
		r.mu.Unlock()
		n, err := cur.Read(p)
		if err == nil || n > 0 {
			return n, nil
		}
		r.mu.Lock()
		if r.stopped || r.n >= maxRecRestarts || r.start == nil {
			r.mu.Unlock()
			if !r.stopped && r.onDied != nil {
				r.onDied(err, false)
			}
			return 0, err
		}
		r.n++
		r.mu.Unlock()
		_ = cur.Stop()
		time.Sleep(2 * time.Second)
		next, serr := r.start()
		r.mu.Lock()
		if r.stopped {
			r.mu.Unlock()
			if next != nil {
				_ = next.Stop()
			}
			return 0, io.EOF
		}
		if serr != nil {
			r.mu.Unlock()
			if r.onDied != nil {
				r.onDied(fmt.Errorf("%v; перезапуск: %w", err, serr), false)
			}
			return 0, err
		}
		r.cur = next
		r.mu.Unlock()
		if r.onDied != nil {
			r.onDied(err, true)
		}
	}
}

func (r *restartRec) Stop() error {
	r.mu.Lock()
	r.stopped = true
	cur := r.cur
	r.mu.Unlock()
	return cur.Stop()
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
		if w != nil && lesson.Discipline != "тест" {
			notify.Admin(ctx, w.Cfg, fmt.Sprintf("запись «%s» идёт, но вейкворды не слушаются: %v", lesson.Discipline, err))
		}
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

	// Цикл только читает PCM: Telegram и pactl уходят в отдельную горутину.
	// Пайп ffmpeg — 2 с звука; заблокированный читатель тормозит и ogg-выход.
	jobs := make(chan func(), 64)
	defer close(jobs)
	go func() {
		for f := range jobs {
			f()
		}
	}()
	later := func(f func()) {
		select {
		case jobs <- f:
		default:
			logx.Warnf("bbb", "startSpotter: очередь уведомлений полна, пропускаю")
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
			if ev := meter.Feed(buf[:n]); ev != capture.LevelNone {
				peak, silent, windows := meter.Last()
				later(func() { w.onLevel(ctx, lesson, ev, peak, silent, windows) })
			}
			for _, h := range eng.Feed(buf[:n], capture.WakeRate, vocab) {
				word := h.Word
				later(func() { w.onWake(ctx, lesson, users, word) })
			}
		}
		if err != nil {
			logx.Debugf("bbb", "startSpotter: read: %v", err)
			return
		}
	}
}

// silenceAlertMinutes of silence in a row mean the recording session most likely
// never got audio (listen-only SFU not joined or stream has no incoming RTP).
const silenceAlertMinutes = 5

func (w *Worker) onLevel(ctx context.Context, lesson model.Lesson, ev capture.LevelEvent, peak, silent, windows int) {
	if ev == capture.LevelNone {
		return
	}
	logx.Infof("capture", "audio level %q min=%d peak=%d (%.1f dBFS) silent_streak=%d", lesson.Discipline, windows, peak, capture.DBFS(peak), silent)
	// Пишущая сессия заходит за 5–15 минут до начала и может ждать в лобби:
	// тишина до звонка — норма, алерт на каждой паре никому не нужен.
	if !lesson.Begin.IsZero() && time.Now().Before(lesson.Begin.Add(silenceAlertMinutes*time.Minute)) {
		return
	}
	switch ev {
	case capture.LevelSilent:
		logx.Warnf("capture", "recording %q silent for %d min — входящего аудио нет", lesson.Discipline, silent)
		if w != nil {
			notify.Admin(ctx, w.Cfg, fmt.Sprintf("запись «%s»: %d мин тишины. Либо лектор молчит, либо listen-only поток пуст.", lesson.Discipline, silent))
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
		_ = w.Store.SetSetting("moodle_wake_kick", fmt.Sprintf("%d:%s", lesson.ID, word))
		if w.Cfg != nil && w.Cfg.AdminID > 0 {
			_ = w.Store.SetSetting(fmt.Sprintf("moodle_kick:%d", w.Cfg.AdminID), "1")
		}
		for _, u := range users {
			if u.TelegramID > 0 {
				_ = w.Store.SetSetting(fmt.Sprintf("moodle_kick:%d", u.TelegramID), "1")
			}
		}
		logx.Infof("bbb", "wake %q on %q -> triggered moodle kick", word, lesson.Discipline)
	}
	if lesson.Discipline == "тест" {
		notify.Admin(ctx, w.Cfg, "тест услышал: «"+word+"»")
		return
	}
	// Адресаты — на момент срабатывания: нажавшие «Зайти» после старта записи
	// тоже получают вейкворды.
	if fresh := w.lectureUsers(lesson, time.Now()); len(fresh) > 0 {
		users = fresh
	}
	for _, u := range wake.WhoGets(word, users) {
		logx.Debugf("bbb", "onWake: notify tg=%d word=%q", u.TelegramID, word)
		notify.User(ctx, w.Cfg, u.TelegramID, msg)
	}
}
