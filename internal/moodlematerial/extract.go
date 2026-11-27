// Package moodlematerial собирает читаемый текст материалов Moodle: секции
// курса, страницы, файлы, папки, книги, ссылки и уроки. Разбор HTML и файлов
// отделён от MCP-слоя, чтобы одни и те же правила работали и в боте, и в MCP.
package moodlematerial

import (
	"bytes"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/ledongthuc/pdf"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

const (
	// FileMaxChars — сколько рун берём из одного файла Moodle или пачки слайдов.
	FileMaxChars = 8000
	// FolderMaxFiles — сколько файлов скачиваем из одной папки Moodle.
	FolderMaxFiles = 3
	// MaxFolders — сколько папок Moodle открываем за один сбор материалов.
	MaxFolders = 3
	// MaxResources — сколько файлов-ресурсов Moodle разбираем.
	MaxResources = 3
	// DefaultCourseTextMaxChars — общий лимит текста курса по умолчанию.
	DefaultCourseTextMaxChars = 16000
)

// Builder копит подписанные куски текста в пределах лимита рун.
// Повторяющийся текст пропускается: страница и её же файл не дублируются.
type Builder struct {
	max  int
	b    strings.Builder
	seen map[string]bool
}

// NewBuilder создаёт сборщик с лимитом max рун.
func NewBuilder(max int) *Builder {
	return &Builder{max: max, seen: make(map[string]bool)}
}

// Add добавляет текст с заголовком-источником, схлопывая пробелы.
// Возвращает false, когда лимит исчерпан.
func (m *Builder) Add(label, text string) bool {
	text = CleanText(text)
	if text == "" {
		return !m.Full()
	}
	if m.seen[text] {
		return !m.Full()
	}
	m.seen[text] = true
	header := "\n--- " + label + " ---\n"
	remaining := m.max - m.Len() - RuneLen(header) - 1
	if remaining <= 0 {
		return false
	}
	text = ClipRunes(text, remaining)
	m.b.WriteString(header)
	m.b.WriteString(text)
	m.b.WriteString("\n")
	return !m.Full()
}

// Len — сколько рун уже накоплено.
func (m *Builder) Len() int { return RuneLen(m.b.String()) }

// Full сообщает, что лимит исчерпан.
func (m *Builder) Full() bool { return m.Len() >= m.max }

// String возвращает текст без крайних пробелов.
func (m *Builder) String() string { return strings.TrimSpace(m.b.String()) }

// RuneLen считает символы, а не байты: лимиты заданы в рунах.
func RuneLen(s string) int { return utf8.RuneCountInString(s) }

// ClipRunes обрезает строку до n рун без многоточия.
func ClipRunes(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// CleanText схлопывает любые пробельные последовательности в один пробел.
func CleanText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ExtractFileText достаёт текст из PDF, HTML или обычного текстового файла.
// Для двоичных форматов (картинки, архивы, docx) возвращает пустую строку.
func ExtractFileText(data []byte, contentType string) string {
	if len(data) == 0 {
		return ""
	}
	ct := strings.ToLower(contentType)
	trimmed := bytes.TrimSpace(data)
	isPDF := strings.Contains(ct, "pdf") || bytes.HasPrefix(trimmed, []byte("%PDF"))
	if isPDF {
		return PDFPlainText(data)
	}
	if strings.Contains(ct, "html") || strings.Contains(ct, "xml") || bytes.HasPrefix(trimmed, []byte("<")) {
		doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
		if err != nil {
			return ""
		}
		doc.Find("script, style, noscript").Remove()
		return CleanText(doc.Text())
	}
	if !IsMostlyText(data) {
		return ""
	}
	return string(data)
}

// PDFPlainText извлекает текстовый слой PDF. Библиотека может паниковать
// на битых файлах, поэтому паника превращается в «текста нет».
func PDFPlainText(data []byte) (text string) {
	defer func() {
		if r := recover(); r != nil {
			logx.Debugf("moodlematerial", "разбор PDF прерван: %v", r)
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

// HTMLMainText извлекает текст основной области HTML-страницы Moodle без
// шапки, меню и подвала. У обычного HTML-файла основной области нет, поэтому
// берётся весь текст.
func HTMLMainText(data []byte) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	doc.Find("script, style, noscript, nav, header, footer").Remove()
	scope := doc.Find("#region-main, [role=main], #page-content").First()
	if scope.Length() == 0 {
		return CleanText(doc.Text())
	}
	return CleanText(scope.Text())
}

// IsMostlyText — грубая проверка, что байты похожи на текст, а не на бинарник.
func IsMostlyText(data []byte) bool {
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

// FileNameFromURL достаёт имя файла из ссылки pluginfile.php.
func FileNameFromURL(src string) string {
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
