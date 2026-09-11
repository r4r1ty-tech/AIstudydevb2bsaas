package rasp

import (
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func sampleLessons() []model.Lesson {
	return []model.Lesson{
		{Date: "2026-09-08", Start: "08:00", Discipline: "Математический анализ", Teacher: "Иванов И.И.", Place: "online", Subgroup: 1, Online: true},
		{Date: "2026-09-09", Start: "09:45", Discipline: "Программирование", Teacher: "Петров П.П.", Place: "ауд. 314", Subgroup: 0, Online: false},
	}
}

func TestDiffIdentical(t *testing.T) {
	a := sampleLessons()
	b := sampleLessons()
	if got := Diff(a, b); got != "" {
		t.Fatalf("identical diff = %q", got)
	}
	if got := Diff(a, []model.Lesson{a[1], a[0]}); got != "" {
		t.Fatalf("same set different order diff = %q", got)
	}
	if got := Diff(nil, nil); got != "" {
		t.Fatalf("empty diff = %q", got)
	}
}

func TestDiffAddRemove(t *testing.T) {
	old := sampleLessons()
	added := model.Lesson{Date: "2026-09-10", Start: "08:00", Discipline: "Алгебра", Teacher: "Сидоров", Place: "online", Subgroup: 0, Online: true}
	newLessons := []model.Lesson{old[0], added}

	got := Diff(old, newLessons)
	if got == "" {
		t.Fatal("expected non-empty diff")
	}
	if !hasDiffLine(got, '+') || !hasDiffLine(got, '-') {
		t.Fatalf("want + and - lines, got %q", got)
	}
	if !strings.Contains(got, "Алгебра") {
		t.Fatalf("missing added lesson: %q", got)
	}
	if !strings.Contains(got, "Программирование") {
		t.Fatalf("missing removed lesson: %q", got)
	}

	onlyAdd := Diff(nil, []model.Lesson{added})
	if !hasDiffLine(onlyAdd, '+') || hasDiffLine(onlyAdd, '-') {
		t.Fatalf("add-only: %q", onlyAdd)
	}
	onlyRemove := Diff([]model.Lesson{added}, nil)
	if !hasDiffLine(onlyRemove, '-') || hasDiffLine(onlyRemove, '+') {
		t.Fatalf("remove-only: %q", onlyRemove)
	}
}

func hasDiffLine(diff string, mark byte) bool {
	for _, line := range strings.Split(diff, "\n") {
		if len(line) >= 2 && line[0] == mark && line[1] == ' ' {
			return true
		}
	}
	return false
}
