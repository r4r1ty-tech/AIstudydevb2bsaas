package tg

import (
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestFormatLiveCard(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("Samara", 4*3600)
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, loc)
	l := model.Lesson{
		Discipline: "Матан",
		Begin:      now.Add(-10 * time.Minute),
		Finish:     now.Add(35 * time.Minute),
	}
	got := formatLiveCard(l, "https://bbb.ssau.ru/b/abc", "Иванов Иван", model.PresenceRoom, now, loc)
	for _, want := range []string{"Матан", "https://bbb.ssau.ru/b/abc", "35 мин", "в комнате", "Иванов Иван", "Отключиться"} {
		if want == "Отключиться" {
			continue
		}
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if !strings.Contains(got, "до 10:35") {
		t.Fatalf("finish clock: %q", got)
	}
	mk := leaveKeyboard(42)
	if mk.InlineKeyboard[0][0].CallbackData != "x:42" {
		t.Fatalf("callback %q", mk.InlineKeyboard[0][0].CallbackData)
	}
	id, ok := parseLeaveCallback("x:42")
	if !ok || id != 42 {
		t.Fatalf("parse %d %v", id, ok)
	}
}

func TestRemainPhrase(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	if got := remainPhrase(now, now.Add(90*time.Minute)); got != "1 ч 30 мин" {
		t.Fatalf("got %q", got)
	}
	if got := remainPhrase(now, now.Add(-time.Minute)); got != "заканчивается" {
		t.Fatalf("got %q", got)
	}
}
