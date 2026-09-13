package bbb

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/capture"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notes"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notify"
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
	w.mu.Lock()
	if w.harvested == nil {
		w.harvested = make(map[string]struct{})
	}
	if _, ok := w.harvested[day]; ok || w.harvesting {
		w.mu.Unlock()
		return
	}
	w.harvesting = true
	w.mu.Unlock()

	go func() {
		defer func() {
			w.mu.Lock()
			w.harvesting = false
			w.harvested[day] = struct{}{}
			w.mu.Unlock()
		}()
		w.finishDay(ctx, day)
	}()
}

func (w *Worker) finishDay(ctx context.Context, day string) {
	packs, err := w.Store.PacksByDate(day)
	if err != nil {
		log.Printf("bbb: packs %s: %v", day, err)
		return
	}
	for i := range packs {
		p := packs[i]
		switch p.Status {
		case model.PackRecording, model.PackDone, model.PackError:
			continue
		case model.PackRecorded:
			w.harvestSlides(ctx, &p)
		}
		w.buildNotes(ctx, p.ID)
	}
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
	if err := notes.Build(ctx, w.Cfg, w.recRoot(), *p); err != nil {
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
	notify.Admin(ctx, w.Cfg, "конспект готов: "+archive.Rel(p.Discipline, p.Number))
}
