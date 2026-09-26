package publish

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate("  short  ", 10); got != "short" {
		t.Errorf("short = %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Errorf("ascii = %q", got)
	}
	// 3 bytes into "лекция" is the middle of «е»: must back off to a rune edge.
	got := truncate("лекция", 3)
	if !utf8.ValidString(got) || got != "л…" {
		t.Errorf("cyrillic cut = %q valid=%v", got, utf8.ValidString(got))
	}
	long := strings.Repeat("я", 400)
	if got := truncate(long, 300); !utf8.ValidString(got) || len(got) > 300+len("…") {
		t.Errorf("long cut invalid: len=%d", len(got))
	}
}
