package model

import (
	"testing"
)

func TestMeetingKey(t *testing.T) {
	t.Parallel()

	lesson := Lesson{
		ID:         42,
		Date:       "2026-09-21",
		Start:      "08:00",
		Discipline: "Математический анализ",
		BBBURL:     "https://bbb.ssau.ru/b/room-math",
	}

	expectedKey := "bbb:https://bbb.ssau.ru/b/room-math|2026-09-21|08:00"

	// 1. Использование lesson.BBBURL при пустом rawURL
	if got := MeetingKey(lesson, ""); got != expectedKey {
		t.Fatalf("ожидался ключ %q, получено %q", expectedKey, got)
	}

	// 2. Явная передача сырого URL, совпадающего с каноническим
	if got := MeetingKey(lesson, "https://bbb.ssau.ru/b/room-math"); got != expectedKey {
		t.Fatalf("ожидался ключ %q, получено %q", expectedKey, got)
	}

	// 3. Нормализация регистра схемы и хоста
	if got := MeetingKey(lesson, "HTTPS://BBB.SSAU.RU/b/room-math"); got != expectedKey {
		t.Fatalf("регистр схемы/хоста: ожидался ключ %q, получено %q", expectedKey, got)
	}

	// 4. Отсечение query-параметров и якоря
	if got := MeetingKey(lesson, "https://bbb.ssau.ru/b/room-math?meetingID=xyz123&user=student#presenter"); got != expectedKey {
		t.Fatalf("query/fragment: ожидался ключ %q, получено %q", expectedKey, got)
	}

	// 5. Отсечение хвостового слэша и пробелов
	if got := MeetingKey(lesson, "  https://bbb.ssau.ru/b/room-math/  "); got != expectedKey {
		t.Fatalf("хвостовой слэш/пробелы: ожидался ключ %q, получено %q", expectedKey, got)
	}

	// 6. Сложная комбинация всех факторов
	comboURL := "  HTTPs://BBB.Ssau.RU/b/room-math/?meetingID=foo#bar  "
	if got := MeetingKey(lesson, comboURL); got != expectedKey {
		t.Fatalf("комбинация: ожидался ключ %q, получено %q", expectedKey, got)
	}

	// 7. Пустой URL возвращает пустую строку
	emptyLesson := Lesson{Date: "2026-09-21", Start: "08:00"}
	if got := MeetingKey(emptyLesson, ""); got != "" {
		t.Fatalf("пустой URL должен давать пустой ключ, получено %q", got)
	}
	if got := MeetingKey(emptyLesson, "   "); got != "" {
		t.Fatalf("пробельный URL должен давать пустой ключ, получено %q", got)
	}

	// 8. Невалидный URL
	if got := MeetingKey(lesson, "http://:::"); got != "" {
		t.Fatalf("невалидный URL должен давать пустой ключ, получено %q", got)
	}

	// 9. Разные даты и время
	lessonDiffDate := lesson
	lessonDiffDate.Date = "2026-09-22"
	if got := MeetingKey(lessonDiffDate, ""); got == expectedKey {
		t.Fatalf("ключи для разных дат не должны совпадать: %q", got)
	}

	lessonDiffStart := lesson
	lessonDiffStart.Start = "09:45"
	if got := MeetingKey(lessonDiffStart, ""); got == expectedKey {
		t.Fatalf("ключи для разного времени старта не должны совпадать: %q", got)
	}

	// 10. Пустая дата или пустое время начала возвращают пустую строку
	lessonNoDate := lesson
	lessonNoDate.Date = ""
	if got := MeetingKey(lessonNoDate, ""); got != "" {
		t.Fatalf("пустая дата должна давать пустой ключ, получено %q", got)
	}
	lessonNoDate.Date = "   "
	if got := MeetingKey(lessonNoDate, ""); got != "" {
		t.Fatalf("пробельная дата должна давать пустой ключ, получено %q", got)
	}

	lessonNoStart := lesson
	lessonNoStart.Start = ""
	if got := MeetingKey(lessonNoStart, ""); got != "" {
		t.Fatalf("пустое время начала должно давать пустой ключ, получено %q", got)
	}
	lessonNoStart.Start = "   "
	if got := MeetingKey(lessonNoStart, ""); got != "" {
		t.Fatalf("пробельное время начала должно давать пустой ключ, получено %q", got)
	}
}

func TestLessonIdentityIncludesTypeAndTeacher(t *testing.T) {
	t.Parallel()

	base := Lesson{
		Date:       "2026-09-21",
		Start:      "09:45",
		Discipline: "Алгоритмы",
		Type:       "Лекция",
		Teacher:    "Иванов И.И.",
		Place:      "online",
		Subgroup:   0,
		Online:     true,
	}

	// Нормализация пробелов в Discipline, Type и Teacher.
	withSpaces := base
	withSpaces.Discipline = "  Алгоритмы  "
	withSpaces.Type = " Лекция "
	withSpaces.Teacher = "\tИванов И.И.\n"
	if base.Identity() != withSpaces.Identity() {
		t.Fatalf("пробелы не должны менять Identity: %q vs %q", base.Identity(), withSpaces.Identity())
	}

	// Смена аудитории, online или ссылки BBB не меняет Identity.
	moved := base
	moved.Place = "311 - 5"
	moved.Online = false
	moved.BBBURL = "https://bbb.ssau.ru/b/room"
	if base.Identity() != moved.Identity() {
		t.Fatalf("смена аудитории/online/BBBURL не должна менять Identity: %q vs %q", base.Identity(), moved.Identity())
	}

	// Смена типа пары (лекция vs практика) меняет Identity.
	practice := base
	practice.Type = "Практика"
	if base.Identity() == practice.Identity() {
		t.Fatalf("смена типа пары должна менять Identity: %q", base.Identity())
	}

	// Смена преподавателя меняет Identity.
	otherTeacher := base
	otherTeacher.Teacher = "Петров П.П."
	if base.Identity() == otherTeacher.Identity() {
		t.Fatalf("смена преподавателя должна менять Identity: %q", base.Identity())
	}
}
