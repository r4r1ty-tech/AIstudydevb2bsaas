package mcpapp

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// notesTestDB создаёт временную bot.db со схемой lecture_packs.
func notesTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if _, err := db.Exec(`CREATE TABLE lecture_packs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		lesson_id INTEGER,
		discipline TEXT,
		number INTEGER,
		date TEXT,
		dir TEXT,
		status TEXT,
		transcript TEXT,
		notes_pdf TEXT,
		err TEXT,
		publish_status TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// insertNotePack добавляет строку пака с явным id.
func insertNotePack(t *testing.T, db *sql.DB, id int64, discipline string, number int, date, dir, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO lecture_packs
		(id, lesson_id, discipline, number, date, dir, status, transcript, notes_pdf, err, publish_status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', 'published')`,
		id, id+100, discipline, number, date, dir, status,
		filepath.ToSlash(filepath.Join(dir, "transcript.txt")),
		filepath.ToSlash(filepath.Join(dir, "notes.pdf"))); err != nil {
		t.Fatal(err)
	}
}

// writeNoteTestFile создаёт файл конспекта вместе с каталогами.
func writeNoteTestFile(t *testing.T, root, rel, name string, data []byte) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel), filepath.FromSlash(filepath.Dir(name)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(name)), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// notesTestSession собирает сессию без Moodle: конспектам сеть не нужна.
func notesTestSession(t *testing.T, dbPath, recDir string) *Session {
	t.Helper()
	return &Session{
		cfg: Config{
			StateDir:      t.TempDir(),
			DBPath:        dbPath,
			RecordingsDir: recDir,
			Timeout:       5 * time.Second,
		},
		caches: newToolCaches(),
		limit:  NewLimiter(defaultToolConcurrency),
	}
}

// notesFixture готовит базу и каталог записей с двумя лекциями.
type notesFixture struct {
	sess   *Session
	root   string
	dbPath string
}

func newNotesFixture(t *testing.T) notesFixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "recordings")
	dbPath := filepath.Join(base, "bot.db")
	db := notesTestDB(t, dbPath)
	insertNotePack(t, db, 7, "Сети", 2, "2026-11-20", "Сети/лекция-2", "ready")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	writeNoteTestFile(t, root, "Сети/лекция-2", "notes.md", []byte("# Сети\n\nМаршрутизация и коммутация."))
	writeNoteTestFile(t, root, "Сети/лекция-2", "notes.pdf", makeFileTestPDF("PDF fallback text"))
	writeNoteTestFile(t, root, "Сети/лекция-2", "transcript.txt", []byte("расшифровка лекции про сети"))
	writeNoteTestFile(t, root, "Сети/лекция-2", "slides/slides.json", []byte(`[{"file":"01.png","text":"Слайд 1"}]`))
	// Второй пак есть только на диске: его должен найти скан.
	writeNoteTestFile(t, root, "Физика/лекция-1", "notes.md", []byte("# Физика\n\nКинематика."))

	return notesFixture{sess: notesTestSession(t, dbPath, root), root: root, dbPath: dbPath}
}

// TestNotesListMCP: список объединяет базу и скан, поддерживает фильтры.
func TestNotesListMCP(t *testing.T) {
	f := newNotesFixture(t)
	ctx := context.Background()

	out, err := notesTool(ctx, f.sess, NotesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Action != "list" || out.Total != 2 || len(out.Packs) != 2 {
		t.Fatalf("список: action=%q total=%d packs=%d", out.Action, out.Total, len(out.Packs))
	}
	if out.Packs == nil {
		t.Fatal("packs не должен быть nil")
	}
	var dbPack, scanPack NotePack
	for _, p := range out.Packs {
		switch p.Discipline {
		case "Сети":
			dbPack = p
		case "Физика":
			scanPack = p
		}
	}
	if dbPack.ID != 7 || dbPack.Number != 2 || dbPack.Date != "2026-11-20" || dbPack.Status != "ready" {
		t.Fatalf("пак из базы: %+v", dbPack)
	}
	if !dbPack.HasMarkdown || !dbPack.HasPDF || !dbPack.HasTranscript || !dbPack.HasSlides {
		t.Fatalf("признаки файлов базы: %+v", dbPack)
	}
	if dbPack.MarkdownURI != "moodle://notes/7/markdown" || dbPack.PDFURI != "moodle://notes/7/pdf" {
		t.Fatalf("uri: %q %q", dbPack.MarkdownURI, dbPack.PDFURI)
	}
	if scanPack.ID >= 0 || !scanPack.HasMarkdown || scanPack.HasPDF {
		t.Fatalf("пак со скана: %+v", scanPack)
	}

	filtered, err := notesTool(ctx, f.sess, NotesInput{Discipline: "сет"})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || filtered.Packs[0].Discipline != "Сети" {
		t.Fatalf("фильтр discipline: %+v", filtered)
	}
	byQuery, err := notesTool(ctx, f.sess, NotesInput{Query: "физика"})
	if err != nil {
		t.Fatal(err)
	}
	if byQuery.Total != 1 || byQuery.Packs[0].Discipline != "Физика" {
		t.Fatalf("фильтр query: %+v", byQuery)
	}
	limited, err := notesTool(ctx, f.sess, NotesInput{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if limited.Total != 2 || len(limited.Packs) != 1 {
		t.Fatalf("лимит: total=%d packs=%d", limited.Total, len(limited.Packs))
	}
}

// TestNotesReadMCP: auto и явные источники, ошибки id и source.
func TestNotesReadMCP(t *testing.T) {
	f := newNotesFixture(t)
	ctx := context.Background()

	out, err := notesTool(ctx, f.sess, NotesInput{Action: "read", ID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if out.Source != "markdown" || !strings.Contains(out.Text, "Маршрутизация") {
		t.Fatalf("auto markdown: source=%q text=%q", out.Source, out.Text)
	}
	if out.Discipline != "Сети" || out.Number != 2 || out.ID != 7 {
		t.Fatalf("метаданные read: %+v", out)
	}

	pdf, err := notesTool(ctx, f.sess, NotesInput{Action: "read", ID: 7, Source: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if pdf.Source != "pdf" || !strings.Contains(pdf.Text, "PDF fallback text") {
		t.Fatalf("pdf: source=%q text=%q", pdf.Source, pdf.Text)
	}

	tr, err := notesTool(ctx, f.sess, NotesInput{Action: "read", ID: 7, Source: "transcript"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Source != "transcript" || !strings.Contains(tr.Text, "расшифровка") {
		t.Fatalf("transcript: %+v", tr)
	}

	sl, err := notesTool(ctx, f.sess, NotesInput{Action: "read", ID: 7, Source: "slides"})
	if err != nil {
		t.Fatal(err)
	}
	if sl.Source != "slides" || !strings.Contains(sl.Text, "Слайд 1") {
		t.Fatalf("slides: %+v", sl)
	}

	list, err := notesTool(ctx, f.sess, NotesInput{Query: "физика"})
	if err != nil {
		t.Fatal(err)
	}
	scan, err := notesTool(ctx, f.sess, NotesInput{Action: "read", ID: list.Packs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scan.Text, "Кинематика") {
		t.Fatalf("скан-пак: %+v", scan)
	}

	if _, err := notesTool(ctx, f.sess, NotesInput{Action: "read"}); err == nil ||
		!strings.Contains(err.Error(), "bad_input") {
		t.Fatalf("без id: %v", err)
	}
	if _, err := notesTool(ctx, f.sess, NotesInput{Action: "read", ID: 999}); err == nil ||
		!strings.Contains(err.Error(), "not_found") {
		t.Fatalf("неизвестный id: %v", err)
	}
	if _, err := notesTool(ctx, f.sess, NotesInput{Action: "read", ID: 7, Source: "audio"}); err == nil ||
		!strings.Contains(err.Error(), "bad_input") {
		t.Fatalf("плохой source: %v", err)
	}
	if _, err := notesTool(ctx, f.sess, NotesInput{Action: "watch"}); err == nil ||
		!strings.Contains(err.Error(), "bad_input") {
		t.Fatalf("плохой action: %v", err)
	}
}

// TestNotesPDFFallbackMCP: без notes.md источник auto берёт текст из notes.pdf.
func TestNotesPDFFallbackMCP(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "recordings")
	writeNoteTestFile(t, root, "Химия/лекция-1", "notes.pdf", makeFileTestPDF("Fallback PDF chemistry"))
	sess := notesTestSession(t, filepath.Join(base, "missing.db"), root)

	list, err := notesTool(context.Background(), sess, NotesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || !list.Packs[0].HasPDF || list.Packs[0].HasMarkdown {
		t.Fatalf("список: %+v", list)
	}
	out, err := notesTool(context.Background(), sess, NotesInput{Action: "read", ID: list.Packs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.Source != "pdf" || !strings.Contains(out.Text, "Fallback PDF chemistry") {
		t.Fatalf("fallback: source=%q text=%q", out.Source, out.Text)
	}
}

// TestNotesPathTraversalMCP: абсолютные пути и «..» отклоняются, секрет не читается.
func TestNotesPathTraversalMCP(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "recordings")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.key"), []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(base, "bot.db")
	db := notesTestDB(t, dbPath)
	insertNotePack(t, db, 1, "Сети", 1, "", "../secret.key", "ready")
	insertNotePack(t, db, 2, "Сети", 2, "", "/etc", "ready")
	insertNotePack(t, db, 3, "Сети", 3, "", "Сети/../../secret.key", "ready")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	sess := notesTestSession(t, dbPath, root)
	ctx := context.Background()

	list, err := notesTool(ctx, sess, NotesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 3 {
		t.Fatalf("паки: %+v", list)
	}
	for _, p := range list.Packs {
		if p.HasMarkdown || p.HasPDF || p.HasTranscript || p.HasSlides {
			t.Fatalf("у небезопасного пака не должно быть файлов: %+v", p)
		}
	}
	for _, id := range []int64{1, 2, 3} {
		if _, err := notesTool(ctx, sess, NotesInput{Action: "read", ID: id}); err == nil ||
			!strings.Contains(err.Error(), "bad_path") {
			t.Fatalf("id=%d: ожидался bad_path, получено %v", id, err)
		} else if strings.Contains(err.Error(), "TOP-SECRET") {
			t.Fatalf("id=%d: секрет попал в ошибку: %v", id, err)
		}
	}
	for _, rel := range []string{"../secret.key", "/etc", "Сети/../../secret.key", "", "."} {
		if _, err := noteFilePath(root, rel, "notes.md"); err == nil {
			t.Fatalf("rel=%q должен быть отклонён", rel)
		}
	}
}

// TestNotesPDFUnreadableMCP: битый PDF и превышение лимита дают pdf_unreadable
// при явном source=pdf, но auto переходит к расшифровке; длинный текст
// честно помечается truncated.
func TestNotesPDFUnreadableMCP(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "recordings")
	writeNoteTestFile(t, root, "Химия/лекция-1", "notes.pdf", []byte("%PDF-1.4 broken"))
	writeNoteTestFile(t, root, "Химия/лекция-2", "notes.md", []byte(strings.Repeat("я", 50)))
	huge := filepath.Join(root, "Химия", "лекция-3")
	if err := os.MkdirAll(huge, 0o755); err != nil {
		t.Fatal(err)
	}
	hugePDF, err := os.Create(filepath.Join(huge, "notes.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := hugePDF.Truncate(maxNotePDFBytes + 1); err != nil {
		_ = hugePDF.Close()
		t.Fatal(err)
	}
	if err := hugePDF.Close(); err != nil {
		t.Fatal(err)
	}
	writeNoteTestFile(t, root, "Химия/лекция-4", "notes.pdf", []byte("%PDF-1.4 broken"))
	writeNoteTestFile(t, root, "Химия/лекция-4", "transcript.txt", []byte("спасительная расшифровка"))

	sess := notesTestSession(t, "", root)
	ctx := context.Background()
	list, err := notesTool(ctx, sess, NotesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 4 {
		t.Fatalf("паки: %+v", list)
	}
	var brokenID, longID, hugeID, fallbackID int64
	for _, p := range list.Packs {
		switch p.Number {
		case 1:
			brokenID = p.ID
		case 2:
			longID = p.ID
		case 3:
			hugeID = p.ID
		case 4:
			fallbackID = p.ID
		}
	}
	for _, id := range []int64{brokenID, hugeID} {
		if _, err := notesTool(ctx, sess, NotesInput{Action: "read", ID: id, Source: "pdf"}); err == nil ||
			!strings.Contains(err.Error(), "pdf_unreadable") {
			t.Fatalf("id=%d: ожидался pdf_unreadable, получено %v", id, err)
		}
	}
	// auto не должен сдаваться из-за битого PDF, если есть transcript.
	fallback, err := notesTool(ctx, sess, NotesInput{Action: "read", ID: fallbackID})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Source != "transcript" || !strings.Contains(fallback.Text, "спасительная расшифровка") {
		t.Fatalf("auto после pdf_unreadable: source=%q text=%q", fallback.Source, fallback.Text)
	}
	truncated, err := notesTool(ctx, sess, NotesInput{Action: "read", ID: longID, MaxChars: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !truncated.Truncated || len([]rune(truncated.Text)) != 5 {
		t.Fatalf("усечение: truncated=%v runes=%d", truncated.Truncated, len([]rune(truncated.Text)))
	}
}

// TestNotesSlidesWithoutManifestMCP: картинки без slides.json читаются как
// список файлов, а не отдают not_found.
func TestNotesSlidesWithoutManifestMCP(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "recordings")
	writeNoteTestFile(t, root, "Сети/лекция-1", "slides/01.png", []byte("png"))
	writeNoteTestFile(t, root, "Сети/лекция-1", "slides/02.jpg", []byte("jpg"))
	writeNoteTestFile(t, root, "Сети/лекция-1", "slides/notes.txt", []byte("не слайд"))
	sess := notesTestSession(t, "", root)
	ctx := context.Background()

	list, err := notesTool(ctx, sess, NotesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || !list.Packs[0].HasSlides {
		t.Fatalf("has_slides: %+v", list)
	}
	out, err := notesTool(ctx, sess, NotesInput{Action: "read", ID: list.Packs[0].ID, Source: "slides"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Source != "slides" || !strings.Contains(out.Text, "slides.json нет") ||
		!strings.Contains(out.Text, "01.png") || !strings.Contains(out.Text, "02.jpg") ||
		strings.Contains(out.Text, "notes.txt") {
		t.Fatalf("список слайдов: %+v", out)
	}
}

// TestNotesSymlinkEscapeMCP: симлинк файла и симлинк каталога не должны
// выпускать чтение за пределы recordings.
func TestNotesSymlinkEscapeMCP(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "recordings")
	secretPath := filepath.Join(base, "secret.key")
	if err := os.WriteFile(secretPath, []byte("TOP-SECRET-SYMLINK"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileDir := filepath.Join(root, "Сети", "лекция-1")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// ../../../secret.key из recordings/Сети/лекция-1 ведёт в base/secret.key.
	if err := os.Symlink("../../../secret.key", filepath.Join(fileDir, "notes.md")); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "notes.md"), []byte("OUTSIDE-NOTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(root, "Физика", "лекция-1")
	if err := os.MkdirAll(filepath.Dir(dirLink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dirLink); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(base, "bot.db")
	db := notesTestDB(t, dbPath)
	insertNotePack(t, db, 11, "Физика", 1, "", "Физика/лекция-1", "ready")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	sess := notesTestSession(t, dbPath, root)
	ctx := context.Background()

	list, err := notesTool(ctx, sess, NotesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("паки: %+v", list)
	}
	ids := map[string]int64{}
	for _, p := range list.Packs {
		ids[p.Discipline] = p.ID
		if p.HasMarkdown || p.HasPDF || p.HasTranscript || p.HasSlides {
			t.Fatalf("симлинк-побег не должен давать файлы: %+v", p)
		}
	}
	for _, discipline := range []string{"Сети", "Физика"} {
		_, err := notesTool(ctx, sess, NotesInput{Action: "read", ID: ids[discipline]})
		if err == nil || !strings.Contains(err.Error(), "bad_path") {
			t.Fatalf("%s: ожидался bad_path, получено %v", discipline, err)
		}
		if strings.Contains(err.Error(), "TOP-SECRET-SYMLINK") || strings.Contains(err.Error(), "OUTSIDE-NOTES") {
			t.Fatalf("%s: содержимое секрета попало в ошибку: %v", discipline, err)
		}
	}
	if _, err := noteFilePath(root, "Сети/лекция-1", "notes.md"); err == nil {
		t.Fatal("noteFilePath должен отклонять файл-симлинк наружу")
	}
	if _, err := noteFilePath(root, "Физика/лекция-1", "notes.md"); err == nil {
		t.Fatal("noteFilePath должен отклонять каталог-симлинк наружу")
	}
}

// TestNotesMissingDBMCP: без базы и без записей список пустой, но не падает.
func TestNotesMissingDBMCP(t *testing.T) {
	base := t.TempDir()
	sess := notesTestSession(t, filepath.Join(base, "nope.db"), filepath.Join(base, "no-recordings"))
	out, err := notesTool(context.Background(), sess, NotesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 0 || out.Packs == nil || len(out.Packs) != 0 {
		t.Fatalf("пустой список: %+v", out)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("warnings: %v", out.Warnings)
	}
}

// TestNotesResourcesMCP: шаблоны markdown/pdf, fallback из PDF и 404.
func TestNotesResourcesMCP(t *testing.T) {
	f := newNotesFixture(t)
	client, ctx := connectMCP(t, f.sess)

	templates, err := client.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var markdownTemplate, pdfTemplate bool
	for _, tpl := range templates.ResourceTemplates {
		switch tpl.URITemplate {
		case NotesMarkdownURI:
			markdownTemplate = true
		case NotesPDFURI:
			pdfTemplate = true
		}
	}
	if !markdownTemplate || !pdfTemplate {
		t.Fatalf("шаблоны: %+v", templates.ResourceTemplates)
	}

	md, err := client.ReadResource(ctx, &mcp.ReadResourceParams{URI: "moodle://notes/7/markdown"})
	if err != nil {
		t.Fatal(err)
	}
	if len(md.Contents) != 1 || md.Contents[0].MIMEType != "text/markdown" ||
		!strings.Contains(md.Contents[0].Text, "Маршрутизация") {
		t.Fatalf("markdown-ресурс: %+v", md.Contents)
	}

	pdf, err := client.ReadResource(ctx, &mcp.ReadResourceParams{URI: "moodle://notes/7/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pdf.Contents) != 1 || pdf.Contents[0].MIMEType != "application/pdf" ||
		!bytes.HasPrefix(pdf.Contents[0].Blob, []byte("%PDF")) {
		t.Fatalf("pdf-ресурс: mime=%q size=%d", pdf.Contents[0].MIMEType, len(pdf.Contents[0].Blob))
	}

	list, err := notesTool(ctx, f.sess, NotesInput{})
	if err != nil {
		t.Fatal(err)
	}
	var scanID int64
	for _, p := range list.Packs {
		if p.Discipline == "Физика" {
			scanID = p.ID
		}
	}
	if scanID == 0 {
		t.Fatal("скан-пак не найден")
	}
	scan, err := client.ReadResource(ctx, &mcp.ReadResourceParams{URI: noteURI(scanID, "markdown")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scan.Contents[0].Text, "Кинематика") {
		t.Fatalf("скан-ресурс: %+v", scan.Contents)
	}

	// Пак только с PDF: markdown-ресурс отдаёт извлечённый текст с пометкой.
	base := t.TempDir()
	root := filepath.Join(base, "recordings")
	writeNoteTestFile(t, root, "Химия/лекция-1", "notes.pdf", makeFileTestPDF("Only PDF here"))
	pdfOnly := notesTestSession(t, filepath.Join(base, "missing.db"), root)
	pdfClient, pdfCtx := connectMCP(t, pdfOnly)
	pdfList, err := notesTool(pdfCtx, pdfOnly, NotesInput{})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := pdfClient.ReadResource(pdfCtx, &mcp.ReadResourceParams{
		URI: noteURI(pdfList.Packs[0].ID, "markdown"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fallback.Contents[0].Text, "извлечён из notes.pdf") ||
		!strings.Contains(fallback.Contents[0].Text, "Only PDF here") {
		t.Fatalf("markdown из pdf: %q", fallback.Contents[0].Text)
	}

	if _, err := client.ReadResource(ctx, &mcp.ReadResourceParams{URI: "moodle://notes/999999/markdown"}); err == nil {
		t.Fatal("ожидалась ошибка для неизвестного id")
	}
	if _, err := client.ReadResource(ctx, &mcp.ReadResourceParams{URI: "moodle://notes/7/audio"}); err == nil {
		t.Fatal("ожидалась ошибка для неизвестного вида ресурса")
	}
}

// TestNotesPromptsMCP: три промпта в списке и рабочий prompts/get.
func TestNotesPromptsMCP(t *testing.T) {
	sess := notesTestSession(t, "", t.TempDir())
	client, ctx := connectMCP(t, sess)

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var notesToolFound bool
	for _, tool := range tools.Tools {
		if tool.Name != "moodle_notes" {
			continue
		}
		notesToolFound = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("moodle_notes без ReadOnlyHint: %+v", tool.Annotations)
		}
	}
	if !notesToolFound {
		t.Fatal("moodle_notes не зарегистрирован")
	}

	list, err := client.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{PromptPrepareLecture: false, PromptMakeNotes: false, PromptExamReview: false}
	for _, p := range list.Prompts {
		if _, ok := want[p.Name]; ok {
			want[p.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("промпт %s не найден в %+v", name, list.Prompts)
		}
	}

	cases := []struct {
		name string
		args map[string]string
		want []string
	}{
		{PromptPrepareLecture, map[string]string{"course": "Сети", "topic": "Маршрутизация", "cmid": "42"},
			[]string{"moodle_status", "moodle_courses", "moodle_course_outline", "moodle_deadlines", "moodle_material_read", "moodle_file_get", "moodle_notes", "Сети", "Маршрутизация", "42"}},
		{PromptMakeNotes, map[string]string{"course": "Сети", "topic": "Коммутация", "cmid": "43"},
			[]string{"moodle_notes", "moodle_material_read", "moodle_file_get", "Коммутация", "43"}},
		{PromptExamReview, map[string]string{"course": "Сети", "topic": "Экзамен"},
			[]string{"moodle_deadlines", "moodle_course_outline", "moodle_notes", "Сети", "Экзамен"}},
	}
	for _, tc := range cases {
		res, err := client.GetPrompt(ctx, &mcp.GetPromptParams{Name: tc.name, Arguments: tc.args})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(res.Messages) == 0 {
			t.Fatalf("%s: пустой ответ", tc.name)
		}
		text := ""
		for _, msg := range res.Messages {
			if content, ok := msg.Content.(*mcp.TextContent); ok {
				text += content.Text
			}
		}
		for _, needle := range tc.want {
			if !strings.Contains(text, needle) {
				t.Fatalf("%s: в промпте нет %q:\n%s", tc.name, needle, text)
			}
		}
	}
}

// TestNotesConfigRecordingsDirMCP: env и значения по умолчанию для каталога записей.
func TestNotesConfigRecordingsDirMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOODLE_MCP_RECORDINGS_DIR", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RecordingsDir != DefaultRecordingsDir {
		t.Fatalf("recordings по умолчанию = %q", cfg.RecordingsDir)
	}

	t.Setenv("MOODLE_MCP_RECORDINGS_DIR", "~/rec")
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RecordingsDir != filepath.Join(home, "rec") {
		t.Fatalf("recordings из env = %q", cfg.RecordingsDir)
	}

	cfg.SetRecordingsDir("~/other")
	if cfg.RecordingsDir != filepath.Join(home, "other") {
		t.Fatalf("SetRecordingsDir = %q", cfg.RecordingsDir)
	}
}
