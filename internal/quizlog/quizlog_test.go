package quizlog

import (
	"strings"
	"testing"
	"time"
)

func TestWriteListRead(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		r := &Run{At: base.Add(time.Duration(i) * time.Minute), TelegramID: 1, CMID: int64(i), Status: Solved,
			Pages: []Page{{Page: 0, Raw: strings.Repeat("x", MaxRaw+10)}}}
		if err := Write(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	list, err := List(dir, 2)
	if err != nil || len(list) != 2 || list[0].CMID != 2 || list[0].Pages[0].Raw != "" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	full, err := Read(dir, list[0].Name)
	if err != nil || !strings.HasSuffix(full.Pages[0].Raw, "(обрезано)") {
		t.Fatalf("read: %v", err)
	}
	for _, bad := range []string{"../x.json", "a/b.json", "x.txt", ""} {
		if _, err := Read(dir, bad); err == nil {
			t.Fatalf("%q прочитан", bad)
		}
	}
	if l, err := List(t.TempDir()+"/нет", 5); err != nil || len(l) != 0 {
		t.Fatalf("пустая папка: %v %v", l, err)
	}
}
