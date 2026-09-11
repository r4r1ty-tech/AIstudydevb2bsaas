package rasp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestParseWeekFixture(t *testing.T) {
	html, err := os.ReadFile(filepath.Join("testdata", "week.html"))
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Europe/Samara")
	if err != nil {
		t.Fatal(err)
	}

	lessons, err := Parse(html, loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons) != 2 {
		t.Fatalf("got %d lessons, want 2: %+v", len(lessons), lessons)
	}

	var online, offline *model.Lesson
	for i := range lessons {
		l := &lessons[i]
		if l.Online {
			online = l
		} else {
			offline = l
		}
	}
	if online == nil {
		t.Fatal("expected an online lesson")
	}
	if online.Date != "2026-09-08" {
		t.Errorf("online date = %q", online.Date)
	}
	if online.Start != "08:00" || online.End != "09:35" {
		t.Errorf("online slot = %s–%s", online.Start, online.End)
	}
	if !strings.Contains(online.Discipline, "Математический") {
		t.Errorf("online discipline = %q", online.Discipline)
	}
	if !strings.Contains(online.Teacher, "Иванов") {
		t.Errorf("online teacher = %q", online.Teacher)
	}
	if !online.Online {
		t.Error("online flag false")
	}
	if online.Subgroup != 1 {
		t.Errorf("online subgroup = %d", online.Subgroup)
	}
	if online.Type != "Лекция" {
		t.Errorf("online type = %q", online.Type)
	}
	wantBegin := time.Date(2026, 9, 8, 8, 0, 0, 0, loc)
	wantFinish := time.Date(2026, 9, 8, 9, 35, 0, 0, loc)
	if !online.Begin.Equal(wantBegin) || online.Begin.Location() != loc {
		t.Errorf("online begin = %v loc=%v", online.Begin, online.Begin.Location())
	}
	if !online.Finish.Equal(wantFinish) {
		t.Errorf("online finish = %v", online.Finish)
	}

	if offline == nil {
		t.Fatal("expected an offline lesson")
	}
	if offline.Date != "2026-09-09" {
		t.Errorf("offline date = %q", offline.Date)
	}
	if offline.Start != "09:45" {
		t.Errorf("offline start = %q", offline.Start)
	}
	if offline.End != "11:20" {
		t.Errorf("offline end = %q", offline.End)
	}
	if !strings.Contains(offline.Discipline, "Программирование") {
		t.Errorf("offline discipline = %q", offline.Discipline)
	}
	if offline.Online {
		t.Error("offline marked online")
	}
	if offline.Subgroup != 0 {
		t.Errorf("offline subgroup = %d want 0", offline.Subgroup)
	}
	if offline.Teacher != "Петров П.П." {
		t.Errorf("offline teacher = %q", offline.Teacher)
	}
	wantOffBegin := time.Date(2026, 9, 9, 9, 45, 0, 0, loc)
	if !offline.Begin.Equal(wantOffBegin) || offline.Begin.Location() != loc {
		t.Errorf("offline begin = %v loc=%v", offline.Begin, offline.Begin.Location())
	}
}

func TestParseSkipsEmptyDiscipline(t *testing.T) {
	html := []byte(`<div class="schedule">
		<div class="schedule__item schedule__head"><div class="schedule__head-date">08.09.2026</div></div>
		<div class="schedule__time"><div class="schedule__time-item">08:00</div><div class="schedule__time-item">09:35</div></div>
		<div class="schedule__item"><div class="schedule__lesson">
			<div class="schedule__discipline">  </div>
			<div class="schedule__place">online</div>
		</div></div>
	</div>`)
	loc, err := time.LoadLocation("Europe/Samara")
	if err != nil {
		t.Fatal(err)
	}
	lessons, err := Parse(html, loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons) != 0 {
		t.Fatalf("got %d, want 0", len(lessons))
	}
}

func TestParseSeveralTeachers(t *testing.T) {
	html := []byte(`<div class="schedule">
		<div class="schedule__item schedule__head"><div class="schedule__head-date">08.09.2026</div></div>
		<div class="schedule__time"><div class="schedule__time-item">08:00</div><div class="schedule__time-item">09:35</div></div>
		<div class="schedule__item"><div class="schedule__lesson">
			<div class="schedule__discipline">Физика</div>
			<div class="schedule__teacher">
				<a href="/rasp?staffId=1">Иванов И.И.</a>
				<a href="/rasp?staffId=2">Сидоров С.С.</a>
			</div>
			<div class="schedule__place">ауд. 1</div>
			<div class="schedule__groups"><span></span></div>
		</div></div>
	</div>`)
	loc, err := time.LoadLocation("Europe/Samara")
	if err != nil {
		t.Fatal(err)
	}
	lessons, err := Parse(html, loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons) != 1 {
		t.Fatalf("got %d lessons", len(lessons))
	}
	if lessons[0].Teacher != "Иванов И.И., Сидоров С.С." {
		t.Errorf("teacher = %q", lessons[0].Teacher)
	}
	if lessons[0].Type != "unknown" {
		t.Errorf("type = %q", lessons[0].Type)
	}
}
