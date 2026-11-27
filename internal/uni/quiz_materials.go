package uni

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodlematerial"
)

const (
	// materialsFileMaxChars — сколько символов берём из одного файла Moodle или пачки слайдов.
	materialsFileMaxChars = moodlematerial.FileMaxChars
)

// utf8Len считает символы, а не байты: промпт ограничен рунами.
func utf8Len(s string) int { return moodlematerial.RuneLen(s) }

// extractFileText достаёт текст из PDF, HTML или обычного текстового файла.
func extractFileText(data []byte, contentType string) string {
	return moodlematerial.ExtractFileText(data, contentType)
}

// collectMoodleCourseText — лёгкий фолбэк, когда локальных конспектов нет:
// читает текст секций курса, страницы-лекции (modtype_page), файлы-ресурсы
// (modtype_resource) и файлы из папок (modtype_folder) Moodle.
// Ошибки не валят решение теста — просто остаёмся без этого источника.
func (s *Syncer) collectMoodleCourseText(ctx context.Context, tag quizTag, c *moodle.Client, courseID int64) string {
	text, err := moodlematerial.CollectCourseText(ctx, c, courseID, moodlematerial.DefaultCourseOptions())
	if err != nil {
		logx.Debugf("uni", "quiz %s: материалы Moodle: курс %d не открылся: %v", tag, courseID, err)
		return ""
	}
	if text != "" {
		logx.Infof("uni", "quiz %s: материалы Moodle: %d симв.", tag, utf8Len(text))
	}
	return text
}

// slidesText собирает текстовый слой слайдов из <dir>/slides/slides.json
// в порядке номеров слайдов, не больше materialsFileMaxChars символов.
func slidesText(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "slides", "slides.json"))
	if err != nil || len(data) == 0 {
		return ""
	}
	var items []struct {
		Num  int    `json:"num"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &items); err != nil {
		logx.Debugf("uni", "материалы: slides.json в %s: %v", dir, err)
		return ""
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Num < items[j].Num })
	var b strings.Builder
	for _, it := range items {
		txt := cleanStr(it.Text)
		if txt == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(txt)
		if utf8Len(b.String()) >= materialsFileMaxChars {
			break
		}
	}
	return moodlematerial.ClipRunes(b.String(), materialsFileMaxChars)
}
