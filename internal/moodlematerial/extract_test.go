package moodlematerial

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// makeTestPDF собирает минимальный PDF с одной страницей текста.
func makeTestPDF(text string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, 6)
	obj := func(num int, body string) {
		offsets[num] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] "+
		"/Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	obj(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	stream := fmt.Sprintf("BT /F1 12 Tf 20 100 Td (%s) Tj ET", text)
	obj(5, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	xref := b.Len()
	b.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

func TestExtractFileTextFormats(t *testing.T) {
	pdfText := ExtractFileText(makeTestPDF("Privet PDF materials"), "application/pdf")
	if !strings.Contains(pdfText, "Privet") {
		t.Fatalf("текст PDF не извлечён: %q", pdfText)
	}
	htmlText := ExtractFileText([]byte("<html><body><p>HTML текст лекции</p></body></html>"), "text/html")
	if !strings.Contains(htmlText, "HTML текст лекции") {
		t.Fatalf("текст HTML не извлечён: %q", htmlText)
	}
	plain := ExtractFileText([]byte("обычный текст без разметки"), "text/plain")
	if !strings.Contains(plain, "обычный текст") {
		t.Fatalf("обычный текст не прочитан: %q", plain)
	}
	if got := ExtractFileText([]byte{0, 1, 2, 3, 255}, "application/octet-stream"); got != "" {
		t.Fatalf("двоичный файл дал текст: %q", got)
	}
	if got := ExtractFileText([]byte("не pdf"), "application/pdf"); got != "" {
		t.Fatalf("битый PDF дал текст: %q", got)
	}
	if got := ExtractFileText(nil, "text/plain"); got != "" {
		t.Fatalf("пустые данные дали текст: %q", got)
	}
}

// TestPDFPlainTextRecovers: паника библиотеки на битых данных не должна
// выходить наружу — только пустой текст.
func TestPDFPlainTextRecovers(t *testing.T) {
	if got := PDFPlainText([]byte("%PDF-1.4\nмусор")); got != "" {
		t.Fatalf("битый PDF дал текст: %q", got)
	}
	if got := PDFPlainText([]byte{0xff, 0x00, 0x13}); got != "" {
		t.Fatalf("двоичные данные дали текст: %q", got)
	}
}

// TestHTMLMainText: из страницы Moodle берётся только основная область без
// шапки и меню, а у обычного файла — весь текст.
func TestHTMLMainText(t *testing.T) {
	page := `<html><body><header>Личный кабинет</header><nav>Курсы События</nav>
<div id="region-main"><h1>Тема</h1><p>Полезный текст лекции</p></div>
<footer>Подвал</footer></body></html>`
	got := HTMLMainText([]byte(page))
	if !strings.Contains(got, "Полезный текст лекции") || strings.Contains(got, "Личный кабинет") || strings.Contains(got, "Подвал") {
		t.Fatalf("HTMLMainText = %q", got)
	}
	plain := HTMLMainText([]byte(`<html><body><p>Просто файл</p></body></html>`))
	if plain != "Просто файл" {
		t.Fatalf("HTMLMainText без основной области = %q", plain)
	}
}

func TestIsMostlyText(t *testing.T) {
	if !IsMostlyText([]byte("строка\nс табуляцией\tи переводом")) {
		t.Fatal("обычный текст признан бинарным")
	}
	if IsMostlyText([]byte{0x00, 0x01, 0x02, 0x03}) {
		t.Fatal("бинарные управляющие байты признаны текстом")
	}
	if IsMostlyText([]byte{0xff, 0xfe, 0xfd}) {
		t.Fatal("невалидный UTF-8 признан текстом")
	}
}

func TestFileNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://moodle.example/pluginfile.php/1/mod_folder/content/0/sub/lab%20work.pdf":            "lab work.pdf",
		"https://moodle.example/pluginfile.php/1/mod_resource/content/2/lecture.pdf?forcedownload=1": "lecture.pdf",
		"/pluginfile.php/1/mod_page/content/0/format.pdf":                                            "format.pdf",
		"": "файл",
	}
	for in, want := range cases {
		if got := FileNameFromURL(in); got != want {
			t.Errorf("FileNameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanTextAndClipRunes(t *testing.T) {
	got := CleanText("   Предел\n\n\tпоследовательности   функции  ")
	if got != "Предел последовательности функции" {
		t.Fatalf("CleanText = %q", got)
	}
	if got := ClipRunes("Привет", 3); got != "При" {
		t.Fatalf("ClipRunes = %q", got)
	}
	if got := ClipRunes("коротко", 100); got != "коротко" {
		t.Fatalf("ClipRunes испортил короткую строку: %q", got)
	}
	if got := ClipRunes("текст", -1); got != "" {
		t.Fatalf("ClipRunes с отрицательным лимитом = %q", got)
	}
	if n := RuneLen("ёжик"); n != 4 {
		t.Fatalf("RuneLen = %d", n)
	}
}

func TestBuilderCollapsesDeduplicatesAndLimits(t *testing.T) {
	mb := NewBuilder(80)
	if !mb.Add("Курс, Введение", "   Первый\n\nтекст  ") {
		t.Fatal("первый фрагмент не добавлен")
	}
	if !mb.Add("Курс, Ещё", "Первый текст") {
		t.Fatal("дубль не должен считаться исчерпанием лимита")
	}
	if mb.String() != "--- Курс, Введение ---\nПервый текст" {
		t.Fatalf("сборка = %q", mb.String())
	}
	if strings.Contains(mb.String(), "  ") {
		t.Fatalf("в сборке остались двойные пробелы: %q", mb.String())
	}

	small := NewBuilder(30)
	small.Add("Источник", strings.Repeat("Я", 100))
	if n := RuneLen(small.String()); n > 30 {
		t.Fatalf("сборщик превысил лимит: %d рун", n)
	}
	if !small.Full() {
		t.Fatal("сборщик не считает себя полным после лимита")
	}
}
