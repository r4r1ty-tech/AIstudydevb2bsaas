package model

import "testing"

func TestParseAndWakeList(t *testing.T) {
	got := ParseWakeWords("Тест,  МУДЛ; лаба\nтест")
	if len(got) != 3 || got[0] != "тест" || got[1] != "мудл" || got[2] != "лаба" {
		t.Fatalf("%v", got)
	}
	if !SkipWakeWords("-") || !SkipWakeWords("нет") || !SkipWakeWords("clear") || SkipWakeWords("лаба") {
		t.Fatal("skip")
	}
	u := User{FIO: "Иванов Иван", ExtraWords: []string{"лаба", "тест"}}
	list := u.WakeList()
	want := map[string]bool{"тест": true, "контрольная": true, "мудл": true, "moodle": true, "иванов": true, "лаба": true}
	if len(list) != len(want) {
		t.Fatalf("%v", list)
	}
	for _, w := range list {
		if !want[w] {
			t.Fatalf("unexpected %q in %v", w, list)
		}
	}
}
