package model

import (
	"testing"
	"time"
)

func TestIsLecture(t *testing.T) {
	t.Parallel()
	if !IsLecture("Лекция") || !IsLecture("лекция") || !IsLecture("Lecture") {
		t.Fatal("lecture types")
	}
	if IsLecture("Практика") || IsLecture("Лабораторная") || IsLecture("unknown") || IsLecture("") {
		t.Fatal("non-lecture")
	}
}

func TestParseBBBKey(t *testing.T) {
	t.Parallel()
	key := BBBKey(531023229, "Матан", "Иванов")
	d, teach := ParseBBBKey(key)
	if d != "Матан" || teach != "Иванов" {
		t.Fatalf("%q %q", d, teach)
	}
	if BBBLabel(key) != "Матан · Иванов" {
		t.Fatalf("label %q", BBBLabel(key))
	}
	lk := BBBLessonKey(582)
	if lk != "lesson:582" {
		t.Fatalf("lesson key %q", lk)
	}
	id, ok := ParseBBBLessonID(lk)
	if !ok || id != 582 {
		t.Fatalf("parse lesson key %d %v", id, ok)
	}
	if BBBLabel(lk) != "пара 582" {
		t.Fatalf("lesson label %q", BBBLabel(lk))
	}
	if _, ok := ParseBBBLessonID("531023229|Матан|Иванов"); ok {
		t.Fatal("discipline key is not a lesson key")
	}
}

func TestLessonIdentity(t *testing.T) {
	t.Parallel()
	a := Lesson{Date: "2026-09-14", Start: "09:45", Discipline: "С", Teacher: "К", Place: "online", Subgroup: 0, Online: true}
	b := a
	b.Type = "Лекция"
	if a.Identity() != b.Identity() {
		t.Fatal("type should not change identity")
	}
	b.Place = "ауд. 1"
	if a.Identity() == b.Identity() {
		t.Fatal("place should change identity")
	}
}
func TestResolveBBB(t *testing.T) {
	l := Lesson{ID: 7, Discipline: "Матан", Teacher: "Иванов", Type: "Лекция"}
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	links := []BBBLink{
		{Key: BBBKey(1, "Матан", "Иванов"), URL: "panel-old", UpdatedAt: t0},
		{Key: BBBKey(2, "Матан", "Иванов"), URL: "panel-new", UpdatedAt: t0.Add(time.Hour)},
		{Key: BBBKey(1, "Матан", "Петров"), URL: "other", UpdatedAt: t0.Add(2 * time.Hour)},
	}
	if u, own := ResolveBBB(l, links); u != "panel-new" || own {
		t.Fatalf("panel = %q %v", u, own)
	}
	links = append(links, BBBLink{Key: BBBRoomKey("Матан", "Иванов", "Практика"), URL: "prac"})
	if u, _ := ResolveBBB(l, links); u != "panel-new" {
		t.Fatalf("practice room must not apply to lecture: %q", u)
	}
	links = append(links, BBBLink{Key: BBBRoomKey("Матан", "Иванов", "Лекция"), URL: "room"})
	if u, own := ResolveBBB(l, links); u != "room" || own {
		t.Fatalf("room = %q %v", u, own)
	}
	links = append(links, BBBLink{Key: BBBLessonKey(7), URL: ""})
	if u, own := ResolveBBB(l, links); u != "" || !own {
		t.Fatalf("cleared own link must win: %q %v", u, own)
	}
	if got := BBBLabel(BBBRoomKey("Матан", "Иванов", "Лекция")); got != "Матан · Иванов · Лекция" {
		t.Fatalf("label = %q", got)
	}
}
