package archive

import (
	"path/filepath"
	"testing"
)

func TestSlugAndRel(t *testing.T) {
	t.Parallel()
	if got := Slug("  Математический   анализ  "); got != "Математический анализ" {
		t.Fatalf("slug spaces = %q", got)
	}
	if got := Slug("Сети/протоколы"); got != "Сети-протоколы" {
		t.Fatalf("slug slash = %q", got)
	}
	if got := Slug("   "); got != "предмет" {
		t.Fatalf("slug empty = %q", got)
	}
	rel := Rel("Математический анализ", 1)
	want := filepath.Join("Математический анализ", "лекция-1")
	if rel != want {
		t.Fatalf("rel = %q want %q", rel, want)
	}
	if Rel("x", 0) != filepath.Join("x", "лекция-1") {
		t.Fatalf("n<1: %q", Rel("x", 0))
	}
	if Label("Матан", 3) != "Матан · лекция 3" {
		t.Fatalf("label: %q", Label("Матан", 3))
	}
	if PDFFileName("Матан", 2) != "Матан-лекция-2.pdf" {
		t.Fatalf("pdf name: %q", PDFFileName("Матан", 2))
	}
}
