package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

func Slug(discipline string) string {
	logx.Debugf("archive", "Slug: enter %q", discipline)
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
		logx.Debugf("archive", "Slug: empty -> предмет")
		return "предмет"
	}
	logx.Debugf("archive", "Slug: exit %q", s)
	return s
}

func Rel(discipline string, n int) string {
	logx.Debugf("archive", "Rel: enter discipline=%q n=%d", discipline, n)
	if n < 1 {
		logx.Debugf("archive", "Rel: n<1 -> 1")
		n = 1
	}
	out := filepath.Join(Slug(discipline), fmt.Sprintf("лекция-%d", n))
	logx.Debugf("archive", "Rel: exit %s", out)
	return out
}

func Abs(root, discipline string, n int) string {
	logx.Debugf("archive", "Abs: enter root=%q discipline=%q n=%d", root, discipline, n)
	if strings.TrimSpace(root) == "" {
		logx.Debugf("archive", "Abs: empty root -> recordings")
		root = "recordings"
	}
	out := filepath.Join(root, Rel(discipline, n))
	logx.Debugf("archive", "Abs: exit %s", out)
	return out
}

func AudioFile(dir string) string {
	p := filepath.Join(dir, "audio.ogg")
	logx.Debugf("archive", "AudioFile: %s", p)
	return p
}

func TranscriptFile(dir string) string {
	p := filepath.Join(dir, "transcript.txt")
	logx.Debugf("archive", "TranscriptFile: %s", p)
	return p
}

func SlidesDir(dir string) string {
	p := filepath.Join(dir, "slides")
	logx.Debugf("archive", "SlidesDir: %s", p)
	return p
}

func NotesMD(dir string) string {
	p := filepath.Join(dir, "notes.md")
	logx.Debugf("archive", "NotesMD: %s", p)
	return p
}

func NotesPDF(dir string) string {
	p := filepath.Join(dir, "notes.pdf")
	logx.Debugf("archive", "NotesPDF: %s", p)
	return p
}

func Label(discipline string, n int) string {
	logx.Debugf("archive", "Label: enter discipline=%q n=%d", discipline, n)
	if n < 1 {
		n = 1
	}
	d := strings.TrimSpace(discipline)
	if d == "" {
		d = "лекция"
	}
	out := fmt.Sprintf("%s · лекция %d", d, n)
	logx.Debugf("archive", "Label: exit %q", out)
	return out
}

func PDFFileName(discipline string, n int) string {
	out := Slug(discipline) + fmt.Sprintf("-лекция-%d.pdf", n)
	logx.Debugf("archive", "PDFFileName: discipline=%q n=%d -> %q", discipline, n, out)
	return out
}

func UnderRoot(root, rel string) (string, error) {
	logx.Debugf("archive", "UnderRoot: enter root=%q rel=%q", root, rel)
	if strings.TrimSpace(root) == "" {
		logx.Debugf("archive", "UnderRoot: empty root -> recordings")
		root = "recordings"
	}
	abs, err := filepath.Abs(filepath.Join(root, rel))
	if err != nil {
		logx.Errorf("archive", "UnderRoot: abs join root=%q rel=%q: %v", root, rel, err)
		return "", err
	}
	base, err := filepath.Abs(root)
	if err != nil {
		logx.Errorf("archive", "UnderRoot: abs root=%q: %v", root, err)
		return "", err
	}
	sep := string(os.PathSeparator)
	if abs != base && !strings.HasPrefix(abs, base+sep) {
		logx.Errorf("archive", "UnderRoot: path escapes recordings: abs=%s base=%s", abs, base)
		return "", fmt.Errorf("path escapes recordings")
	}
	logx.Debugf("archive", "UnderRoot: exit %s", abs)
	return abs, nil
}
