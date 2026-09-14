package tg

import "testing"

func TestParseTestNamePayload(t *testing.T) {
	t.Parallel()
	if _, ok := parseTestNamePayload(""); ok {
		t.Fatal("empty")
	}
	if _, ok := parseTestNamePayload("https://bbb.ssau.ru/b/x"); ok {
		t.Fatal("url is not a name")
	}
	got, ok := parseTestNamePayload("имя Иванов")
	if !ok || got != "Иванов" {
		t.Fatalf("got %q %v", got, ok)
	}
	got, ok = parseTestNamePayload("Петров Пётр")
	if !ok || got != "Петров Пётр" {
		t.Fatalf("got %q %v", got, ok)
	}
}

func TestNormalizeTestName(t *testing.T) {
	t.Parallel()
	if normalizeTestName("-") != "тест" {
		t.Fatal("dash")
	}
	if normalizeTestName("  Алекс  ") != "Алекс" {
		t.Fatal("trim")
	}
}
