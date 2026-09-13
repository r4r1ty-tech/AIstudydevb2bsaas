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
