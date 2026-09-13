package archive

import (
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestShouldHarvest(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("Samara", 4*3600)
	d := "2026-09-15"
	a := model.Lesson{
		Date: d, Type: "Лекция", Online: true,
		Finish: time.Date(2026, 9, 15, 11, 20, 0, 0, loc),
	}
	b := model.Lesson{
		Date: d, Type: "Лекция", Online: true,
		Finish: time.Date(2026, 9, 15, 16, 40, 0, 0, loc),
	}
	prac := model.Lesson{
		Date: d, Type: "Практика", Online: true,
		Finish: time.Date(2026, 9, 15, 18, 0, 0, 0, loc),
	}
	lessons := []model.Lesson{a, b, prac}

	mid := time.Date(2026, 9, 15, 12, 0, 0, 0, loc)
	if ShouldHarvest(lessons, mid, 3*time.Minute) {
		t.Fatal("second lecture still ahead")
	}
	justEnd := b.Finish.Add(time.Minute)
	if ShouldHarvest(lessons, justEnd, 3*time.Minute) {
		t.Fatal("grace not elapsed")
	}
	after := b.Finish.Add(4 * time.Minute)
	if !ShouldHarvest(lessons, after, 3*time.Minute) {
		t.Fatal("should harvest after last lecture + grace")
	}
	if ShouldHarvest(nil, after, 3*time.Minute) {
		t.Fatal("no lectures")
	}
}

func TestNotesDayIsYesterday(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("Samara", 4*3600)
	justAfter := time.Date(2026, 9, 14, 0, 5, 0, 0, loc)
	if got := NotesDay(justAfter); got != "2026-09-13" {
		t.Fatalf("midnight: %s", got)
	}
	evening := time.Date(2026, 9, 13, 23, 50, 0, 0, loc)
	if got := NotesDay(evening); got != "2026-09-12" {
		t.Fatalf("before midnight still previous day: %s", got)
	}
}

func TestShouldNotePack(t *testing.T) {
	t.Parallel()
	if ShouldNotePack(model.PackDone) || ShouldNotePack(model.PackRecording) || ShouldNotePack(model.PackError) {
		t.Fatal("skip done/recording/error")
	}
	if !ShouldNotePack(model.PackSlides) || !ShouldNotePack(model.PackRecorded) || !ShouldNotePack(model.PackNotes) {
		t.Fatal("need notes for slides/recorded/in-progress")
	}
}
