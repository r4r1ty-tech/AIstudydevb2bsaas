package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

func Slug(discipline string) string {
	s := strings.TrimSpace(discipline)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			return '-'
		default:
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}
	}, s)
	s = strings.Trim(s, " .-")
	if s == "" {
		return "предмет"
	}
	return s
}

func Rel(discipline string, n int) string {
	if n < 1 {
		n = 1
	}
	return filepath.Join(Slug(discipline), fmt.Sprintf("лекция-%d", n))
}

func Abs(root, discipline string, n int) string {
	if strings.TrimSpace(root) == "" {
		root = "recordings"
	}
	return filepath.Join(root, Rel(discipline, n))
}

func AudioFile(dir string) string      { return filepath.Join(dir, "audio.ogg") }
func TranscriptFile(dir string) string { return filepath.Join(dir, "transcript.txt") }
func SlidesDir(dir string) string      { return filepath.Join(dir, "slides") }
func NotesMD(dir string) string        { return filepath.Join(dir, "notes.md") }
func NotesPDF(dir string) string       { return filepath.Join(dir, "notes.pdf") }

func Label(discipline string, n int) string {
	if n < 1 {
		n = 1
	}
	d := strings.TrimSpace(discipline)
	if d == "" {
		d = "лекция"
	}
	return fmt.Sprintf("%s · лекция %d", d, n)
}

func PDFFileName(discipline string, n int) string {
	return Slug(discipline) + fmt.Sprintf("-лекция-%d.pdf", n)
}

func UnderRoot(root, rel string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "recordings"
	}
	abs, err := filepath.Abs(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	sep := string(os.PathSeparator)
	if abs != base && !strings.HasPrefix(abs, base+sep) {
		return "", fmt.Errorf("path escapes recordings")
	}
	return abs, nil
}
