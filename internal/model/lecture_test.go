package model

import "testing"

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
