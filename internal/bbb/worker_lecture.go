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

// attachRecorder вешает запись на вкладку; recording=false — запись не стартовала,
// вкладка остаётся просто присутствием.
func (w *Worker) attachRecorder(ctx context.Context, sess Session, lesson model.Lesson, url string) (Session, bool) {
	if w == nil || w.Store == nil || sess == nil {
		return sess, false
	}
	logx.Debugf("bbb", "attachRecorder: lesson=%d discipline=%q url=%s", lesson.ID, lesson.Discipline, redactURL(url))
	pack, err := w.Store.EnsurePack(lesson, url, w.recRoot())
	if err != nil || pack == nil {
		logx.Errorf("bbb", "pack: %v", err)
		return sess, false
	}
	abs := filepath.Join(w.recRoot(), pack.Dir)
	seg := capture.SegmentPath(abs, time.Now().UnixNano())
	first, err := startRecorder(ctx, seg)
	if err != nil {
		logx.Errorf("bbb", "ffmpeg: %v", err)
		notify.Admin(ctx, w.Cfg, "запись «"+lesson.Discipline+"» не стартовала: "+err.Error())
		return sess, false
	}
	rec := &restartRec{
		cur: first,
		start: func() (recorder, error) {
			next := capture.SegmentPath(abs, time.Now().UnixNano())
			logx.Infof("bbb", "rec segment %s (restart)", filepath.Base(next))
			return startRecorder(ctx, next)
		},
		onDied: func(err error, restarted bool) {
			if restarted {
				logx.Warnf("bbb", "ffmpeg %s умер: %v — перезапустил запись", pack.Dir, err)
				go notify.Admin(ctx, w.Cfg, fmt.Sprintf("запись «%s»: ffmpeg упал (%v), перезапустил — в записи может быть короткая дырка", lesson.Discipline, err))
				return
			}
			logx.Errorf("bbb", "ffmpeg %s умер: %v — запись остановлена", pack.Dir, err)
			go notify.Admin(ctx, w.Cfg, fmt.Sprintf("запись «%s» остановилась: %v", lesson.Discipline, err))
		},
	}
	logx.Infof("bbb", "rec segment %s", filepath.Base(seg))
	logx.Infof("bbb", "recording started pack=%d dir=%s", pack.Number, pack.Dir)
	pack.Status = model.PackRecording
	if err := w.Store.SavePack(pack); err != nil {
		logx.Errorf("bbb", "attachRecorder: save recording pack=%s: %v", pack.Dir, err)
	}
	if err := w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventRecord, LessonID: lesson.ID,
		Message: archive.Rel(lesson.Discipline, pack.Number),
	}); err != nil {
		logx.Debugf("bbb", "attachRecorder: add event: %v", err)
	}
	// Словарь — по всей подгруппе: Vosk не перезапускается, а «Зайти» могут
	// нажать уже после старта записи. Адресатов onWake считает на момент хита.
	go w.startSpotter(ctx, rec, lesson, w.subgroupUsers(lesson, time.Now()))
	st := w.Store
	return &closeHook{Session: sess, fn: func() {
		logx.Infof("bbb", "recording stopped pack=%d dir=%s", pack.Number, pack.Dir)
		if err := rec.Stop(); err != nil {
			logx.Warnf("bbb", "stop rec %s: %v", pack.Dir, err)
		}
		n, merr := capture.MergeSegments(context.Background(), abs, archive.AudioFile(abs))
		if merr != nil {
			logx.Warnf("bbb", "merge %s: %v", pack.Dir, merr)
		} else {
			logx.Infof("bbb", "audio merged %s segs=%d", pack.Dir, n)
		}
		if merr != nil {
			return
		}
		p, err := st.PackByID(pack.ID)
		if err != nil || p == nil {
			logx.Debugf("bbb", "attachRecorder: pack %d reload: %v", pack.ID, err)
			return
		}
		p.Status = model.PackRecorded
		if err := st.SavePack(p); err != nil {
			logx.Errorf("bbb", "attachRecorder: save recorded pack=%s: %v", p.Dir, err)
		}
	}}, true
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
		w.setSetting(slidesDoneKey+day, "1")
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
		for _, day := range days {
			if ctx.Err() != nil {
				return
			}
			if w.buildNotesDay(ctx, day) {
				w.setSetting(notesDoneKey+day, "1")
			}
		}
	}()
}

// openNoteDays: вчерашний день и более ранние дни с паками, у которых
// notes_done ещё не стоит — конспект, упавший вчера, догоняется сегодня.
func (w *Worker) openNoteDays(now time.Time) []string {
	last := archive.NotesDay(now)
	if last == "" {
		return nil
	}
	var days []string
	if packs, err := w.Store.ListPacks(); err != nil {
		logx.Errorf("bbb", "openNoteDays: list packs: %v", err)
	} else {
		first := now.AddDate(0, 0, -noteCatchUpDays).Format("2006-01-02")
		seen := map[string]bool{}
		for _, p := range packs {
			if p.Date < first || p.Date >= last || seen[p.Date] {
				continue
			}
			seen[p.Date] = true
			if !w.settingOn(notesDoneKey + p.Date) {
				days = append(days, p.Date)
			}
		}
		sort.Strings(days)
	}
	if !w.settingOn(notesDoneKey + last) {
		days = append(days, last)
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
			w.harvestSlides(ctx, &p)
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
		if p.Status == model.PackError {
			// Битый конспект пересобираем с паузами, но не бесконечно.
			if !w.noteRetryReady(&p, now) {
				if w.noteAttemptsFor(p.ID) < maxNoteAttempts {
					done = false
				}
				continue
			}
			if !w.buildNotes(ctx, p.ID) && w.noteAttemptsFor(p.ID) < maxNoteAttempts {
				done = false
			}
			continue
		}
		if !archive.ShouldNotePack(p.Status) {
			continue
		}
		// Упавший пак держит день открытым, пока есть попытки: иначе
		// notes_done ставится сразу и ретраев с паузой не бывает.
		if !w.buildNotes(ctx, p.ID) && w.noteAttemptsFor(p.ID) < maxNoteAttempts {
			done = false
		}
	}
	logx.Debugf("bbb", "buildNotesDay: day=%s done=%v", day, done)
	return done
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
	if p == nil || w.Store == nil {
		return stuckWait
	}
	w.mu.Lock()
	recKey := w.recorder[p.LessonID]
	_, live := w.sessions[recKey]
	w.mu.Unlock()
	if recKey != "" && live {
		return stuckWait
	}
	dir := filepath.Join(w.recRoot(), p.Dir)
	audio := filepath.Join(w.recRoot(), p.Audio)
	if p.Audio == "" {
		audio = archive.AudioFile(dir)
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
		logx.Warnf("bbb", "pack %s: записи нет (%d b) — закрываю как empty", p.Dir, size)
		p.Status = model.PackEmpty
		p.Err = fmt.Sprintf("записи нет (%d байт)", size)
		if err := w.Store.SavePack(p); err != nil {
			logx.Errorf("bbb", "promoteStuckRecording: save empty pack=%s: %v", p.Dir, err)
			return stuckWait
		}
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("лекция «%s» %s: записи нет — конспекта не будет", p.Discipline, p.Date))
		return stuckEmpty
	}
	p.Status = model.PackRecorded
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "promoteStuckRecording: save pack=%s: %v", p.Dir, err)
	}
	logx.Infof("bbb", "promoted stuck recording pack=%s", p.Dir)
	return stuckRecorded
}

func (w *Worker) harvestSlides(ctx context.Context, p *model.LecturePack) {
	if p == nil || p.BBBURL == "" {
		return
	}
	if w.Cfg != nil && w.Cfg.BBBDryRun {
		logx.Debugf("bbb", "harvestSlides: dry run, skip pack=%s", p.Dir)
		return
	}
	logx.Infof("bbb", "harvestSlides: join %s/%d url=%s", p.Discipline, p.Number, redactURL(p.BBBURL))
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
	sess, err := w.Joiner.Join(ctx, JoinReq{URL: p.BBBURL, FIO: fio, Role: RoleSlides})
	if err != nil {
		logx.Warnf("bbb", "slides join %s/%d: %v", p.Discipline, p.Number, err)
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
		logx.Warnf("bbb", "slides %s/%d: не в комнате (err=%v) — пропускаю", p.Discipline, p.Number, err)
		return
	}
	n, err := sess.GrabSlides(ctx, archive.SlidesDir(filepath.Join(w.recRoot(), p.Dir)))
	if err != nil {
		logx.Warnf("bbb", "slides grab %s/%d: %v", p.Discipline, p.Number, err)
	}
	if n == 0 {
		logx.Warnf("bbb", "slides %s/%d: презентации нет — статус не меняю", p.Discipline, p.Number)
		return
	}
	p.Status = model.PackSlides
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "harvestSlides: save pack=%s: %v", p.Dir, err)
	}
	if err := w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventSlides, LessonID: p.LessonID,
		Message: archive.Rel(p.Discipline, p.Number),
	}); err != nil {
		logx.Debugf("bbb", "harvestSlides: add event: %v", err)
	}
	logx.Infof("bbb", "slides %s/%d n=%d", p.Discipline, p.Number, n)
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
	p.Status = model.PackNotes
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "buildNotes: save notes pack=%s: %v", p.Dir, err)
	}
	w.hogs().Hold()
	err = notes.Build(ctx, w.Cfg, w.recRoot(), *p)
	w.hogs().Release()
	if errors.Is(err, notes.ErrEmptyAudio) {
		// Не ошибка сборки: повторять нечего, выдуманный конспект не рассылаем.
		w.noteOK(p.ID)
		p.Status = model.PackEmpty
		p.Err = err.Error()
		if serr := w.Store.SavePack(p); serr != nil {
			logx.Errorf("bbb", "buildNotes: save empty pack=%s: %v", p.Dir, serr)
		}
		logx.Warnf("bbb", "buildNotes: pack=%s закрыт как empty: %v", p.Dir, err)
		notify.Admin(ctx, w.Cfg, fmt.Sprintf("лекция «%s» %s: %v — конспекта не будет", p.Discipline, p.Date, err))
		return true
	}
	if err != nil {
		n := w.noteFail(p.ID)
		p.Status = model.PackError
		p.Err = err.Error()
		if serr := w.Store.SavePack(p); serr != nil {
			logx.Errorf("bbb", "buildNotes: save error pack=%s: %v", p.Dir, serr)
		}
		attempt := ""
		if n < maxNoteAttempts {
			attempt = fmt.Sprintf(" (попытка %d/%d, следующая через %s)", n, maxNoteAttempts, noteRetryGap(n))
		} else {
			attempt = fmt.Sprintf(" (попыток больше не будет: %d)", n)
		}
		logx.Warnf("bbb", "buildNotes: pack=%d failed attempt=%d: %v", p.ID, n, err)
		// Редкие повторы не шлём каждый раз: админ уже знает, что пак в работе.
		if n <= noteQuickRetries || n >= maxNoteAttempts {
			notify.Admin(ctx, w.Cfg, "конспект не собрался: "+archive.Rel(p.Discipline, p.Number)+" — "+err.Error()+attempt)
		}
		return false
	}
	w.noteOK(p.ID)
	p.Status = model.PackDone
	p.Transcript = filepath.ToSlash(filepath.Join(p.Dir, "transcript.txt"))
	p.NotesPDF = filepath.ToSlash(filepath.Join(p.Dir, "notes.pdf"))
	p.Err = ""
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "buildNotes: save done pack=%s: %v", p.Dir, err)
	}
	if err := w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventNotes, LessonID: p.LessonID,
		Message: archive.Rel(p.Discipline, p.Number),
	}); err != nil {
		logx.Debugf("bbb", "buildNotes: add event: %v", err)
	}
	logx.Infof("bbb", "notes done pack=%s", p.Dir)
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
	logx.Debugf("bbb", "publishPack: pack=%s", p.Dir)
	tr, err := os.ReadFile(w.packFile(p, p.Transcript, "transcript.txt"))
	if err != nil {
		logx.Errorf("bbb", "publishPack: transcript %s: %v", p.Dir, err)
		w.publishFail(p, "transcript: "+err.Error())
		return
	}
	pdf, err := os.ReadFile(w.packFile(p, p.NotesPDF, "notes.pdf"))
	if err != nil {
		logx.Errorf("bbb", "publishPack: pdf %s: %v", p.Dir, err)
		w.publishFail(p, "pdf: "+err.Error())
		return
	}
	dir := filepath.ToSlash(p.Dir)
	if _, err := pub.Upsert(ctx, dir+"/transcript.txt", tr, "add "+dir); err != nil {
		logx.Errorf("bbb", "publishPack: upsert transcript %s: %v", dir, err)
		w.publishFail(p, err.Error())
		return
	}
	if _, err := pub.Upsert(ctx, dir+"/notes.pdf", pdf, "add "+dir); err != nil {
		logx.Errorf("bbb", "publishPack: upsert pdf %s: %v", dir, err)
		w.publishFail(p, err.Error())
		return
	}
	now := time.Now().UTC()
	p.PublishStatus = "published"
	p.PublishedAt = &now
	p.Err = ""
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "publishPack: save pack=%s: %v", p.Dir, err)
	}
	if err := os.Remove(filepath.Join(w.recRoot(), p.Audio)); err != nil {
		logx.Debugf("bbb", "publishPack: remove audio %s: %v", p.Audio, err)
	}
	logx.Infof("bbb", "published %s", dir)
	notify.Admin(ctx, w.Cfg, "конспект выгружен: "+archive.Rel(p.Discipline, p.Number))
}

func (w *Worker) publishFail(p *model.LecturePack, reason string) {
	p.PublishStatus = "error"
	p.Err = reason
	if err := w.Store.SavePack(p); err != nil {
		logx.Errorf("bbb", "publishFail: save pack=%s: %v", p.Dir, err)
	}
	logx.Warnf("bbb", "publish %s: %s", p.Dir, reason)
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
	text := "Конспект готов: " + label + "\nСкачать PDF — кнопка ниже или «Конспекты» в меню."
	mk := notify.NotesButton(p.ID)
	sent := false
	if users, err := w.Store.ListUsers(); err == nil {
		for _, u := range users {
			if !u.Onboarded || !u.Enabled {
				continue
			}
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
