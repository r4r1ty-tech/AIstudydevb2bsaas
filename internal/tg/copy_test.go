package tg

import (
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestFormatT15Card(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	begin := time.Date(2026, 9, 12, 11, 45, 0, 0, loc)
	l := model.Lesson{
		Discipline: "Алгебра",
		Teacher:    "Сидоров",
		Start:      "11:30",
		End:        "13:05",
		Begin:      begin,
	}
	now := begin.Add(-12 * time.Minute)
	got := formatT15Card(l, now, loc, false)
	for _, want := range []string{"Через 12 мин пара", "Алгебра", "Сидоров", "11:30–13:05", "Зайти за тебя", askBBBLink} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(formatT15Card(l, now, loc, true), askBBBLink) {
		t.Fatal("hasLink should omit askBBBLink")
	}
}

func TestFormatJoinAndSkipAckKeepLesson(t *testing.T) {
	t.Parallel()
	l := &model.Lesson{Discipline: "Алгебра", Teacher: "Сидоров", Start: "11:30", End: "13:05"}
	yes := formatJoinAck(l, time.UTC, "Иванов Иван", true)
	if !strings.Contains(yes, "Алгебра") || !strings.Contains(yes, "Иванов Иван") || !strings.Contains(yes, "зайду") {
		t.Fatalf("join ack: %s", yes)
	}
	if strings.Contains(yes, askBBBLink) {
		t.Fatal("linked join should not ask url")
	}
	no := formatSkipAck(l, time.UTC)
	if !strings.Contains(no, "Алгебра") || !strings.Contains(no, "пропускаю") {
		t.Fatalf("skip ack: %s", no)
	}
}

func TestFormatTodaySplitsNowAndLater(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, loc)
	cur := model.Lesson{
		Discipline: "Алгебра", Start: "11:30", End: "13:05",
		Begin: now.Add(-30 * time.Minute), Finish: now.Add(65 * time.Minute),
	}
	next := model.Lesson{
		Discipline: "Физика", Start: "13:15", End: "14:50",
		Begin: now.Add(75 * time.Minute), Finish: now.Add(2 * time.Hour),
	}
	got := formatToday(now, loc, "Иванов Иван", []todayRow{
		{Lesson: cur, Presence: model.PresenceRoom},
		{Lesson: next, HasLink: true, Decision: model.JoinPending},
	})
	if !strings.Contains(got, "Сейчас") || !strings.Contains(got, "Дальше") {
		t.Fatalf("sections: %s", got)
	}
	if !strings.Contains(got, "в комнате") || !strings.Contains(got, "молчу — зайду") {
		t.Fatalf("notes: %s", got)
	}
	empty := formatToday(now, loc, "Иванов Иван", nil)
	if !strings.Contains(empty, "Онлайн-пар на сегодня больше нет") {
		t.Fatalf("empty: %s", empty)
	}
}

func TestUntilPhrase(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	if got := untilPhrase(now, now.Add(15*time.Minute)); got != "Через 15 мин пара" {
		t.Fatalf("15: %q", got)
	}
	if got := untilPhrase(now, now); got != "Сейчас пара" {
		t.Fatalf("now: %q", got)
	}
}

func TestParseOnboardCallback(t *testing.T) {
	t.Parallel()
	kind, n, ok := parseOnboardCallback("ob:sub:2")
	if !ok || kind != "sub" || n != 2 {
		t.Fatalf("sub2: %s %d %v", kind, n, ok)
	}
	kind, _, ok = parseOnboardCallback("ob:skipw")
	if !ok || kind != "skipw" {
		t.Fatalf("skip: %s %v", kind, ok)
	}
	if _, _, ok := parseOnboardCallback("j:y:1"); ok {
		t.Fatal("join callback is not onboard")
	}
	if !isMenuLabel(btnToday) || !isMenuLabel(btnSettings) || isMenuLabel("привет") {
		t.Fatal("menu labels")
	}
}

func TestFormatSettingsAndWordsAsk(t *testing.T) {
	t.Parallel()
	u := model.User{FIO: "Иванов Иван", Subgroup: 2, ExtraWords: []string{"лаба"}}
	got := formatSettings(u)
	for _, want := range []string{"Профиль", "Иванов Иван", "Подгруппа: 2", "лаба", "тест"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(formatOnboardDone(u), "зайду за 5 мин") {
		t.Fatalf("onboard: %s", formatOnboardDone(u))
	}
}

func TestParseSettingsCallback(t *testing.T) {
	t.Parallel()
	kind, n, ok := parseSettingsCallback("st:sub:2")
	if !ok || kind != "sub" || n != 2 {
		t.Fatalf("sub2: %s %d %v", kind, n, ok)
	}
	kind, _, ok = parseSettingsCallback("st:fio")
	if !ok || kind != "fio" {
		t.Fatalf("fio: %s %v", kind, ok)
	}
	kind, _, ok = parseSettingsCallback("st:rooms")
	if !ok || kind != "rooms" {
		t.Fatalf("rooms: %s %v", kind, ok)
	}
	kind, _, ok = parseSettingsCallback("st:back")
	if !ok || kind != "back" {
		t.Fatalf("back: %s %v", kind, ok)
	}
	if _, _, ok := parseSettingsCallback("ob:sub:1"); ok {
		t.Fatal("onboard is not settings")
	}
	mk := settingsKeyboard(1)
	if mk.InlineKeyboard[0][0].Text != "Изменить имя в журнале" {
		t.Fatalf("fio btn: %+v", mk)
	}
	if mk.InlineKeyboard[1][0].Text != "Подгруппа 1 ✓" || mk.InlineKeyboard[1][1].Text != "Подгруппа 2" {
		t.Fatalf("marks: %+v", mk)
	}
	if mk.InlineKeyboard[3][0].CallbackData != "st:rooms" {
		t.Fatalf("rooms: %+v", mk)
	}
}

func TestNotesListAndCallback(t *testing.T) {
	t.Parallel()
	ready := []model.LecturePack{{
		ID: 7, Discipline: "Матан", Number: 2, Date: "2026-09-13", Status: model.PackDone,
	}}
	pending := []model.LecturePack{{
		ID: 8, Discipline: "Физика", Number: 1, Date: "2026-09-13", Status: model.PackSlides,
	}}
	got := formatNotesList(ready, pending)
	for _, want := range []string{"Конспекты", "Матан · лекция 2", "Физика", "после полуночи"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(formatNotesList(nil, nil), "пока нет") {
		t.Fatal("empty")
	}
	id, ok := parseNotesCallback("nt:7")
	if !ok || id != 7 {
		t.Fatalf("cb %d %v", id, ok)
	}
	if _, ok := parseNotesCallback("j:y:1"); ok {
		t.Fatal("join is not notes")
	}
	kb := notesKeyboard([]int64{7}, []string{"Матан · 2"})
	if kb.InlineKeyboard[0][0].CallbackData != "nt:7" {
		t.Fatalf("%+v", kb)
	}
}

func TestJSRegexpNotUsedInT15Buttons(t *testing.T) {
	t.Parallel()
	mk := t15Keyboard(42)
	if mk.InlineKeyboard[0][0].Text != "Зайти за меня" || mk.InlineKeyboard[0][1].Text != "Не сегодня" {
		t.Fatalf("buttons: %+v", mk)
	}
	if mk.InlineKeyboard[0][0].CallbackData != "j:y:42" {
		t.Fatalf("cb: %s", mk.InlineKeyboard[0][0].CallbackData)
	}
	kb := mainKeyboard()
	if !kb.IsPersistent || kb.InputFieldPlaceholder != inputHint {
		t.Fatalf("main kb: %+v", kb)
	}
	if kb.Keyboard[0][0].Text != btnToday || kb.Keyboard[0][1].Text != btnNotes {
		t.Fatalf("row1: %+v", kb.Keyboard)
	}
	if kb.Keyboard[1][0].Text != btnSettings {
		t.Fatalf("profile button: %+v", kb.Keyboard)
	}
}

func TestFormatTestCardAndKeyboard(t *testing.T) {
	t.Parallel()
	empty := formatTestCard(model.TestJoin{})
	if !strings.Contains(empty, "Ссылки нет") || !strings.Contains(empty, "Отдельная комната") {
		t.Fatalf("empty:\n%s", empty)
	}
	live := model.TestJoin{
		URL:    "https://bbb.ssau.ru/b/x",
		Want:   model.TestWantListen,
		Status: model.TestRoom,
		Mode:   model.TestWantListen,
		Name:   "тест",
	}
	got := formatTestCard(live)
	for _, want := range []string{"Сейчас тест", "bbb.ssau.ru/b/x", "со звуком", "тест", "в комнате"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	idleKB := testKeyboard(model.TestJoin{})
	if idleKB.InlineKeyboard[0][0].CallbackData != "tx:dummy" || idleKB.InlineKeyboard[0][1].CallbackData != "tx:listen" {
		t.Fatalf("idle kb: %+v", idleKB)
	}
	inKB := testKeyboard(live)
	if inKB.InlineKeyboard[0][0].CallbackData != "tx:leave" {
		t.Fatalf("in kb: %+v", inKB)
	}
	if !testAlready(live, model.TestWantListen) || testAlready(live, model.TestWantDummy) {
		t.Fatal("testAlready")
	}
}
