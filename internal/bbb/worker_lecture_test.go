package bbb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func TestPickRecorderMinID(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 9, 15, 8, 10, 0, 0, time.UTC)
	lesson := model.Lesson{ID: 7, Subgroup: 0, Type: "Лекция"}
	users := []model.User{
		{TelegramID: 20, FIO: "Бб", Onboarded: true, Enabled: true, Subgroup: 1},
		{TelegramID: 10, FIO: "Аа", Onboarded: true, Enabled: true, Subgroup: 1},
	}
	w := &Worker{Store: st}
	if id := w.pickRecorder(users, lesson, now); id != 10 {
		t.Fatalf("got %d", id)
	}
}

func TestBuildNotesDaySkipsLiveRecording(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	lesson := model.Lesson{ID: 1, Date: "2026-09-13", Discipline: "Матан", Type: "Лекция"}
	p, err := st.EnsurePack(lesson, "https://bbb.ssau.ru/b/x", root)
	if err != nil {
		t.Fatal(err)
	}
	p.Status = model.PackRecording
	p.Audio = filepath.ToSlash(filepath.Join(p.Dir, "audio.ogg"))
	if err := st.SavePack(p); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: st, Cfg: &config.Config{RecordingsDir: root}}
	if w.buildNotesDay(context.Background(), "2026-09-13") {
		t.Fatal("live recording must wait")
	}
}

func TestBeginJobQuiet(t *testing.T) {
	w := NewWorker(&config.Config{BBBDryRun: true}, nil, time.UTC)
	w.sessions["1:1"] = drySession{}
	if w.beginJob(true) {
		t.Fatal("notes should wait while tabs live")
	}
	if !w.beginJob(false) {
		t.Fatal("slides can start")
	}
	w.endJob()
	w.sessions = map[string]Session{}
	if !w.beginJob(true) {
		t.Fatal("notes after empty")
	}
}
