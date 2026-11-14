package model

import (
	"testing"
	"time"
)

func TestLessonForAndLKLink(t *testing.T) {
	me := User{TelegramID: 7, Subgroup: 2}
	cases := []struct {
		l    Lesson
		want bool
	}{
		{Lesson{}, true},
		{Lesson{Subgroup: 2}, true},
		{Lesson{Subgroup: 1}, false},
		{Lesson{Owner: 7}, true},
		{Lesson{Owner: 8}, false},
	}
	for i, c := range cases {
		if got := c.l.For(me); got != c.want {
			t.Errorf("case %d: %v want %v", i, got, c.want)
		}
	}
	a := Lesson{Date: "2026-11-13", Start: "09:45", Discipline: "X"}
	b := a
	b.Owner = 7
	if a.Identity() == b.Identity() {
		t.Fatal("IET lesson must not collide with group lesson")
	}

	l := Lesson{ID: 5, Discipline: "X", Teacher: "T", Type: "Лекция", BBBURL: "https://bbb.ssau.ru/b/lk"}
	room := BBBLink{Key: BBBRoomKey("X", "T", "Лекция"), URL: "https://bbb.ssau.ru/b/room"}
	if u, own := ResolveBBB(l, []BBBLink{room}); u != "https://bbb.ssau.ru/b/lk" || !own {
		t.Fatalf("lk link must beat room: %q %v", u, own)
	}
	manual := BBBLink{Key: BBBLessonKey(5), URL: "https://bbb.ssau.ru/b/manual"}
	if u, _ := ResolveBBB(l, []BBBLink{room, manual}); u != "https://bbb.ssau.ru/b/manual" {
		t.Fatalf("manual lesson link must win: %q", u)
	}
}

func TestMoodleItemClosed(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	closedTodo := "Тест сейчас не открыт для попыток"
	cases := []struct {
		name string
		item MoodleItem
		want bool
	}{
		{"не сдан, срок прошёл", MoodleItem{Kind: MoodleQuiz, Status: MoodleTodo, Due: &past}, true},
		{"не сдан, срок впереди", MoodleItem{Kind: MoodleQuiz, Status: MoodleTodo, Due: &future}, false},
		{"не сдан, без срока, не открыт", MoodleItem{Kind: MoodleQuiz, Status: MoodleTodo, Todo: closedTodo}, true},
		{"не сдан, срок впереди, не открыт", MoodleItem{Kind: MoodleQuiz, Status: MoodleTodo, Due: &future, Todo: closedTodo}, true},
		{"не сдан, без срока, открыт", MoodleItem{Kind: MoodleQuiz, Status: MoodleTodo, Todo: "Разрешено попыток: 2"}, false},
		{"ещё не открылся: опенинг впереди", MoodleItem{Kind: MoodleQuiz, Status: MoodleTodo, Opens: &future, Todo: closedTodo}, false},
		{"черновик, срок прошёл", MoodleItem{Kind: MoodleQuiz, Status: MoodleDraft, Due: &past}, true},
		{"сдан, срок прошёл", MoodleItem{Kind: MoodleQuiz, Status: MoodleSubmitted, Due: &past}, false},
		{"сдан, не открыт", MoodleItem{Kind: MoodleQuiz, Status: MoodleDone, Todo: closedTodo}, false},
		{"задание, срок прошёл", MoodleItem{Kind: MoodleAssign, Status: MoodleTodo, Due: &past}, false},
		{"задание, не открыт", MoodleItem{Kind: MoodleAssign, Status: MoodleTodo, Todo: closedTodo}, false},
	}
	for _, c := range cases {
		if got := c.item.Closed(now); got != c.want {
			t.Errorf("%s: Closed() = %v, want %v", c.name, got, c.want)
		}
	}
}
