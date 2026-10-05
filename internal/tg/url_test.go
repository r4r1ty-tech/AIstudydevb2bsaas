package tg

import (
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestExtractBBBURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"https://bbb.ssau.ru/b/abc-def", "https://bbb.ssau.ru/b/abc-def"},
		{"http://bbb.ssau.ru/b/xyz", "http://bbb.ssau.ru/b/xyz"},
		{"bbb.ssau.ru/b/no-scheme", "https://bbb.ssau.ru/b/no-scheme"},
		{"Кинь https://bbb.ssau.ru/b/room1 пожалуйста", "https://bbb.ssau.ru/b/room1"},
		{"см. <https://bbb.ssau.ru/b/x>.", "https://bbb.ssau.ru/b/x"},
		{"нет ссылки", ""},
		{"ssau.ru/rasp", ""},
	}
	for _, tc := range cases {
		if got := extractBBBURL(tc.in); got != tc.want {
			t.Errorf("extractBBBURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBBBKeyUsage(t *testing.T) {
	t.Parallel()
	got := model.BBBKey(531023229, " Матан ", "Иванов")
	want := "531023229|Матан|Иванов"
	if got != want {
		t.Fatalf("BBBKey = %q, want %q", got, want)
	}
	if extractBBBURL("https://bbb.ssau.ru/b/x") == "" {
		t.Fatal("expected url extract to match host used with BBBKey rooms")
	}
}

func TestParseJoinCallback(t *testing.T) {
	t.Parallel()
	yes, id, ok := parseJoinCallback("j:y:42")
	if !ok || !yes || id != 42 {
		t.Fatalf("j:y:42 -> yes=%v id=%d ok=%v", yes, id, ok)
	}
	yes, id, ok = parseJoinCallback("j:n:7")
	if !ok || yes || id != 7 {
		t.Fatalf("j:n:7 -> yes=%v id=%d ok=%v", yes, id, ok)
	}
	if _, _, ok := parseJoinCallback("j:x:1"); ok {
		t.Fatal("expected reject")
	}
	if got := joinCallbackData(true, 99); got != "j:y:99" {
		t.Fatalf("joinCallbackData yes = %q", got)
	}
	if got := joinCallbackData(false, 99); got != "j:n:99" {
		t.Fatalf("joinCallbackData no = %q", got)
	}
}

func TestParseSubgroup(t *testing.T) {
	t.Parallel()
	// Пустой текст (кнопка меню, /settings) — не выбор подгруппы.
	if _, ok := parseSubgroup(""); ok {
		t.Fatal("empty must not pick subgroup 1")
	}
	n, ok := parseSubgroup("2")
	if !ok || n != 2 {
		t.Fatalf("2 -> %d %v", n, ok)
	}
	if _, ok := parseSubgroup("нет"); ok {
		t.Fatal("expected reject")
	}
}

func TestCommandPayloadAndWakeReply(t *testing.T) {
	t.Parallel()
	if got := commandPayload("/words лаба, зачёт"); got != "лаба, зачёт" {
		t.Fatalf("payload: %q", got)
	}
	if got := commandPayload("/words@bot"); got != "" {
		t.Fatalf("empty payload: %q", got)
	}
	if !isClearWords("очистить") || isClearWords("лаба") {
		t.Fatal("clear")
	}
	u := model.User{FIO: "Иванов Иван", ExtraWords: []string{"лаба"}}
	got := formatWakeReply(u)
	if !strings.Contains(got, "лаба") || !strings.Contains(got, "тест") || !strings.Contains(got, "иванов") {
		t.Fatalf("reply: %s", got)
	}
}

func TestPickLessonForBBB(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	a := model.Lesson{ID: 1, Discipline: "A", Teacher: "T1", Begin: now.Add(20 * time.Minute), Online: true, Subgroup: 0}
	b := model.Lesson{ID: 2, Discipline: "B", Teacher: "T2", Begin: now.Add(40 * time.Minute), Online: true, Subgroup: 0}
	c := model.Lesson{ID: 3, Discipline: "C", Teacher: "T3", Begin: now.Add(-time.Hour), Online: true, Subgroup: 0}

	has := func(id int64) bool { return id == 1 }
	got := pickLessonForBBB(now, 1, []model.Lesson{a, b, c}, has, nil)
	if got == nil || got.ID != 2 {
		t.Fatalf("want nearest without BBB id=2, got %#v", got)
	}

	allLinked := func(int64) bool { return true }
	intents := map[int64]time.Time{1: now.Add(-time.Minute), 3: now}
	got = pickLessonForBBB(now, 1, []model.Lesson{a, b, c}, allLinked, intents)
	if got == nil || got.ID != 3 {
		t.Fatalf("want last intent id=3, got %#v", got)
	}
}

func TestPickLessonForBBBPreferCurrent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	live := model.Lesson{ID: 10, Discipline: "Сейчас", Begin: now.Add(-30 * time.Minute), Finish: now.Add(time.Hour), Online: true}
	next := model.Lesson{ID: 11, Discipline: "Дальше", Begin: now.Add(20 * time.Minute), Finish: now.Add(2 * time.Hour), Online: true}

	none := func(int64) bool { return false }
	got := pickLessonForBBB(now, 1, []model.Lesson{next, live}, none, nil)
	if got == nil || got.ID != 10 {
		t.Fatalf("want current lesson id=10, got %#v", got)
	}
}

func TestNormalizeFIO(t *testing.T) {
	t.Parallel()
	if got, ok := normalizeFIO("  Иванов\n Иван   Иванович "); !ok || got != "Иванов Иван Иванович" {
		t.Fatalf("got %q %v", got, ok)
	}
	for _, bad := range []string{"привет", "", strings.Repeat("Оченьдлиннаяфамилия ", 3) + strings.Repeat("я", 60), "а б в г д"} {
		if _, ok := normalizeFIO(bad); ok {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
