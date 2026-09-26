package model

import (
	"testing"
	"time"
)

func TestUserActive(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Hour)
	cases := []struct {
		name string
		u    User
		want bool
	}{
		{"ok", User{Enabled: true, Onboarded: true, FIO: "Иванов"}, true},
		{"disabled", User{Enabled: false, Onboarded: true, FIO: "Иванов"}, false},
		{"paused until later", User{Enabled: true, Onboarded: true, FIO: "Иванов", DisabledUntil: &later}, false},
		{"pause over", User{Enabled: true, Onboarded: true, FIO: "Иванов", DisabledUntil: &earlier}, true},
		{"not onboarded", User{Enabled: true, FIO: "Иванов"}, false},
		{"blank fio", User{Enabled: true, Onboarded: true, FIO: "  "}, false},
	}
	for _, c := range cases {
		if got := c.u.Active(now); got != c.want {
			t.Errorf("%s: Active = %v", c.name, got)
		}
	}
}

func TestWakeWordHelpers(t *testing.T) {
	t.Parallel()
	if got := FormatWakeWords([]string{"Лаба", "зачёт", "лаба"}); got != "лаба, зачёт" {
		t.Errorf("FormatWakeWords = %q", got)
	}
	if got := FormatWakeWords(nil); got != "" {
		t.Errorf("empty = %q", got)
	}
	got := MergeWakeWords([]string{"лаба"}, []string{"зачёт", "ЛАБА"})
	if len(got) != 2 || got[0] != "лаба" || got[1] != "зачёт" {
		t.Errorf("MergeWakeWords = %v", got)
	}
}

func TestLessonSubgroupAndSlot(t *testing.T) {
	t.Parallel()
	all := Lesson{Subgroup: 0}
	two := Lesson{Subgroup: 2, Start: "08:00", End: "09:35"}
	if !all.MatchesSubgroup(1) || !all.MatchesSubgroup(2) {
		t.Error("whole-group lesson matches everyone")
	}
	if two.MatchesSubgroup(1) || !two.MatchesSubgroup(2) {
		t.Error("subgroup lesson matches only its subgroup")
	}
	if got := two.SlotLabel(); got != "08:00–09:35" {
		t.Errorf("SlotLabel = %q", got)
	}
}

func TestTestJoinGuestName(t *testing.T) {
	t.Parallel()
	if got := (TestJoin{}).GuestName(); got != TestGuestName {
		t.Errorf("default = %q", got)
	}
	if got := (TestJoin{Name: "  Робот "}).GuestName(); got != "Робот" {
		t.Errorf("trimmed = %q", got)
	}
}
