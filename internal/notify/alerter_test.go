package notify

import (
	"testing"
)

func TestDetectTestKeywords(t *testing.T) {
	text := "Ребята, открываем Moodle, там выложен тест по физике."
	matched := DetectTestKeywords(text)

	if len(matched) < 2 {
		t.Errorf("expected at least 2 keywords, got %v", matched)
	}

	foundMoodle := false
	foundTest := false
	for _, m := range matched {
		if m == "moodle" {
			foundMoodle = true
		}
		if m == "тест" {
			foundTest = true
		}
	}

	if !foundMoodle || !foundTest {
		t.Errorf("expected moodle and тест in matches, got %v", matched)
	}
}

func TestDetectTestKeywordsNegative(t *testing.T) {
	text := "Здравствуйте, сегодня разберем теорему Пифагора."
	matched := DetectTestKeywords(text)
	if len(matched) != 0 {
		t.Errorf("expected 0 matches, got %v", matched)
	}
}
