package mcpapp

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// makeFileTestPDF собирает минимальный PDF с одной страницей текста.
func makeFileTestPDF(text string) []byte {
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

// fileFake — фейковый Moodle с файлами: PDF через редирект, текст, «огромный»
// файл и редирект на чужой хост.
type fileFake struct {
	fake *fakeMoodle

	mu          sync.Mutex
	pdf         []byte
	foreignURL  string
	downloads   map[string]int
	resourceGet int
}

func (f *fileFake) count(key string) {
	f.mu.Lock()
	f.downloads[key]++
	f.mu.Unlock()
}

func (f *fileFake) downloadCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloads[key]
}

// newFileTestServer поднимает fakeMoodle и файловые маршруты, включая редирект
// на чужой хост (localhost) и «файл» больше 25 МБ.
func newFileTestServer(t *testing.T) (*httptest.Server, *fileFake, *atomic.Int64) {
	t.Helper()
	fake := newFakeMoodle()
	f := &fileFake{
		fake:      fake,
		pdf:       makeFileTestPDF("Privet PDF materials"),
		downloads: map[string]int{},
	}
	var foreignHits atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignHits.Add(1)
		_, _ = w.Write([]byte("чужой файл"))
	}))
	t.Cleanup(foreign.Close)
	// Разные порты на 127.0.0.1 — один хост для allowlist, поэтому чужой
	// сервер подставляем под именем localhost.
	f.foreignURL = strings.Replace(foreign.URL, "127.0.0.1", "localhost", 1) + "/file.pdf"

	fallback := fake.handler()
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/moodle/course/view.php":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(courseOutlinePage(r.URL.Query().Get("id"))))
		case "/moodle/mod/resource/view.php":
			f.mu.Lock()
			f.resourceGet++
			f.mu.Unlock()
			http.Redirect(w, r, "/moodle/pluginfile.php/1/mod_resource/content/0/doc.pdf", http.StatusFound)
		case "/moodle/pluginfile.php/1/mod_resource/content/0/doc.pdf":
			f.count("pdf")
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(f.pdf)
		case "/moodle/pluginfile.php/1/mod_folder/content/0/notes.txt":
			f.count("text")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("Текст заметки про энтропию."))
		case "/moodle/pluginfile.php/1/mod_folder/content/0/huge.bin":
			f.count("huge")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.FormatInt(26<<20, 10))
			w.WriteHeader(http.StatusOK)
		case "/moodle/pluginfile.php/1/mod_folder/content/0/cross":
			f.count("cross")
			http.Redirect(w, r, f.foreignURL, http.StatusFound)
		case "/moodle/pluginfile.php/1/mod_folder/content/0/login":
			f.count("login")
			http.Redirect(w, r, "/moodle/login/index.php", http.StatusFound)
		case "/moodle/pluginfile.php/1/mod_folder/content/0/missing":
			http.NotFound(w, r)
		default:
			fallback.ServeHTTP(w, r)
		}
	})
	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv, f, &foreignHits
}

func TestFileGetByURLPDF(t *testing.T) {
	srv, f, _ := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out FileOutput
	callMaterialTool(t, cs, ctx, "moodle_file_get", map[string]any{
		"url": srv.URL + "/moodle/pluginfile.php/1/mod_resource/content/0/doc.pdf",
	}, &out)
	if !out.Extracted || !strings.Contains(out.Text, "Privet PDF") {
		t.Fatalf("текст PDF: %+v", out)
	}
	if out.Filename != "doc.pdf" || !strings.Contains(out.ContentType, "pdf") {
		t.Fatalf("метаданные файла: %+v", out)
	}
	if out.Size != int64(len(f.pdf)) {
		t.Fatalf("size = %d, want %d", out.Size, len(f.pdf))
	}
	if out.Saved || out.Path != "" {
		t.Fatalf("файл сохранён без save=true: %+v", out)
	}
}

func TestFileGetByCMIDResolvesThroughOutline(t *testing.T) {
	srv, f, _ := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out FileOutput
	callMaterialTool(t, cs, ctx, "moodle_file_get", map[string]any{
		"cmid": 63209, "course_id": 1742,
	}, &out)
	if !out.Extracted || !strings.Contains(out.Text, "Privet PDF") {
		t.Fatalf("файл по cmid: %+v", out)
	}
	if f.downloadCount("pdf") != 1 || f.downloadCount("huge") != 0 {
		t.Fatalf("скачивания: %+v", f.downloads)
	}
	f.mu.Lock()
	resourceGet := f.resourceGet
	f.mu.Unlock()
	if resourceGet != 1 {
		t.Fatalf("страница resource запрошена %d раз, want 1", resourceGet)
	}
}

func TestFileGetCrossHostRedirect(t *testing.T) {
	srv, _, foreignHits := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	text := callToolError(t, cs, ctx, "moodle_file_get", map[string]any{
		"url": srv.URL + "/moodle/pluginfile.php/1/mod_folder/content/0/cross",
	})
	if !strings.Contains(text, "forbidden_host") {
		t.Fatalf("редирект на чужой хост: %q", text)
	}
	if got := foreignHits.Load(); got != 0 {
		t.Fatalf("запрос ушёл на чужой хост: %d", got)
	}

	// Прямой чужой адрес отклоняется до запроса.
	text = callToolError(t, cs, ctx, "moodle_file_get", map[string]any{"url": "https://evil.example/file.pdf"})
	if !strings.Contains(text, "forbidden_host") {
		t.Fatalf("прямой чужой url: %q", text)
	}
}

func TestFileGetTooLarge(t *testing.T) {
	srv, _, _ := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	text := callToolError(t, cs, ctx, "moodle_file_get", map[string]any{
		"url": srv.URL + "/moodle/pluginfile.php/1/mod_folder/content/0/huge.bin",
	})
	if !strings.Contains(text, "too_large") {
		t.Fatalf("файл больше 25 МБ: %q", text)
	}
}

func TestFileGetNotFound(t *testing.T) {
	srv, _, _ := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	text := callToolError(t, cs, ctx, "moodle_file_get", map[string]any{
		"url": srv.URL + "/moodle/pluginfile.php/1/mod_folder/content/0/missing",
	})
	if !strings.Contains(text, "not_found") {
		t.Fatalf("404: %q", text)
	}

	text = callToolError(t, cs, ctx, "moodle_file_get", map[string]any{"cmid": 999999})
	if !strings.Contains(text, "not_found") {
		t.Fatalf("неизвестный cmid: %q", text)
	}
}

func TestFileGetUnauthorized(t *testing.T) {
	srv, _, _ := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	text := callToolError(t, cs, ctx, "moodle_file_get", map[string]any{
		"url": srv.URL + "/moodle/pluginfile.php/1/mod_folder/content/0/login",
	})
	if !strings.Contains(text, "unauthorized") {
		t.Fatalf("редирект на вход: %q", text)
	}
}

// TestFileGetSave: save=true пишет файл в StateDir/files с правами 0600.
func TestFileGetSave(t *testing.T) {
	srv, _, _ := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out FileOutput
	callMaterialTool(t, cs, ctx, "moodle_file_get", map[string]any{
		"url":  srv.URL + "/moodle/pluginfile.php/1/mod_folder/content/0/notes.txt",
		"save": true,
	}, &out)
	if !out.Saved || out.Path == "" {
		t.Fatalf("save не отмечен: %+v", out)
	}
	wantDir := filepath.Join(sess.cfg.StateDir, "files")
	if filepath.Dir(out.Path) != wantDir {
		t.Fatalf("path = %q, want каталог %q", out.Path, wantDir)
	}
	info, err := os.Stat(out.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("права файла = %o, want 600", got)
	}
	data, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Текст заметки") {
		t.Fatalf("содержимое файла: %q", data)
	}
	if !out.Extracted || !strings.Contains(out.Text, "энтропию") {
		t.Fatalf("текст не извлечён при save: %+v", out)
	}
}

// TestFileGetExtractDisabled: extract_text=false оставляет только метаданные.
func TestFileGetExtractDisabled(t *testing.T) {
	srv, _, _ := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out FileOutput
	callMaterialTool(t, cs, ctx, "moodle_file_get", map[string]any{
		"url":          srv.URL + "/moodle/pluginfile.php/1/mod_folder/content/0/notes.txt",
		"extract_text": false,
	}, &out)
	if out.Extracted || out.Text != "" {
		t.Fatalf("текст извлечён при extract_text=false: %+v", out)
	}
	if out.Size == 0 {
		t.Fatalf("размер файла не заполнен: %+v", out)
	}
}

// TestFileGetMaxChars: текст режется по рунам, усечение отмечается.
func TestFileGetMaxChars(t *testing.T) {
	srv, _, _ := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	var out FileOutput
	callMaterialTool(t, cs, ctx, "moodle_file_get", map[string]any{
		"url":       srv.URL + "/moodle/pluginfile.php/1/mod_folder/content/0/notes.txt",
		"max_chars": 4,
	}, &out)
	if n := len([]rune(out.Text)); n != 4 {
		t.Fatalf("текст = %d рун, want 4: %q", n, out.Text)
	}
	if !out.Truncated {
		t.Fatal("усечение не отмечено")
	}
}

func TestFileGetBadInput(t *testing.T) {
	srv, _, _ := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	text := callToolError(t, cs, ctx, "moodle_file_get", map[string]any{})
	if !strings.Contains(text, "bad_input") {
		t.Fatalf("пустой запрос: %q", text)
	}
}

func TestFileToolRegistered(t *testing.T) {
	srv, _, _ := newFileTestServer(t)
	sess := newMaterialSession(t, srv.URL)
	cs, ctx := connectMCP(t, sess)

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range list.Tools {
		if tool.Name != "moodle_file_get" {
			continue
		}
		found = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal("moodle_file_get без ReadOnlyHint")
		}
	}
	if !found {
		t.Fatal("в tools/list нет moodle_file_get")
	}
}

// TestCheckFileURL: allowlist, схемы и protocol-relative адреса.
func TestCheckFileURL(t *testing.T) {
	allowed := []string{"do.ssau.ru", "127.0.0.1"}
	ok := []string{
		"https://do.ssau.ru/moodle/pluginfile.php/1/x",
		"http://127.0.0.1:8080/moodle/pluginfile.php/1/x",
		"/moodle/pluginfile.php/1/x",
	}
	for _, raw := range ok {
		if err := checkFileURL(raw, allowed); err != nil {
			t.Errorf("checkFileURL(%q) = %v, want nil", raw, err)
		}
	}
	bad := []string{
		"https://evil.example/x",
		"ftp://do.ssau.ru/x",
		"//do.ssau.ru/x",
		"https://do.ssau.ru.evil.example/x",
	}
	for _, raw := range bad {
		err := checkFileURL(raw, allowed)
		if !errors.Is(err, errFileForbiddenHost) {
			t.Errorf("checkFileURL(%q) = %v, want errFileForbiddenHost", raw, err)
		}
	}
}

// TestSafeFileName: имя не выходит за пределы каталога files.
func TestSafeFileName(t *testing.T) {
	cases := map[string]string{
		"../etc/passwd": "passwd",
		`..\..\windows`: "windows",
		"....":          defaultFileName,
		".hidden":       "_hidden",
		"лекция 1.pdf":  "лекция_1.pdf",
		"":              defaultFileName,
	}
	for in, want := range cases {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
