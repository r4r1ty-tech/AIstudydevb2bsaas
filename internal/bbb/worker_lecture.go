package bbb

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notes"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
)

const (
	slidesDoneKey = "slides_done:"
	notesDoneKey  = "notes_done:"
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
		log.Printf("bbb: pack: %v", err)
		return sess
	}
	abs := filepath.Join(w.recRoot(), pack.Dir)
	rec, err := capture.Start(ctx, archive.AudioFile(abs))
	if err != nil {
		log.Printf("bbb: ffmpeg: %v", err)
		notify.Admin(ctx, w.Cfg, "запись «"+lesson.Discipline+"» не стартовала: "+err.Error())
		return sess
	}
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
	if !w.beginJob(false) {
		return
	}
	go func() {
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
	go func() {
		defer w.endJob()
		if w.buildNotesDay(ctx, day) {
			w.setSetting(notesDoneKey+day, "1")
		}
	}()
}

func (w *Worker) harvestDay(ctx context.Context, day string) {
	packs, err := w.Store.PacksByDate(day)
	if err != nil {
		log.Printf("bbb: packs %s: %v", day, err)
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
		log.Printf("bbb: notes packs %s: %v", day, err)
		return false
	}
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
		if !archive.ShouldNotePack(p.Status) {
			continue
		}
		w.buildNotes(ctx, p.ID)
	}
	return done
}

func (w *Worker) promoteStuckRecording(p *model.LecturePack) bool {
	if p == nil || w.Store == nil {
		return false
	}
	audio := filepath.Join(w.recRoot(), p.Audio)
	st, err := os.Stat(audio)
	if err != nil || st.Size() < 2048 {
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
	w.hogs().Hold()
	defer w.hogs().Release()
	sess, err := w.Joiner.Join(ctx, JoinReq{URL: p.BBBURL, FIO: fio, Role: RoleSlides})
	if err != nil {
		log.Printf("bbb: slides join %s/%d: %v", p.Discipline, p.Number, err)
		return
	}
	n, err := sess.GrabSlides(ctx, archive.SlidesDir(filepath.Join(w.recRoot(), p.Dir)))
	_ = sess.Close()
	if err != nil {
		log.Printf("bbb: slides grab %s/%d: %v", p.Discipline, p.Number, err)
	}
	p.Status = model.PackSlides
	_ = w.Store.SavePack(p)
	_ = w.Store.AddEvent(model.Event{
		At: time.Now(), Type: model.EventSlides, LessonID: p.LessonID,
		Message: archive.Rel(p.Discipline, p.Number),
	})
	log.Printf("bbb: slides %s/%d n=%d", p.Discipline, p.Number, n)
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
		p.Status = model.PackError
		p.Err = err.Error()
		_ = w.Store.SavePack(p)
		notify.Admin(ctx, w.Cfg, "конспект не собрался: "+archive.Rel(p.Discipline, p.Number)+" — "+err.Error())
		return
	}
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
