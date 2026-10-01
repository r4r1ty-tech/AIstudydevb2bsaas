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

func TestChooseRecorderKeepsLiveAndPicksMinID(t *testing.T) {
	w := NewWorker(&config.Config{BBBDryRun: true}, nil, time.UTC)
	now := time.Date(2026, 9, 15, 8, 10, 0, 0, time.UTC)
	lesson := model.Lesson{ID: 7, Type: "Лекция"}
	here := []pending{
		{u: model.User{TelegramID: 20}, lesson: lesson, key: sessionKey(20, 7)},
		{u: model.User{TelegramID: 10}, lesson: lesson, key: sessionKey(10, 7)},
	}
	if i := w.chooseRecorder(lesson, here, now); i != 1 {
		t.Fatalf("min id: got %d", i)
	}
	// Живой рекордер 20 остаётся, второй запис не начинаем.
	w.sessions[sessionKey(20, 7)] = stubSess{room: true}
	w.recorder[7] = sessionKey(20, 7)
	if i := w.chooseRecorder(lesson, here, now); i != 0 {
		t.Fatalf("live recorder: got %d", i)
	}
	// Рекордер выпал (его нет среди тех, кто должен быть в комнате) — передаём 10.
	if i := w.chooseRecorder(lesson, here[1:], now); i != 0 {
		t.Fatalf("handover: got %d", i)
	}
	// Запись только что не стартовала — пауза, не дёргаем вкладки каждые 15 с.
	delete(w.recorder, 7)
	w.recFailAt[7] = now
	if i := w.chooseRecorder(lesson, here, now.Add(time.Minute)); i != -1 {
		t.Fatalf("rec retry gap: got %d", i)
	}
	if i := w.chooseRecorder(lesson, here, now.Add(3*time.Minute)); i != 1 {
		t.Fatalf("after gap: got %d", i)
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
