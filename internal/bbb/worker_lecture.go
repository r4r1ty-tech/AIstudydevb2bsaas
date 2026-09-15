package bbb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

	maxNoteAttempts = 3
	noteRetryEvery  = 10 * time.Minute
)

func (w *Worker) recRoot() string {
	if w != nil && w.Cfg != nil && w.Cfg.RecordingsDir != "" {
		return w.Cfg.RecordingsDir
	}
	return config.DefaultRecordings
}

func (w *Worker) pickRecorder(users []model.User, lesson model.Lesson, now time.Time) int64 {
	if w == nil || w.Store == nil {
		return 0
	}
	var id int64
	for _, u := range users {
		if !u.Active(now) || !lesson.MatchesSubgroup(u.Subgroup) {
			continue
		}
		intent, err := w.Store.GetIntent(u.TelegramID, lesson.ID)
		if err != nil || !WantsJoin(intent) {
			continue
		}
		if id == 0 || u.TelegramID < id {
			id = u.TelegramID
		}
	}
	return id
}

func (w *Worker) attachRecorder(ctx context.Context, sess Session, lesson model.Lesson, url string) Session {
	if w == nil || w.Store == nil || sess == nil {
		return sess
	}
	pack, err := w.Store.EnsurePack(lesson, url, w.recRoot())
	if err != nil || pack == nil {
		logx.Errorf("bbb", "pack: %v", err)
		return sess
	}
	abs := filepath.Join(w.recRoot(), pack.Dir)
	seg := capture.SegmentPath(abs, time.Now().UnixNano())
	rec, err := capture.Start(ctx, seg)
	if err != nil {
		logx.Errorf("bbb", "ffmpeg: %v", err)
		notify.Admin(ctx, w.Cfg, "запись «"+lesson.Discipline+"» не стартовала: "+err.Error())
		return sess
	}
	logx.Infof("bbb", "rec segment %s", filepath.Base(seg))
	pack.Status = model.PackRecording
	_ = w.Store.SavePack(pack)
	_ = w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventRecord, LessonID: lesson.ID,
		Message: archive.Rel(lesson.Discipline, pack.Number),
	})
	users := w.lectureUsers(lesson, time.Now())
	go w.startSpotter(ctx, rec, lesson, users)
	st := w.Store
	return &closeHook{Session: sess, fn: func() {
		_ = rec.Stop()
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
			return
		}
		p.Status = model.PackRecorded
		_ = st.SavePack(p)
	}}
}

func (w *Worker) maybeHarvest(ctx context.Context, now time.Time) {
	if w == nil || w.Store == nil {
		return
	}
	all, err := w.Store.ListLessons()
	if err != nil || !archive.ShouldHarvest(all, now, archive.HarvestGrace) {
		return
	}
	day := now.Format("2006-01-02")
	if w.settingOn(slidesDoneKey + day) {
		return
	}
	// Слайды не поднимают третий Chromium, пока идёт пара/тест.
	if !w.beginJob(true) {
		return
	}
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
	day := archive.NotesDay(now)
	if day == "" || w.settingOn(notesDoneKey+day) {
		return
	}
	if !w.beginJob(true) {
		return
	}
	w.jobWG.Add(1)
	go func() {
		defer w.jobWG.Done()
		defer w.endJob()
		if w.buildNotesDay(ctx, day) {
			w.setSetting(notesDoneKey+day, "1")
		}
	}()
}

func (w *Worker) harvestDay(ctx context.Context, day string) {
	packs, err := w.Store.PacksByDate(day)
	if err != nil {
		logx.Errorf("bbb", "packs %s: %v", day, err)
		return
	}
	for i := range packs {
		p := packs[i]
		if p.Status == model.PackRecorded {
			w.harvestSlides(ctx, &p)
		}
	}
}

func (w *Worker) buildNotesDay(ctx context.Context, day string) bool {
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
			if w.promoteStuckRecording(&p) {
				p.Status = model.PackRecorded
			} else {
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
			w.buildNotes(ctx, p.ID)
			continue
		}
		if !archive.ShouldNotePack(p.Status) {
			continue
		}
		w.buildNotes(ctx, p.ID)
	}
	return done
}

func (w *Worker) noteRetryReady(p *model.LecturePack, now time.Time) bool {
	if p == nil {
		return false
	}
	if w.noteAttemptsFor(p.ID) >= maxNoteAttempts {
		return false
	}
	return !p.UpdatedAt.After(now.Add(-noteRetryEvery))
}

func (w *Worker) noteAttemptsFor(id int64) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.noteAttempts[id]
}

func (w *Worker) noteFail(id int64) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.noteAttempts[id]++
	return w.noteAttempts[id]
}

func (w *Worker) noteOK(id int64) {
	w.mu.Lock()
	delete(w.noteAttempts, id)
	w.mu.Unlock()
}

func (w *Worker) promoteStuckRecording(p *model.LecturePack) bool {
	if p == nil || w.Store == nil {
		return false
	}
	audio := filepath.Join(w.recRoot(), p.Audio)
	st, err := os.Stat(audio)
	if err != nil || st.Size() < minRecordedBytes {
		size := int64(0)
		if st != nil {
			size = st.Size()
		}
		logx.Warnf("bbb", "pack %s: audio too small (%d b) — не считаю записанной", p.Dir, size)
		return false
	}
	p.Status = model.PackRecorded
	_ = w.Store.SavePack(p)
	return true
}

func (w *Worker) harvestSlides(ctx context.Context, p *model.LecturePack) {
	if p == nil || p.BBBURL == "" {
		return
	}
	if w.Cfg != nil && w.Cfg.BBBDryRun {
		return
	}
	fio := "архив"
	if users, err := w.Store.ListUsers(); err == nil {
		for _, u := range users {
			if u.FIO != "" {
				fio = u.FIO
				break
			}
		}
	}
	sess, err := w.Joiner.Join(ctx, JoinReq{URL: p.BBBURL, FIO: fio, Role: RoleSlides})
	if err != nil {
		logx.Warnf("bbb", "slides join %s/%d: %v", p.Discipline, p.Number, err)
		return
	}
	w.hogs().Hold()
	defer func() {
		_ = sess.Close()
		w.hogs().Release()
	}()
	n, err := sess.GrabSlides(ctx, archive.SlidesDir(filepath.Join(w.recRoot(), p.Dir)))
	if err != nil {
		logx.Warnf("bbb", "slides grab %s/%d: %v", p.Discipline, p.Number, err)
	}
	p.Status = model.PackSlides
	_ = w.Store.SavePack(p)
	_ = w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventSlides, LessonID: p.LessonID,
		Message: archive.Rel(p.Discipline, p.Number),
	})
	logx.Infof("bbb", "slides %s/%d n=%d", p.Discipline, p.Number, n)
	notify.Admin(ctx, w.Cfg, "слайды сняты: "+archive.Rel(p.Discipline, p.Number))
}

func (w *Worker) buildNotes(ctx context.Context, id int64) {
	p, err := w.Store.PackByID(id)
	if err != nil || p == nil {
		return
	}
	p.Status = model.PackNotes
	_ = w.Store.SavePack(p)
	w.hogs().Hold()
	err = notes.Build(ctx, w.Cfg, w.recRoot(), *p)
	w.hogs().Release()
	if err != nil {
		n := w.noteFail(p.ID)
		p.Status = model.PackError
		p.Err = err.Error()
		_ = w.Store.SavePack(p)
		attempt := ""
		if n < maxNoteAttempts {
			attempt = fmt.Sprintf(" (попытка %d/%d)", n, maxNoteAttempts)
		} else {
			attempt = fmt.Sprintf(" (попыток больше не будет: %d)", n)
		}
		notify.Admin(ctx, w.Cfg, "конспект не собрался: "+archive.Rel(p.Discipline, p.Number)+" — "+err.Error()+attempt)
		return
	}
	w.noteOK(p.ID)
	p.Status = model.PackDone
	p.Transcript = filepath.ToSlash(filepath.Join(p.Dir, "transcript.txt"))
	p.NotesPDF = filepath.ToSlash(filepath.Join(p.Dir, "notes.pdf"))
	p.Err = ""
	_ = w.Store.SavePack(p)
	_ = w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventNotes, LessonID: p.LessonID,
		Message: archive.Rel(p.Discipline, p.Number),
	})
	w.announceNotes(ctx, p)
	w.publishPack(ctx, p)
}

func (w *Worker) publisher() *publish.GitHub {
	if w == nil || w.Cfg == nil || w.Cfg.GitHubToken == "" {
		return nil
	}
	return &publish.GitHub{
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
	tr, err := os.ReadFile(w.packFile(p, p.Transcript, "transcript.txt"))
	if err != nil {
		w.publishFail(p, "transcript: "+err.Error())
		return
	}
	pdf, err := os.ReadFile(w.packFile(p, p.NotesPDF, "notes.pdf"))
	if err != nil {
		w.publishFail(p, "pdf: "+err.Error())
		return
	}
	dir := filepath.ToSlash(p.Dir)
	if _, err := pub.Upsert(ctx, dir+"/transcript.txt", tr, "add "+dir); err != nil {
		w.publishFail(p, err.Error())
		return
	}
	if _, err := pub.Upsert(ctx, dir+"/notes.pdf", pdf, "add "+dir); err != nil {
		w.publishFail(p, err.Error())
		return
	}
	now := time.Now().UTC()
	p.PublishStatus = "published"
	p.PublishedAt = &now
	p.Err = ""
	_ = w.Store.SavePack(p)
	_ = os.Remove(filepath.Join(w.recRoot(), p.Audio))
	logx.Infof("bbb", "published %s", dir)
	notify.Admin(ctx, w.Cfg, "конспект выгружен: "+archive.Rel(p.Discipline, p.Number))
}

func (w *Worker) publishFail(p *model.LecturePack, reason string) {
	p.PublishStatus = "error"
	p.Err = reason
	_ = w.Store.SavePack(p)
	logx.Warnf("bbb", "publish %s: %s", p.Dir, reason)
}

func (w *Worker) maybePublish(ctx context.Context, now time.Time) {
	if w == nil || w.Store == nil || w.publisher() == nil {
		return
	}
	if !w.beginJob(false) {
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
	base := filepath.Join(w.recRoot(), p.Dir)
	for _, f := range []string{
		archive.TranscriptFile(base),
		archive.NotesMD(base),
		archive.NotesPDF(base),
	} {
		_ = os.Remove(f)
	}
	_ = os.RemoveAll(archive.SlidesDir(base))
	now := time.Now().UTC()
	p.CleanedAt = &now
	_ = w.Store.SavePack(p)
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
	}
	if !sent {
		notify.UserMarkup(ctx, w.Cfg, w.adminID(), text, mk)
	}
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
	return err == nil && ok && v == "1"
}

func (w *Worker) setSetting(key, val string) {
	if w == nil || w.Store == nil {
		return
	}
	_ = w.Store.SetSetting(key, val)
}

func (w *Worker) beginJob(needQuiet bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.busy {
		return false
	}
	if needQuiet && len(w.sessions) > 0 {
		return false
	}
	w.busy = true
	return true
}

func (w *Worker) endJob() {
	w.mu.Lock()
	w.busy = false
	w.mu.Unlock()
}
