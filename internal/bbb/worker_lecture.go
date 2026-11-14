package bbb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notes"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/publish"
)

const (
	slidesDoneKey = "slides_done:"
	notesDoneKey  = "notes_done:"

	minRecordedBytes = 20000

	publishedTTL = 24 * time.Hour
	publishRetry = 5 * time.Minute

	// Первые попытки идут часто (сеть моргнула), дальше — редко: сбой
	// провайдера на полдня не должен стоить конспекта.
	maxNoteAttempts  = 10
	noteQuickRetries = 3
	noteRetryEvery   = 10 * time.Minute
	noteRetrySlow    = 2 * time.Hour
	// Сколько прошлых дней догоняем, если их конспекты не закрыты.
	noteCatchUpDays = 7

	noteAttemptsKey = "notes_attempts:"
)

func (w *Worker) recRoot() string {
	if w != nil && w.Cfg != nil && w.Cfg.RecordingsDir != "" {
		return w.Cfg.RecordingsDir
	}
	return config.DefaultRecordings
}

// setStatus меняет статус пака и пишет переход с причиной. Сохраняет вызывающий.
func (w *Worker) setStatus(p *model.LecturePack, to, reason string) {
	if p == nil {
		return
	}
	from := p.Status
	if from == to {
		logx.Debugf("bbb", "pack: %s статус уже %s (%s)", packTag(p), to, reason)
		return
	}
	p.Status = to
	logx.Infof("bbb", "pack: %s статус %s → %s, причина: %s", packTag(p), from, to, reason)
}

func statSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// rtpOf — сколько RTP-пакетов принял WebRTC-сессия; false, если сессия не считает.
func rtpOf(sess Session) (uint64, bool) {
	if r, ok := sess.(interface{ RTPPackets() uint64 }); ok {
		return r.RTPPackets(), true
	}
	return 0, false
}

func rtpText(n uint64, ok bool) string {
	if !ok {
		return "н/д"
	}
	return strconv.FormatUint(n, 10)
}

// attachRecorder вешает запись на вкладку; recording=false — запись не стартовала,
// вкладка остаётся просто присутствием.
func (w *Worker) attachRecorder(ctx context.Context, sess Session, lesson model.Lesson, url string) (Session, bool) {
	if w == nil || w.Store == nil || sess == nil {
		return sess, false
	}
	logx.Infof("bbb", "recording: старт записи lesson=%d %s url=%s", lesson.ID, lesson.Discipline, redactURL(url))
	recStarted := time.Now()
	pack, err := w.Store.EnsurePack(lesson, url, w.recRoot())
	if err != nil || pack == nil {
		logx.Errorf("bbb", "recording: lesson=%d %s пак не получен: %v", lesson.ID, lesson.Discipline, err)
		return sess, false
	}
	logx.Debugf("bbb", "recording: %s пак получен статус=%s dir=%s", packTag(pack), pack.Status, pack.Dir)
	abs := filepath.Join(w.recRoot(), pack.Dir)
	slidesDir := archive.SlidesDir(w.recRoot(), lesson.Discipline, pack.Number)
	seg := capture.SegmentPath(abs, time.Now().UnixNano())
	first, err := startSessionRecorder(ctx, sess, seg)
	if err != nil {
		logx.Errorf("bbb", "recording: %s не стартовала: %v", packTag(pack), err)
		notify.Admin(ctx, w.Cfg, "запись «"+lesson.Discipline+"» не стартовала: "+err.Error())
		return sess, false
	}
	rec := &restartRec{
		cur: first,
		start: func() (recorder, error) {
			next := capture.SegmentPath(abs, time.Now().UnixNano())
			logx.Infof("bbb", "recording: %s новый сегмент %s (перезапуск после падения)", packTag(pack), filepath.Base(next))
			return startSessionRecorder(ctx, sess, next)
		},
		onDied: func(err error, restarted bool) {
			if restarted {
				logx.Warnf("bbb", "recording: %s ffmpeg умер: %v — перезапустил запись", packTag(pack), err)
				go notify.Admin(ctx, w.Cfg, fmt.Sprintf("запись «%s»: ffmpeg упал (%v), перезапустил — в записи может быть короткая дырка", lesson.Discipline, err))
				return
			}
			logx.Errorf("bbb", "recording: %s ffmpeg умер: %v — запись остановлена (перезапуск невозможен или лимит исчерпан)", packTag(pack), err)
			go notify.Admin(ctx, w.Cfg, fmt.Sprintf("запись «%s» остановилась: %v", lesson.Discipline, err))
		},
	}
	logx.Infof("bbb", "recording: старт %s сегмент=%s за %s", packTag(pack), filepath.Base(seg), time.Since(recStarted).Round(time.Millisecond))
	w.setStatus(pack, model.PackRecording, "вкладка с записью подключилась")
	if err := w.Store.SavePack(pack); err != nil {
		logx.Errorf("bbb", "attachRecorder: save recording pack=%s: %v", pack.Dir, err)
	}
	if err := w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventRecord, LessonID: lesson.ID,
		Message: archive.Rel(lesson.Discipline, pack.Number),
	}); err != nil {
		logx.Debugf("bbb", "attachRecorder: add event: %v", err)
	}

	var slidesCaptured atomic.Bool
	slidesCtx, slidesCancel := context.WithCancel(ctx)
	var slidesWG sync.WaitGroup
	slidesWG.Add(1)
	go func() {
		defer slidesWG.Done()
		ticker := time.NewTicker(45 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-slidesCtx.Done():
				return
			case <-ticker.C:
				n, err := sess.GrabSlides(slidesCtx, slidesDir)
				if err != nil {
					logx.Debugf("bbb", "background GrabSlides lesson=%d: %v", lesson.ID, err)
				}
				if n > 0 {
					slidesCaptured.Store(true)
					w.MarkSlides(lesson.ID, slidesDir)
				}
			}
		}
	}()

	// Словарь — по всей подгруппе: Vosk не перезапускается, а «Зайти» могут
	// нажать уже после старта записи. Адресатов onWake считает на момент хита.
	go w.startSpotter(ctx, rec, lesson, w.subgroupUsers(lesson, time.Now()))
	st := w.Store
	return &closeHook{Session: sess, fn: func() {
		slidesCancel()
		slidesWG.Wait()

		// Финальный сбор слайдов перед закрытием сессии
		fctx, fcancel := context.WithTimeout(context.Background(), 10*time.Second)
		n, err := sess.GrabSlides(fctx, slidesDir)
		fcancel()
		if err != nil {
			logx.Debugf("bbb", "final GrabSlides %s: %v", packTag(pack), err)
		}
		if n > 0 {
			slidesCaptured.Store(true)
			w.MarkSlides(lesson.ID, slidesDir)
		}

		// RTP и размер снимаем до Stop: после него сессия может уже отпустить трек.
		rtp, hasRTP := rtpOf(sess)
		logx.Infof("bbb", "recording: стоп %s после %s, RTP=%s, слайдов в папке=%d",
			packTag(pack), time.Since(recStarted).Round(time.Second), rtpText(rtp, hasRTP), n)
		if err := rec.Stop(); err != nil {
			logx.Warnf("bbb", "recording: остановка %s: %v", packTag(pack), err)
		}
		nSegs, merr := capture.MergeSegments(context.Background(), abs, archive.AudioFile(abs))
		if merr != nil {
			logx.Warnf("bbb", "recording: склейка %s не удалась: %v — статус остаётся recording, разберёт promoteStuckRecording", packTag(pack), merr)
		} else {
			audio := archive.AudioFile(abs)
			logx.Infof("bbb", "recording: %s файл=%s размер=%d байт сегментов=%d", packTag(pack), audio, statSize(audio), nSegs)
		}
		if merr != nil {
			return
		}
		p, err := st.PackByID(pack.ID)
		if err != nil || p == nil {
			logx.Warnf("bbb", "recording: %s не перечитался после записи: %v", packTag(pack), err)
			return
		}
		if archive.HasSlides(slidesDir) || slidesCaptured.Load() {
			w.setStatus(p, model.PackSlides, "запись остановлена, слайды есть")
		} else {
			w.setStatus(p, model.PackRecorded, "запись остановлена, слайдов нет")
		}
		if err := st.SavePack(p); err != nil {
			logx.Errorf("bbb", "attachRecorder: save recorded pack=%s: %v", p.Dir, err)
		}
	}}, true
}

// MarkSlides marks that slides have been captured for a lecture pack.
func (w *Worker) MarkSlides(lessonID int64, slidesDir string) {
	if w == nil || w.Store == nil {
		return
	}
	p, err := w.Store.PackByLesson(lessonID)
	if err != nil || p == nil {
		logx.Debugf("bbb", "MarkSlides: pack not found lesson=%d: %v", lessonID, err)
		return
	}
	if p.Status != model.PackDone && p.Status != model.PackNotes && p.Status != model.PackRecording {
		w.setStatus(p, model.PackSlides, "слайды сняты с презентации")
		if err := w.Store.SavePack(p); err != nil {
			logx.Errorf("bbb", "MarkSlides: save %s: %v", packTag(p), err)
		}
	} else {
		logx.Debugf("bbb", "MarkSlides: %s статус %s — слайды не меняют его", packTag(p), p.Status)
	}
	if err := w.Store.AddEvent(model.Event{
		At:       time.Now(),
		Type:     model.EventSlides,
		LessonID: lessonID,
		Message:  archive.Rel(p.Discipline, p.Number),
	}); err != nil {
		logx.Debugf("bbb", "MarkSlides: add event: %v", err)
	}
	logx.Infof("bbb", "MarkSlides: lesson=%d dir=%s", lessonID, slidesDir)
}

func (w *Worker) allLecturesFinishedToday() bool {
	if w == nil || w.Store == nil {
		return false
	}
	all, err := w.Store.ListLessons()
	if err != nil {
		return false
	}
	return archive.AllLecturesFinished(all, time.Now())
}

func (w *Worker) maybeHarvest(ctx context.Context, now time.Time) {
	if w == nil || w.Store == nil {
		return
	}
	all, err := w.Store.ListLessons()
	if err != nil || !archive.ShouldHarvest(all, now, archive.HarvestGrace) {
		if err != nil {
			logx.Debugf("bbb", "maybeHarvest: list lessons: %v", err)
		}
		return
	}
	day := now.Format("2006-01-02")
	if w.settingOn(slidesDoneKey + day) {
		logx.Debugf("bbb", "maybeHarvest: already done day=%s", day)
		return
	}
	// Слайды не поднимают третий Chromium, пока идёт пара/тест.
	if !w.beginJob(true) {
		logx.Debugf("bbb", "maybeHarvest: busy, skip day=%s", day)
		return
	}
	logx.Infof("bbb", "maybeHarvest: start day=%s", day)
	w.jobWG.Add(1)
	go func() {
		defer w.jobWG.Done()
		defer w.endJob()
		w.harvestDay(ctx, day)
		if all, err := w.Store.ListLessons(); err == nil && archive.AllLecturesFinished(all, time.Now()) {
			w.setSetting(slidesDoneKey+day, "1")
		}
	}()
}

func (w *Worker) maybeNotes(ctx context.Context, now time.Time) {
	if w == nil || w.Store == nil {
		return
	}
	days := w.openNoteDays(now)
	if len(days) == 0 {
		logx.Debugf("bbb", "maybeNotes: нет открытых дней")
		return
	}
	if !w.beginJob(true) {
		logx.Debugf("bbb", "maybeNotes: busy days=%v", days)
		return
	}
	logx.Infof("bbb", "maybeNotes: start days=%v", days)
	w.jobWG.Add(1)
	go func() {
		defer w.jobWG.Done()
		defer w.endJob()
		loc := w.Loc
		if loc == nil {
			loc = time.UTC
		}
		today := time.Now().In(loc).Format("2006-01-02")
		for _, day := range days {
			if ctx.Err() != nil {
				return
			}
			if w.buildNotesDay(ctx, day) {
				if day == today && !w.allLecturesFinishedToday() {
					logx.Debugf("bbb", "maybeNotes: day=%s done for now, but more lectures today", day)
					continue
				}
				logx.Infof("bbb", "notes: день %s закрыт — все конспекты дня собраны или сдались", day)
				w.setSetting(notesDoneKey+day, "1")
			}
		}
	}()
}

// openNoteDays: проверяет прошедшие незакрытые дни, а также сегодняшний день,
// если на сегодня уже есть паки и день ещё не помечен как notes_done.
func (w *Worker) openNoteDays(now time.Time) []string {
	loc := w.Loc
	if loc == nil {
		loc = time.UTC
	}
	today := now.In(loc).Format("2006-01-02")
	yesterday := archive.NotesDay(now)
	first := now.In(loc).AddDate(0, 0, -noteCatchUpDays).Format("2006-01-02")

	var days []string
	seen := map[string]bool{}
	if packs, err := w.Store.ListPacks(); err != nil {
		logx.Errorf("bbb", "openNoteDays: list packs: %v", err)
	} else {
		for _, p := range packs {
			if p.Date < first || p.Date > today || seen[p.Date] {
				continue
			}
			seen[p.Date] = true
			if !w.settingOn(notesDoneKey + p.Date) {
				days = append(days, p.Date)
			}
		}
		sort.Strings(days)
	}
	if yesterday != "" && !seen[yesterday] && !w.settingOn(notesDoneKey+yesterday) {
		days = append(days, yesterday)
		sort.Strings(days)
	}
	return days
}

func (w *Worker) harvestDay(ctx context.Context, day string) {
	logx.Debugf("bbb", "harvestDay: day=%s", day)
	packs, err := w.Store.PacksByDate(day)
	if err != nil {
		logx.Errorf("bbb", "packs %s: %v", day, err)
		return
	}
	logx.Debugf("bbb", "harvestDay: day=%s packs=%d", day, len(packs))
	for i := range packs {
		p := packs[i]
		if p.Status == model.PackRecorded {
			slidesDir := archive.SlidesDir(filepath.Join(w.recRoot(), p.Dir))
			if !archive.HasSlides(slidesDir) {
				w.harvestSlides(ctx, &p)
			} else {
				logx.Debugf("bbb", "harvestDay: %s слайды уже есть в %s — не снимаю", packTag(&p), slidesDir)
			}
		} else {
			logx.Debugf("bbb", "harvestDay: %s статус %s — слайды не снимаю", packTag(&p), p.Status)
		}
	}
}

func (w *Worker) buildNotesDay(ctx context.Context, day string) bool {
	logx.Debugf("bbb", "buildNotesDay: day=%s", day)
	packs, err := w.Store.PacksByDate(day)
	if err != nil {
		logx.Errorf("bbb", "notes packs %s: %v", day, err)
		return false
	}
	now := time.Now()
	done := true
	for i := range packs {
		p := packs[i]
		if p.Status == model.PackRecording {
			switch w.promoteStuckRecording(ctx, &p) {
			case stuckRecorded:
				p.Status = model.PackRecorded
			case stuckEmpty:
				continue
			default:
				done = false
				continue
			}
		}
		// Если лекция ещё идёт (Finish > now), не собираем конспект раньше времени
		if lesson, err := w.Store.LessonByID(p.LessonID); err == nil && lesson != nil {
			if !lesson.Finish.IsZero() && now.Before(lesson.Finish) {
				logx.Debugf("bbb", "buildNotesDay: %s пропуск: пара идёт до %s, конспект не раньше конца", packTag(&p), lesson.Finish.Format("15:04:05"))
				done = false
				continue
			}
		}
		if p.Status == model.PackError {
			// Битый конспект пересобираем с паузами, но не бесконечно.
			if !w.noteRetryReady(&p, now) {
				if w.noteAttemptsFor(p.ID) < maxNoteAttempts {
					logx.Debugf("bbb", "buildNotesDay: %s повтор позже: попыток %d/%d, день открыт", packTag(&p), w.noteAttemptsFor(p.ID), maxNoteAttempts)
					done = false
				} else {
					logx.Debugf("bbb", "buildNotesDay: %s попытки исчерпаны (%d), не пересобираю", packTag(&p), maxNoteAttempts)
				}
				continue
			}
			if !w.buildNotes(ctx, p.ID) {
				if w.noteAttemptsFor(p.ID) < maxNoteAttempts {
					done = false
				}
			} else if w.packNeedsDayOpen(p.ID) {
				// Пак не пересобирался (lease держит другой воркер, статус ещё не финальный или ошибка чтения) — день не закрываем.
				done = false
			}
			continue
		}
		if !archive.ShouldNotePack(p.Status) {
			logx.Debugf("bbb", "buildNotesDay: %s статус %s — конспект не нужен", packTag(&p), p.Status)
			continue
		}
		// Упавший пак держит день открытым, пока есть попытки: иначе
		// notes_done ставится сразу и ретраев с паузой не бывает.
		if !w.buildNotes(ctx, p.ID) {
			if w.noteAttemptsFor(p.ID) < maxNoteAttempts {
				done = false
			}
		} else if w.packNeedsDayOpen(p.ID) {
			// Другой воркер ещё собирает конспект или перевёл пак в error с оставшимися попытками — день не закрываем.
			done = false
		}
	}
	logx.Debugf("bbb", "buildNotesDay: day=%s done=%v", day, done)
	return done
}

// packNeedsDayOpen проверяет после пропущенной/чужой сборки, должен ли день оставаться открытым.
func (w *Worker) packNeedsDayOpen(packID int64) bool {
	cur, err := w.Store.PackByID(packID)
	if err != nil || cur == nil {
		return true
	}
	return archive.ShouldNotePack(cur.Status) || (cur.Status == model.PackError && w.noteAttemptsFor(cur.ID) < maxNoteAttempts)
}

func (w *Worker) noteRetryReady(p *model.LecturePack, now time.Time) bool {
	if p == nil {
		return false
	}
	n := w.noteAttemptsFor(p.ID)
	if n >= maxNoteAttempts {
		return false
	}
	ready := !p.UpdatedAt.After(now.Add(-noteRetryGap(n)))
	logx.Debugf("bbb", "noteRetryReady: pack=%d attempts=%d ready=%v", p.ID, n, ready)
	return ready
}

func noteRetryGap(attempts int) time.Duration {
	if attempts < noteQuickRetries {
		return noteRetryEvery
	}
	return noteRetrySlow
}

// Счётчик попыток лежит в settings: рестарт воркера (деплой, падение) не
// должен ни обнулять лимит, ни стирать память о том, что пак ещё не собран.
func (w *Worker) noteAttemptsFor(id int64) int {
	raw, ok, err := w.Store.GetSetting(noteAttemptsKey + strconv.FormatInt(id, 10))
	if err != nil || !ok {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(raw))
	return n
}

func (w *Worker) noteFail(id int64) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := w.noteAttemptsFor(id) + 1
	w.setSetting(noteAttemptsKey+strconv.FormatInt(id, 10), strconv.Itoa(n))
	logx.Debugf("bbb", "noteFail: pack=%d attempts=%d", id, n)
	return n
}

func (w *Worker) noteOK(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.noteAttemptsFor(id) != 0 {
		w.setSetting(noteAttemptsKey+strconv.FormatInt(id, 10), "0")
	}
}

type stuckResult int

const (
	stuckWait     stuckResult = iota // ещё пишется — день не закрываем
	stuckRecorded                    // пересклеили, есть звук
	stuckEmpty                       // звука нет — пак закрыт как empty
)

// promoteStuckRecording: пак вчерашнего дня всё ещё «recording» — процесс
// убили посреди лекции или склейка упала. Пересклеиваем сегменты; есть звук —
// recorded, нет — empty (с сообщением админу), а не WARN каждые 15 с весь день.
func (w *Worker) promoteStuckRecording(ctx context.Context, p *model.LecturePack) stuckResult {
	if p == nil || w.Store == nil || strings.TrimSpace(p.Dir) == "" {
		return stuckWait
	}
	w.mu.Lock()
	var recKey string
	var live bool
	for _, mk := range []string{p.MeetingKey, fmt.Sprintf("lesson:%d", p.LessonID)} {
		if mk == "" {
			continue
		}
		if rk := w.recorder[mk]; rk != "" {
			if _, ok := w.sessions[rk]; ok {
				recKey = rk
				live = true
				break
			}
		}
	}
	w.mu.Unlock()
	if recKey != "" && live {
		logx.Debugf("bbb", "promoteStuckRecording: %s ещё пишется в живой сессии %s — жду", packTag(p), recKey)
		return stuckWait
	}
	dir := filepath.Join(w.recRoot(), p.Dir)
	var audio string
	if strings.TrimSpace(p.Audio) == "" {
		audio = archive.AudioFile(dir)
	} else {
		audio = filepath.Join(w.recRoot(), p.Audio)
	}
	if n, err := capture.MergeSegments(ctx, dir, audio); err != nil {
		logx.Debugf("bbb", "promoteStuckRecording: merge %s: %v", p.Dir, err)
	} else if n > 0 {
		logx.Infof("bbb", "promoteStuckRecording: склеил %d сегментов %s", n, p.Dir)
	}
	st, err := os.Stat(audio)
	if err != nil || st.Size() < minRecordedBytes {
		size := int64(0)
		if st != nil {
			size = st.Size()
		}
		logx.Warnf("bbb", "recording: %s записи нет (%d байт, порог %d) — закрываю как empty", packTag(p), size, minRecordedBytes)
		w.setStatus(p, model.PackEmpty, fmt.Sprintf("склейка дала %d байт, порог %d", size, minRecordedBytes))
		p.Err = fmt.Sprintf("записи нет (%d байт)", size)
		if err := w.Store.SavePack(p); err != nil {
			logx.Errorf("bbb", "promoteStuckRecording: save empty pack=%s: %v", p.Dir, err)
			return stuckWait
		}
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("лекция «%s» %s: записи нет — конспекта не будет", p.Discipline, p.Date))
		return stuckEmpty
	}
	w.setStatus(p, model.PackRecorded, fmt.Sprintf("процесс записи не дошёл до конца, склейка дала %d байт", st.Size()))
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "promoteStuckRecording: save %s: %v", packTag(p), err)
	}
	return stuckRecorded
}

func (w *Worker) harvestSlides(ctx context.Context, p *model.LecturePack) {
	if p == nil || p.BBBURL == "" {
		logx.Debugf("bbb", "harvestSlides: %s пропуск: у пака нет ссылки на комнату", packTag(p))
		return
	}
	if w.Cfg != nil && w.Cfg.BBBDryRun {
		logx.Debugf("bbb", "harvestSlides: dry run, skip %s", packTag(p))
		return
	}
	logx.Infof("bbb", "harvestSlides: join %s url=%s — снимаю слайды после лекции", packTag(p), redactURL(p.BBBURL))
	fio := "архив"
	if users, err := w.Store.ListUsers(); err == nil {
		for _, u := range users {
			if u.FIO != "" && u.Active(time.Now()) {
				fio = u.FIO
				break
			}
		}
	} else {
		logx.Debugf("bbb", "harvestSlides: list users: %v", err)
	}
	harvestStart := time.Now()
	sess, err := w.Joiner.Join(ctx, JoinReq{URL: p.BBBURL, FIO: fio, Role: RoleSlides})
	if err != nil {
		logx.Warnf("bbb", "harvestSlides: %s вход за слайдами не удался за %s: %v", packTag(p), time.Since(harvestStart).Round(time.Millisecond), err)
		return
	}
	w.hogs().Hold()
	defer func() {
		if err := sess.Close(); err != nil {
			logx.Debugf("bbb", "harvestSlides: close: %v", err)
		}
		w.hogs().Release()
	}()
	// Join считает успехом и лобби / «встреча не началась» — слайдов там нет.
	if room, err := sess.InRoom(ctx); err != nil || !room {
		logx.Warnf("bbb", "harvestSlides: %s не в комнате (err=%v, лобби или встреча не идёт) — пропускаю", packTag(p), err)
		return
	}
	grabStart := time.Now()
	n, err := sess.GrabSlides(ctx, archive.SlidesDir(filepath.Join(w.recRoot(), p.Dir)))
	if err != nil {
		logx.Warnf("bbb", "harvestSlides: %s снятие слайдов за %s: %v", packTag(p), time.Since(grabStart).Round(time.Millisecond), err)
	}
	if n == 0 {
		logx.Warnf("bbb", "harvestSlides: %s презентации нет — статус не меняю", packTag(p))
		return
	}
	w.setStatus(p, model.PackSlides, fmt.Sprintf("слайды дособраны после лекции: %d", n))
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "harvestSlides: save %s: %v", packTag(p), err)
	}
	if err := w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventSlides, LessonID: p.LessonID,
		Message: archive.Rel(p.Discipline, p.Number),
	}); err != nil {
		logx.Debugf("bbb", "harvestSlides: add event: %v", err)
	}
	logx.Infof("bbb", "harvestSlides: %s готово, слайдов=%d за %s", packTag(p), n, time.Since(harvestStart).Round(time.Second))
	notify.Admin(ctx, w.Cfg, fmt.Sprintf("слайды сняты: %s (%d)", archive.Rel(p.Discipline, p.Number), n))
}

// buildNotes builds and publishes one pack; false means it failed.
func (w *Worker) buildNotes(ctx context.Context, id int64) bool {
	logx.Debugf("bbb", "buildNotes: pack=%d", id)
	p, err := w.Store.PackByID(id)
	if err != nil || p == nil {
		logx.Errorf("bbb", "buildNotes: pack %d: %v", id, err)
		return false
	}
	claimAt := time.Now().UTC()
	claimed, err := w.Store.ClaimPackForNotes(p.ID, claimAt)
	if err != nil {
		logx.Errorf("bbb", "buildNotes: claim %s: %v", packTag(p), err)
		return false
	}
	if !claimed {
		logx.Debugf("bbb", "buildNotes: %s уже захвачен или статус %s не требует сборки", packTag(p), p.Status)
		return true
	}
	// Сохраняем время захвата в структуре, чтобы SavePack при переходе в PackNotes
	// не затёр claimed_at обратно в NULL.
	p.ClaimedAt = &claimAt
	released := false
	defer func() {
		if !released {
			if rerr := w.Store.ReleasePackClaim(p.ID); rerr != nil {
				logx.Warnf("bbb", "buildNotes: release claim %s: %v", packTag(p), rerr)
			}
		}
	}()

	attempt := w.noteAttemptsFor(p.ID) + 1
	w.setStatus(p, model.PackNotes, fmt.Sprintf("сборка конспекта, попытка %d/%d", attempt, maxNoteAttempts))
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "buildNotes: save notes %s: %v", packTag(p), err)
	}
	w.hogs().Hold()
	buildStart := time.Now()
	err = notes.Build(ctx, w.Cfg, w.recRoot(), *p)
	w.hogs().Release()
	buildDur := time.Since(buildStart).Round(time.Second)
	if errors.Is(err, notes.ErrEmptyAudio) {
		// Не ошибка сборки: повторять нечего, выдуманный конспект не рассылаем.
		w.noteOK(p.ID)
		w.setStatus(p, model.PackEmpty, "записи нет или в ней нет речи: "+err.Error())
		p.Err = err.Error()
		if serr := w.Store.SavePack(p); serr != nil {
			logx.Errorf("bbb", "buildNotes: save empty %s: %v", packTag(p), serr)
		} else {
			released = true
		}
		logx.Warnf("bbb", "buildNotes: %s закрыт как empty за %s: %v", packTag(p), buildDur, err)
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("лекция «%s» %s: %v — конспекта не будет", p.Discipline, p.Date, err))
		return true
	}
	if err != nil {
		n := w.noteFail(p.ID)
		w.setStatus(p, model.PackError, fmt.Sprintf("сборка за %s упала (попытка %d): %v", buildDur, n, err))
		p.Err = err.Error()
		if serr := w.Store.SavePack(p); serr != nil {
			logx.Errorf("bbb", "buildNotes: save error %s: %v", packTag(p), serr)
		} else {
			released = true
		}
		attempt := ""
		if n < maxNoteAttempts {
			attempt = fmt.Sprintf(" (попытка %d/%d, следующая через %s)", n, maxNoteAttempts, noteRetryGap(n))
		} else {
			attempt = fmt.Sprintf(" (попыток больше не будет: %d)", n)
		}
		logx.Warnf("bbb", "buildNotes: %s не собрался за %s, попытка %d/%d: %v", packTag(p), buildDur, n, maxNoteAttempts, err)
		// Редкие повторы не шлём каждый раз: админ уже знает, что пак в работе.
		if n <= noteQuickRetries || n >= maxNoteAttempts {
			notify.Admin(ctx, w.Cfg, "конспект не собрался: "+archive.Rel(p.Discipline, p.Number)+" — "+err.Error()+attempt)
		}
		return false
	}
	w.noteOK(p.ID)
	w.setStatus(p, model.PackDone, fmt.Sprintf("конспект собран за %s", buildDur))
	p.Transcript = filepath.ToSlash(filepath.Join(p.Dir, "transcript.txt"))
	p.NotesPDF = filepath.ToSlash(filepath.Join(p.Dir, "notes.pdf"))
	p.Err = ""
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "buildNotes: save done %s: %v", packTag(p), err)
	} else {
		released = true
	}
	if err := w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventNotes, LessonID: p.LessonID,
		Message: archive.Rel(p.Discipline, p.Number),
	}); err != nil {
		logx.Debugf("bbb", "buildNotes: add event: %v", err)
	}
	logx.Infof("bbb", "notes: готово %s, PDF=%s, дальше announce и publish", packTag(p), p.NotesPDF)
	w.announceNotes(ctx, p)
	w.publishPack(ctx, p)
	return true
}

// githubAPI overrides the GitHub API base in tests; "" means the real one.
var githubAPI = ""

func (w *Worker) publisher() *publish.GitHub {
	if w == nil || w.Cfg == nil || w.Cfg.GitHubToken == "" {
		return nil
	}
	return &publish.GitHub{
		API:    githubAPI,
		Token:  w.Cfg.GitHubToken,
		Owner:  w.Cfg.GitHubOwner,
		Repo:   w.Cfg.GitHubRepo,
		Branch: w.Cfg.GitHubBranch,
	}
}

func (w *Worker) packFile(p *model.LecturePack, rel, fallback string) string {
	if strings.TrimSpace(rel) != "" {
		return filepath.Join(w.recRoot(), rel)
	}
	return filepath.Join(w.recRoot(), p.Dir, fallback)
}

func (w *Worker) publishPack(ctx context.Context, p *model.LecturePack) {
	pub := w.publisher()
	if pub == nil || p == nil || p.Status != model.PackDone {
		return
	}
	if strings.TrimSpace(p.Dir) == "" {
		logx.Warnf("bbb", "publish: %s пустой каталог пака — пропускаю выгрузку", packTag(p))
		return
	}
	pubStart := time.Now()
	trPath := w.packFile(p, p.Transcript, "transcript.txt")
	tr, err := os.ReadFile(trPath)
	if err != nil {
		logx.Errorf("bbb", "publish: %s transcript %s: %v", packTag(p), trPath, err)
		w.publishFail(p, "transcript: "+err.Error())
		return
	}
	pdfPath := w.packFile(p, p.NotesPDF, "notes.pdf")
	pdf, err := os.ReadFile(pdfPath)
	if err != nil {
		logx.Errorf("bbb", "publish: %s pdf %s: %v", packTag(p), pdfPath, err)
		w.publishFail(p, "pdf: "+err.Error())
		return
	}
	logx.Infof("bbb", "publish: старт %s transcript=%d байт pdf=%d байт → %s/%s@%s",
		packTag(p), len(tr), len(pdf), pub.Owner, pub.Repo, pub.Branch)
	dir := filepath.ToSlash(p.Dir)
	if _, err := pub.Upsert(ctx, dir+"/transcript.txt", tr, "add "+dir); err != nil {
		logx.Errorf("bbb", "publish: %s upsert transcript: %v", packTag(p), err)
		w.publishFail(p, err.Error())
		return
	}
	if _, err := pub.Upsert(ctx, dir+"/notes.pdf", pdf, "add "+dir); err != nil {
		logx.Errorf("bbb", "publish: %s upsert pdf: %v", packTag(p), err)
		w.publishFail(p, err.Error())
		return
	}
	now := time.Now().UTC()
	p.PublishStatus = "published"
	p.PublishedAt = &now
	p.Err = ""
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "publish: save %s: %v", packTag(p), err)
	}
	if rel := strings.TrimSpace(p.Audio); rel != "" {
		audio := filepath.Join(w.recRoot(), rel)
		if err := os.Remove(audio); err != nil {
			logx.Debugf("bbb", "publish: %s аудио не удалил %s: %v", packTag(p), audio, err)
		} else {
			logx.Debugf("bbb", "publish: %s аудио удалено %s", packTag(p), audio)
		}
	}
	logx.Infof("bbb", "publish: %s опубликован за %s (статус publish: published)", packTag(p), time.Since(pubStart).Round(time.Millisecond))
	notify.Admin(ctx, w.Cfg, "конспект выгружен: "+archive.Rel(p.Discipline, p.Number))
}

func (w *Worker) publishFail(p *model.LecturePack, reason string) {
	p.PublishStatus = "error"
	p.Err = reason
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "publishFail: save pack=%s: %v", p.Dir, err)
	}
	logx.Warnf("bbb", "publish: %s не выгрузился: %s — повторю через %s", packTag(p), reason, publishRetry)
}

func (w *Worker) maybePublish(ctx context.Context, now time.Time) {
	if w == nil || w.Store == nil || w.publisher() == nil {
		return
	}
	if !w.beginJob(false) {
		logx.Debugf("bbb", "maybePublish: busy, skip")
		return
	}
	w.jobWG.Add(1)
	go func() {
		defer w.jobWG.Done()
		defer w.endJob()
		w.publishSweep(ctx, now)
	}()
}

func (w *Worker) publishSweep(ctx context.Context, now time.Time) {
	packs, err := w.Store.ListPacks()
	if err != nil {
		logx.Warnf("bbb", "publish list: %v", err)
		return
	}
	logx.Debugf("bbb", "publishSweep: packs=%d now=%s", len(packs), now.Format(time.RFC3339))
	for i := range packs {
		p := packs[i]
		if p.Status != model.PackDone || p.PublishStatus == "published" {
			continue
		}
		if !p.UpdatedAt.IsZero() && now.Sub(p.UpdatedAt) < publishRetry {
			logx.Debugf("bbb", "publishSweep: %s пропуск: повтор выгрузки через %s", packTag(&p), publishRetry-now.Sub(p.UpdatedAt))
			continue
		}
		w.publishPack(ctx, &p)
	}
	for i := range packs {
		p := packs[i]
		if p.PublishedAt == nil || p.CleanedAt != nil {
			continue
		}
		if now.Sub(*p.PublishedAt) < publishedTTL {
			continue
		}
		w.cleanPack(&p)
	}
}

func (w *Worker) cleanPack(p *model.LecturePack) {
	if p == nil || strings.TrimSpace(p.Dir) == "" {
		return
	}
	logx.Debugf("bbb", "cleanPack: pack=%s", p.Dir)
	base := filepath.Join(w.recRoot(), p.Dir)
	for _, f := range []string{
		archive.TranscriptFile(base),
		archive.NotesMD(base),
		archive.NotesPDF(base),
	} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			logx.Debugf("bbb", "cleanPack: remove %s: %v", f, err)
		}
	}
	if err := os.RemoveAll(archive.SlidesDir(base)); err != nil {
		logx.Debugf("bbb", "cleanPack: remove slides %s: %v", base, err)
	}
	now := time.Now().UTC()
	p.CleanedAt = &now
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "cleanPack: save pack=%s: %v", p.Dir, err)
	}
	logx.Infof("bbb", "cleaned local %s", p.Dir)
}

func (w *Worker) announceNotes(ctx context.Context, p *model.LecturePack) {
	if p == nil {
		return
	}
	label := archive.Label(p.Discipline, p.Number)
	text := "Конспект готов: " + label + "\nСкачать PDF — кнопка ниже или «Конспекты» в панели."
	mk := notify.NotesButton(p.ID)
	sent := false
	if users, err := w.Store.ListUsers(); err == nil {
		for _, u := range w.noteRecipients(p, users) {
			notify.UserMarkup(ctx, w.Cfg, u.TelegramID, text, mk)
			sent = true
		}
	} else {
		logx.Debugf("bbb", "announceNotes: list users: %v", err)
	}
	if !sent {
		notify.UserMarkup(ctx, w.Cfg, w.adminID(), text, mk)
	}
	logx.Debugf("bbb", "announceNotes: pack=%d sent=%v", p.ID, sent)
}

// noteRecipients отбирает активных студентов, относящихся хотя бы к одной паре этого пака.
func (w *Worker) noteRecipients(p *model.LecturePack, users []model.User) []model.User {
	var active []model.User
	for _, u := range users {
		if u.Onboarded && u.Enabled {
			active = append(active, u)
		}
	}
	if len(active) == 0 || p == nil || w == nil || w.Store == nil {
		return nil
	}
	lessonIDs, err := w.Store.PackLessonIDs(p.ID)
	if err != nil {
		logx.Debugf("bbb", "noteRecipients: pack lesson ids pack=%d: %v", p.ID, err)
	}
	if len(lessonIDs) == 0 && p.LessonID > 0 {
		lessonIDs = []int64{p.LessonID}
	}

	var lessons []model.Lesson
	for _, lid := range lessonIDs {
		if lid <= 0 {
			continue
		}
		l, lerr := w.Store.LessonByID(lid)
		if lerr != nil {
			logx.Debugf("bbb", "noteRecipients: lesson %d: %v", lid, lerr)
			continue
		}
		if l != nil {
			lessons = append(lessons, *l)
		}
	}
	if len(lessons) > 0 {
		var out []model.User
		for _, u := range active {
			for i := range lessons {
				if lessons[i].For(u) {
					out = append(out, u)
					break
				}
			}
		}
		return out
	}

	// Для старых/синтетических паков без привязанных lesson_id делаем общую рассылку.
	if len(lessonIDs) == 0 {
		return active
	}

	// Уроков уже нет в таблице lessons — ищем участников по join_intents и events.
	allowed := w.participantsByHistory(lessonIDs, active)
	if len(allowed) == 0 {
		return nil
	}
	var out []model.User
	for _, u := range active {
		if allowed[u.TelegramID] {
			out = append(out, u)
		}
	}
	return out
}

func (w *Worker) participantsByHistory(lessonIDs []int64, active []model.User) map[int64]bool {
	if len(lessonIDs) == 0 {
		return nil
	}
	lidSet := make(map[int64]struct{}, len(lessonIDs))
	for _, lid := range lessonIDs {
		if lid > 0 {
			lidSet[lid] = struct{}{}
		}
	}
	if len(lidSet) == 0 {
		return nil
	}
	allowed := make(map[int64]bool)
	for _, u := range active {
		for lid := range lidSet {
			if in, err := w.Store.GetIntent(u.TelegramID, lid); err == nil && in != nil {
				allowed[u.TelegramID] = true
				break
			}
		}
	}
	if evs, err := w.Store.ListEvents(500); err == nil {
		for _, e := range evs {
			if e.TelegramID <= 0 {
				continue
			}
			if _, ok := lidSet[e.LessonID]; ok {
				allowed[e.TelegramID] = true
			}
		}
	}
	return allowed
}

func (w *Worker) adminID() int64 {
	if w != nil && w.Cfg != nil {
		return w.Cfg.AdminID
	}
	return 0
}

func (w *Worker) settingOn(key string) bool {
	if w == nil || w.Store == nil {
		return false
	}
	v, ok, err := w.Store.GetSetting(key)
	if err != nil {
		logx.Debugf("bbb", "settingOn: %s: %v", key, err)
		return false
	}
	on := ok && v == "1"
	logx.Debugf("bbb", "settingOn: %s on=%v", key, on)
	return on
}

func (w *Worker) setSetting(key, val string) {
	if w == nil || w.Store == nil {
		return
	}
	if err := w.Store.SetSetting(key, val); err != nil {
		logx.Errorf("bbb", "setSetting: %s=%s: %v", key, val, err)
	}
}

func (w *Worker) beginJob(needQuiet bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.busy {
		logx.Debugf("bbb", "beginJob: busy")
		return false
	}
	// Заходы идут в горутинах и попадают в sessions не сразу — учитываем и их,
	// и пары, которые этот тик решил посетить: LLM/STT на старте пары кладут VDS.
	if needQuiet && (len(w.sessions) > 0 || len(w.joining) > 0 || w.lectureNow) {
		logx.Debugf("bbb", "beginJob: quiet but sessions=%d joining=%d lecture=%v", len(w.sessions), len(w.joining), w.lectureNow)
		return false
	}
	w.busy = true
	logx.Debugf("bbb", "beginJob: acquired needQuiet=%v", needQuiet)
	return true
}

func (w *Worker) endJob() {
	w.mu.Lock()
	w.busy = false
	w.mu.Unlock()
	logx.Debugf("bbb", "endJob: released")
}
