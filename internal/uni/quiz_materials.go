package uni

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/ledongthuc/pdf"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

const (
	// materialsFileMaxChars — сколько символов берём из одного файла Moodle или пачки слайдов.
	materialsFileMaxChars = 8000
	// moodleFolderMaxFiles — сколько файлов скачиваем из одной папки Moodle.
	moodleFolderMaxFiles = 3
	// moodleMaxFolders — сколько папок Moodle открываем за один сбор материалов.
	moodleMaxFolders = 3
	// moodleMaxResources — сколько файлов-ресурсов Moodle разбираем.
	moodleMaxResources = 3
)

// materialsBuilder копит подписанные куски текста в пределах лимита рун.
type materialsBuilder struct {
	max  int
	b    strings.Builder
	seen map[string]bool
}

// newMaterialsBuilder создаёт сборщик с лимитом max рун.
func newMaterialsBuilder(max int) *materialsBuilder {
	return &materialsBuilder{max: max, seen: make(map[string]bool)}
}

// add добавляет текст с заголовком-источником, схлопывая пробелы.
// Возвращает false, когда лимит исчерпан.
func (m *materialsBuilder) add(label, text string) bool {
	text = cleanStr(text)
	if text == "" {
		return !m.full()
	}
	if m.seen[text] {
		return !m.full()
	}
	m.seen[text] = true
	header := "\n--- " + label + " ---\n"
	remaining := m.max - m.len() - utf8Len(header) - 1
	if remaining <= 0 {
		return false
	}
	if r := []rune(text); len(r) > remaining {
		text = string(r[:remaining])
	}
	m.b.WriteString(header)
	m.b.WriteString(text)
	m.b.WriteString("\n")
	return !m.full()
}

func (m *materialsBuilder) len() int { return utf8Len(m.b.String()) }

func (m *materialsBuilder) full() bool { return m.len() >= m.max }

func (m *materialsBuilder) String() string { return strings.TrimSpace(m.b.String()) }

// utf8Len считает символы, а не байты: промпт ограничен рунами.
func utf8Len(s string) int { return utf8.RuneCountInString(s) }

// clipRunesPlain обрезает строку до n символов без многоточия.
func clipRunesPlain(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// collectMoodleCourseText — лёгкий фолбэк, когда локальных конспектов нет:
// читает текст секций курса, страницы-лекции (modtype_page), файлы-ресурсы
// (modtype_resource) и файлы из папок (modtype_folder) Moodle.
// Ошибки не валят решение теста — просто остаёмся без этого источника.
func (s *Syncer) collectMoodleCourseText(ctx context.Context, tag quizTag, c *moodle.Client, courseID int64) string {
	if c == nil || courseID == 0 {
		return ""
	}
	doc, _, err := c.PageWithURL(ctx, fmt.Sprintf("/course/view.php?id=%d", courseID))
	if err != nil {
		logx.Debugf("uni", "quiz %s: материалы Moodle: курс %d не открылся: %v", tag, courseID, err)
		return ""
	}
	mb := newMaterialsBuilder(16000)
	doc.Find("li.section, li.course-section").EachWithBreak(func(_ int, sec *goquery.Selection) bool {
		title := cleanStr(sec.Find(".sectionname, h3").First().Text())
		if title == "" {
			title = "секция курса"
		}
		sec.Find("script, style, noscript").Remove()
		return mb.add("Курс, "+title, sec.Text())
	})
	// Страницы-лекции: берём первые три, чтобы не раздувать промпт.
	pages := 0
	doc.Find("li.activity.modtype_page a.activityname, li.activity.modtype_page a.activityinstance").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		if pages >= 3 || mb.full() {
			return false
		}
		href := strings.TrimSpace(a.AttrOr("href", ""))
		if href == "" {
			return true
		}
		pages++
		page, _, err := c.PageWithURL(ctx, href)
		if err != nil {
			logx.Debugf("uni", "quiz %s: материалы Moodle: страница %s: %v", tag, href, err)
			return true
		}
		page.Find("script, style, nav, header, footer, noscript").Remove()
		text := cleanStr(page.Find("#region-main, .course-content").First().Text())
		return mb.add("Лекция Moodle: "+cleanStr(a.Text()), text)
	})
	// Файлы-ресурсы: ссылка ведёт на pluginfile.php или на view.php с редиректом.
	resources := 0
	doc.Find("li.activity.modtype_resource").EachWithBreak(func(_ int, act *goquery.Selection) bool {
		if resources >= moodleMaxResources || mb.full() {
			return false
		}
		a := act.Find("a.activityname, a.activityinstance").First()
		href := strings.TrimSpace(a.AttrOr("href", ""))
		if href == "" {
			return true
		}
		resources++
		label := "Файл Moodle: " + cleanStr(a.Text())
		return appendMoodleFile(ctx, tag, c, mb, label, href)
	})
	// Папки Moodle: открываем страницу папки и берём первые файлы.
	folders := 0
	doc.Find("li.activity.modtype_folder").EachWithBreak(func(_ int, act *goquery.Selection) bool {
		if folders >= moodleMaxFolders || mb.full() {
			return false
		}
		a := act.Find("a.activityname, a.activityinstance").First()
		href := strings.TrimSpace(a.AttrOr("href", ""))
		if href == "" {
			href = strings.TrimSpace(act.Find(`a[href*="/mod/folder/view.php"]`).First().AttrOr("href", ""))
		}
		if href == "" {
			return true
		}
		folders++
		name := cleanStr(a.Text())
		if name == "" {
			name = "папка"
		}
		return collectMoodleFolder(ctx, tag, c, mb, name, href)
	})
	res := mb.String()
	if res != "" {
		logx.Infof("uni", "quiz %s: материалы Moodle: %d симв.", tag, utf8Len(res))
	}
	return res
}

// collectMoodleFolder открывает страницу папки Moodle и добавляет текст
// не более moodleFolderMaxFiles первых файлов. Возвращает false при исчерпании лимита.
func collectMoodleFolder(ctx context.Context, tag quizTag, c *moodle.Client, mb *materialsBuilder, name, href string) bool {
	doc, _, err := c.PageWithURL(ctx, href)
	if err != nil {
		logx.Debugf("uni", "quiz %s: материалы Moodle: папка %s: %v", tag, href, err)
		return true
	}
	links := make([]string, 0, moodleFolderMaxFiles)
	seen := map[string]bool{}
	doc.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		if len(links) >= moodleFolderMaxFiles {
			return false
		}
		src := strings.TrimSpace(a.AttrOr("href", ""))
		if !strings.Contains(src, "pluginfile.php") || seen[src] {
			return true
		}
		seen[src] = true
		links = append(links, src)
		return true
	})
	for _, src := range links {
		if mb.full() {
			return false
		}
		label := fmt.Sprintf("Папка Moodle: %s / %s", name, moodleFileName(src))
		if !appendMoodleFile(ctx, tag, c, mb, label, src) {
			return false
		}
	}
	return true
}

// appendMoodleFile скачивает файл Moodle и добавляет извлечённый текст.
// Любая ошибка означает «без этого файла», а не сбой сбора материалов.
func appendMoodleFile(ctx context.Context, tag quizTag, c *moodle.Client, mb *materialsBuilder, label, src string) bool {
	data, contentType, err := c.FetchFile(ctx, src)
	if err != nil {
		logx.Debugf("uni", "quiz %s: материалы Moodle: файл %s: %v", tag, src, err)
		return !mb.full()
	}
	text := extractFileText(data, contentType)
	if text == "" {
		logx.Debugf("uni", "quiz %s: материалы Moodle: из файла %s текст не извлечён", tag, src)
		return !mb.full()
	}
	return mb.add(label, clipRunesPlain(text, materialsFileMaxChars))
}

// extractFileText достаёт текст из PDF, HTML или обычного текстового файла.
// Для двоичных форматов (картинки, архивы, docx) возвращает пустую строку.
func extractFileText(data []byte, contentType string) string {
	if len(data) == 0 {
		return ""
	}
	ct := strings.ToLower(contentType)
	trimmed := bytes.TrimSpace(data)
	isPDF := strings.Contains(ct, "pdf") || bytes.HasPrefix(trimmed, []byte("%PDF"))
	if isPDF {
		return pdfPlainText(data)
	}
	if strings.Contains(ct, "html") || strings.Contains(ct, "xml") || bytes.HasPrefix(trimmed, []byte("<")) {
		doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
		if err != nil {
			return ""
		}
		doc.Find("script, style, noscript").Remove()
		return cleanStr(doc.Text())
	}
	if !isMostlyText(data) {
		return ""
	}
	return string(data)
}

// pdfPlainText извлекает текстовый слой PDF. Библиотека может паниковать
// на битых файлах, поэтому паника превращается в «текста нет».
func pdfPlainText(data []byte) (text string) {
	defer func() {
		if r := recover(); r != nil {
			logx.Debugf("uni", "материалы: разбор PDF прерван: %v", r)
			text = ""
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ""
	}
	rd, err := r.GetPlainText()
	if err != nil {
		return ""
	}
	var b strings.Builder
	if _, err := io.Copy(&b, io.LimitReader(rd, 4<<20)); err != nil {
		return ""
	}
	return b.String()
}

// isMostlyText — грубая проверка, что байты похожи на текст, а не на бинарник.
func isMostlyText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	sample := data
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	control := 0
	for _, c := range sample {
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' {
			control++
		}
	}
	return control*100 <= len(sample)
}

// moodleFileName достаёт имя файла из ссылки pluginfile.php.
func moodleFileName(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return path.Base(src)
	}
	name := path.Base(u.Path)
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	if name == "." || name == "/" || name == "" {
		return "файл"
	}
	return name
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
	return clipRunesPlain(b.String(), materialsFileMaxChars)
}
