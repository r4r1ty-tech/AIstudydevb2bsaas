package panelpass

import (
	"strings"
	"testing"
	"time"
)

var key = []byte("0123456789abcdef0123456789abcdef")

func TestIssueAndWho(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 10, 0, 0, time.UTC)
	ids := []int64{111, 222, 333}
	pw, until := Issue(key, 222, now)
	if len(pw) != codeLen+1 || pw[4] != '-' {
		t.Fatalf("формат пароля: %q", pw)
	}
	if want := time.Date(2026, 11, 14, 14, 0, 0, 0, time.UTC); !until.Equal(want) {
		t.Fatalf("until = %v, want %v", until, want)
	}
	for _, in := range []string{pw, strings.ToUpper(pw), " " + strings.ReplaceAll(pw, "-", "") + " "} {
		if id, ok := Who(key, ids, in, now); !ok || id != 222 {
			t.Fatalf("Who(%q) = %d,%v", in, id, ok)
		}
	}
	// в следующем часу ещё принимается, через два — уже нет
	if id, ok := Who(key, ids, pw, now.Add(time.Hour)); !ok || id != 222 {
		t.Fatalf("через час: %d,%v", id, ok)
	}
	if _, ok := Who(key, ids, pw, now.Add(2*time.Hour)); ok {
		t.Fatal("через два часа пароль ещё принят")
	}
}

func TestWhoRejects(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	pw, _ := Issue(key, 999, now)
	if _, ok := Who(key, []int64{111, 222}, pw, now); ok {
		t.Fatal("пароль не из вайтлиста принят")
	}
	if _, ok := Who([]byte("другой ключ другой ключ другой к"), []int64{999}, pw, now); ok {
		t.Fatal("пароль с другим ключом принят")
	}
	if _, ok := Who(nil, []int64{999}, pw, now); ok {
		t.Fatal("без ключа принят")
	}
	if _, ok := Who(key, []int64{999}, "", now); ok {
		t.Fatal("пустой принят")
	}
}

func TestDistinctPerUserAndHour(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	a, _ := Issue(key, 1, now)
	b, _ := Issue(key, 2, now)
	c, _ := Issue(key, 1, now.Add(time.Hour))
	if a == b || a == c {
		t.Fatalf("пароли совпали: %s %s %s", a, b, c)
	}
}
