package mcpapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodlematerial"
)

const (
	// defaultNotesLimit — сколько конспектов отдаёт список без явного лимита.
	defaultNotesLimit = 50
	// maxNotesLimit — потолок списка, чтобы ответ не распухал.
	maxNotesLimit = 500
	// maxNoteTextBytes — сколько байт текстового конспекта читаем с диска.
	maxNoteTextBytes = 16 << 20
	// maxNotePDFBytes — потолок PDF-ресурса.
	maxNotePDFBytes = 25 << 20

	// NotesMarkdownURI — шаблон ресурса с markdown-конспектом.
	NotesMarkdownURI = "moodle://notes/{id}/markdown"
	// NotesPDFURI — шаблон ресурса с PDF-конспектом.
	NotesPDFURI = "moodle://notes/{id}/pdf"

	notesURIPrefix = "moodle://notes/"
)

var (
	// errNoteNotFound — конспекта или выбранного источника нет.
	errNoteNotFound = errors.New("mcpapp: конспект не найден")
	// errNoteBadPath — путь выходит за каталог записей или абсолютный.
	errNoteBadPath = errors.New("mcpapp: недопустимый путь конспекта")
	// errNotePDFUnreadable — PDF есть, но текстового слоя в нём нет.
	errNotePDFUnreadable = errors.New("mcpapp: PDF конспекта без текстового слоя")
	// errNoteBadInput — некорректные аргументы инструмента.
	errNoteBadInput = errors.New("mcpapp: неверные аргументы")
)

// lectureDirRE — имя каталога лекции в архиве записей.
var lectureDirRE = regexp.MustCompile(`^лекция-([0-9]+)$`)

// recordingsDir возвращает каталог записей с учётом значения по умолчанию.
func (s *Session) recordingsDir() string {
	if s != nil {
		if dir := strings.TrimSpace(s.cfg.RecordingsDir); dir != "" {
			return dir
		}
	}
	return DefaultRecordingsDir
}

// NotesInput — аргументы moodle_notes.
type NotesInput struct {
	Action     string `json:"action,omitempty" jsonschema:"list — список конспектов (по умолчанию), read — прочитать один"`
	ID         int64  `json:"id,omitempty" jsonschema:"id конспекта из action=list; обязателен для read"`
	Source     string `json:"source,omitempty" jsonschema:"для read: auto, markdown, pdf, transcript или slides; по умолчанию auto"`
	Discipline string `json:"discipline,omitempty" jsonschema:"фильтр списка по названию дисциплины (подстрока, без учёта регистра)"`
	Query      string `json:"query,omitempty" jsonschema:"поиск по дисциплине, номеру лекции, дате и статусу"`
	Limit      int    `json:"limit,omitempty" jsonschema:"сколько конспектов вернуть: по умолчанию 50, максимум 500"`
	MaxChars   int    `json:"max_chars,omitempty" jsonschema:"для read: сколько рун текста вернуть, максимум 100000"`
}

// NotePack — одна лекция в списке конспектов. Пути наружу не отдаются:
// только URI ресурсов и признаки наличия файлов.
type NotePack struct {
	ID            int64  `json:"id"`
	LessonID      int64  `json:"lesson_id,omitempty"`
	Discipline    string `json:"discipline"`
	Number        int    `json:"number"`
	Date          string `json:"date,omitempty"`
	Status        string `json:"status,omitempty"`
	HasMarkdown   bool   `json:"has_markdown"`
	HasPDF        bool   `json:"has_pdf"`
	HasTranscript bool   `json:"has_transcript"`
	HasSlides     bool   `json:"has_slides"`
	MarkdownURI   string `json:"markdown_uri"`
	PDFURI        string `json:"pdf_uri"`
}

// NotesOutput — ответ moodle_notes: список либо прочитанный текст.
type NotesOutput struct {
	Action     string     `json:"action"`
	Total      int        `json:"total"`
	Packs      []NotePack `json:"packs"`
	ID         int64      `json:"id,omitempty"`
	Discipline string     `json:"discipline,omitempty"`
	Number     int        `json:"number,omitempty"`
	Source     string     `json:"source,omitempty"`
	Text       string     `json:"text,omitempty"`
	Truncated  bool       `json:"truncated,omitempty"`
	Warnings   []string   `json:"warnings,omitempty"`
}

// notePack — внутренняя запись: относительный каталог и метаданные.
type notePack struct {
	id            int64
	lessonID      int64
	discipline    string
	number        int
	date          string
	status        string
	dir           string // относительный путь внутри recordings, через «/»
	hasMarkdown   bool
	hasPDF        bool
	hasTranscript bool
	hasSlides     bool
}

// dbNotePack — строка lecture_packs, нужная конспектам.
type dbNotePack struct {
	id            int64
	lessonID      int64
	discipline    string
	number        int
	date          string
	dir           string
	status        string
	transcript    string
	notesPDF      string
	errText       string
	publishStatus string
}

// registerNoteTools добавляет moodle_notes: локальные конспекты лекций.
func registerNoteTools(s *mcp.Server, sess *Session) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "moodle_notes",
		Title: "Конспекты лекций",
		Description: "Читает локальные конспекты лекций: действие list (по умолчанию) отдаёт список с фильтрами discipline/query, " +
			"действие read — текст одного конспекта (markdown, PDF, расшифровка или слайды). " +
			"Источники — sqlite-таблица lecture_packs и каталог записей; только чтение, пути за пределы каталога записей отклоняются.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in NotesInput) (*mcp.CallToolResult, NotesOutput, error) {
		out, err := notesTool(ctx, sess, in)
		if err != nil {
			return nil, NotesOutput{}, err
		}
		return nil, out, nil
	})
}

// notesTool выбирает действие и приводит ошибки к кодам MCP.
func notesTool(ctx context.Context, sess *Session, in NotesInput) (NotesOutput, error) {
	if err := ctx.Err(); err != nil {
		return NotesOutput{}, noteToolError(err)
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	switch action {
	case "", "list":
		out, err := listNotes(ctx, sess, in)
		if err != nil {
			return NotesOutput{}, noteToolError(err)
		}
		return out, nil
	case "read":
		out, err := readNote(ctx, sess, in)
		if err != nil {
			return NotesOutput{}, noteToolError(err)
		}
		return out, nil
	default:
		return NotesOutput{}, noteToolError(fmt.Errorf("%w: action=%q, ожидается list или read", errNoteBadInput, in.Action))
	}
}

// listNotes собирает конспекты из базы и каталога записей, фильтрует и
// ограничивает выдачу. Сбой базы не роняет список: остаётся сканирование.
func listNotes(ctx context.Context, sess *Session, in NotesInput) (NotesOutput, error) {
	root := sess.recordingsDir()
	packs, warnings := collectNotes(ctx, sess.cfg.DBPath, root)
	filtered := filterNotes(packs, in.Discipline, in.Query)
	total := len(filtered)
	limit := in.Limit
	if limit <= 0 {
		limit = defaultNotesLimit
	}
	if limit > maxNotesLimit {
		limit = maxNotesLimit
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	out := NotesOutput{
		Action:   "list",
		Total:    total,
		Packs:    make([]NotePack, 0, len(filtered)),
		Warnings: warnings,
	}
	for _, p := range filtered {
		out.Packs = append(out.Packs, publicNotePack(p))
	}
	logx.Debugf("mcpapp", "конспекты: найдено %d, отдано %d", total, len(out.Packs))
	return out, nil
}

// readNote читает выбранный источник одного конспекта.
func readNote(ctx context.Context, sess *Session, in NotesInput) (NotesOutput, error) {
	if in.ID == 0 {
		return NotesOutput{}, fmt.Errorf("%w: для read нужен id из action=list", errNoteBadInput)
	}
	source := strings.ToLower(strings.TrimSpace(in.Source))
	switch source {
	case "", "auto":
		source = "auto"
	case "markdown", "pdf", "transcript", "slides":
	default:
		return NotesOutput{}, fmt.Errorf("%w: source=%q, ожидается auto, markdown, pdf, transcript или slides", errNoteBadInput, in.Source)
	}
	root := sess.recordingsDir()
	packs, warnings := collectNotes(ctx, sess.cfg.DBPath, root)
	pack, ok := findNote(packs, in.ID)
	if !ok {
		return NotesOutput{}, fmt.Errorf("%w: id=%d", errNoteNotFound, in.ID)
	}
	maxChars := in.MaxChars
	if maxChars <= 0 || maxChars > maxToolOutputRunes {
		maxChars = maxToolOutputRunes
	}
	text, used, truncated, err := readNoteText(root, pack, source, maxChars)
	if err != nil {
		return NotesOutput{}, err
	}
	return NotesOutput{
		Action:     "read",
		Total:      1,
		Packs:      []NotePack{publicNotePack(pack)},
		ID:         pack.id,
		Discipline: pack.discipline,
		Number:     pack.number,
		Source:     used,
		Text:       text,
		Truncated:  truncated,
		Warnings:   warnings,
	}, nil
}

// publicNotePack превращает внутреннюю запись в MCP-ответ.
func publicNotePack(p notePack) NotePack {
	return NotePack{
		ID:            p.id,
		LessonID:      p.lessonID,
		Discipline:    p.discipline,
		Number:        p.number,
		Date:          p.date,
		Status:        p.status,
		HasMarkdown:   p.hasMarkdown,
		HasPDF:        p.hasPDF,
		HasTranscript: p.hasTranscript,
		HasSlides:     p.hasSlides,
		MarkdownURI:   noteURI(p.id, "markdown"),
		PDFURI:        noteURI(p.id, "pdf"),
	}
}

// noteURI собирает адрес ресурса конспекта.
func noteURI(id int64, kind string) string {
	return notesURIPrefix + strconv.FormatInt(id, 10) + "/" + kind
}

// filterNotes применяет фильтры discipline/query без учёта регистра.
func filterNotes(packs []notePack, discipline, query string) []notePack {
	discipline = strings.ToLower(strings.TrimSpace(discipline))
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]notePack, 0, len(packs))
	for _, p := range packs {
		haystack := strings.ToLower(strings.Join([]string{
			p.discipline, "лекция " + strconv.Itoa(p.number), strconv.Itoa(p.number),
			p.date, p.status,
		}, " "))
		if discipline != "" && !strings.Contains(strings.ToLower(p.discipline), discipline) {
			continue
		}
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// findNote ищет конспект по id.
func findNote(packs []notePack, id int64) (notePack, bool) {
	for _, p := range packs {
		if p.id == id {
			return p, true
		}
	}
	return notePack{}, false
}

// collectNotes объединяет строки lecture_packs и каталоги записей. Пак из базы
// приоритетнее: у него есть дата и статус. Ошибка базы — предупреждение, а не
// отказ: сканирование продолжает работать.
func collectNotes(ctx context.Context, dbPath, root string) ([]notePack, []string) {
	var warnings []string
	rows, err := loadNotePacks(ctx, dbPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, []string{"база конспектов не прочитана: " + ctx.Err().Error()}
		}
		logx.Warnf("mcpapp", "конспекты: база не прочитана: %v", err)
		warnings = append(warnings, "база конспектов не прочитана, используется только каталог записей")
	}
	packs := make([]notePack, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		p := notePack{
			id:         row.id,
			lessonID:   row.lessonID,
			discipline: strings.TrimSpace(row.discipline),
			number:     row.number,
			date:       row.date,
			status:     row.status,
			dir:        strings.TrimSpace(row.dir),
		}
		if p.dir == "" {
			p.dir = filepath.ToSlash(archive.Rel(p.discipline, p.number))
		}
		seen[noteRelKey(p.dir)] = true
		enrichNote(&p, root)
		packs = append(packs, p)
	}
	for _, scan := range scanNoteDirs(root) {
		if seen[noteRelKey(scan.dir)] {
			continue
		}
		p := notePack{
			id:         scanNoteID(scan.dir),
			discipline: scan.discipline,
			number:     scan.number,
			dir:        scan.dir,
		}
		enrichNote(&p, root)
		packs = append(packs, p)
	}
	sort.SliceStable(packs, func(i, j int) bool {
		if packs[i].discipline != packs[j].discipline {
			return packs[i].discipline < packs[j].discipline
		}
		if packs[i].number != packs[j].number {
			return packs[i].number < packs[j].number
		}
		return packs[i].id < packs[j].id
	})
	return packs, warnings
}

// loadNotePacks читает lecture_packs строго на чтение. Нет файла или таблицы —
// это пустой список без ошибки: на машине может не быть бота.
func loadNotePacks(ctx context.Context, dbPath string) ([]dbNotePack, error) {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return nil, nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(8000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	defer func() {
		if cerr := db.Close(); cerr != nil {
			logx.Warnf("mcpapp", "конспекты: закрыть базу: %v", cerr)
		}
	}()
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(lesson_id, 0), COALESCE(discipline, ''), `+
		`COALESCE(number, 0), COALESCE(date, ''), COALESCE(dir, ''), COALESCE(status, ''), `+
		`COALESCE(transcript, ''), COALESCE(notes_pdf, ''), COALESCE(err, ''), `+
		`COALESCE(publish_status, '') FROM lecture_packs`)
	if err != nil {
		if isMissingTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []dbNotePack
	for rows.Next() {
		var p dbNotePack
		if err := rows.Scan(&p.id, &p.lessonID, &p.discipline, &p.number, &p.date, &p.dir,
			&p.status, &p.transcript, &p.notesPDF, &p.errText, &p.publishStatus); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// isMissingTable сообщает, что запрос упал из-за отсутствующей таблицы.
func isMissingTable(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "does not exist")
}

// noteScan — каталог лекции, найденный сканированием.
type noteScan struct {
	dir        string
	discipline string
	number     int
}

// scanNoteDirs ищет <каталог записей>/<дисциплина>/лекция-N. Ошибки чтения
// каталогов игнорируются: неполный архив не должен ломать список.
func scanNoteDirs(root string) []noteScan {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "recordings"
	}
	disciplines, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []noteScan
	for _, d := range disciplines {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		base := filepath.Join(root, d.Name())
		lectures, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, l := range lectures {
			if !l.IsDir() {
				continue
			}
			m := lectureDirRE.FindStringSubmatch(l.Name())
			if m == nil {
				continue
			}
			n, err := strconv.Atoi(m[1])
			if err != nil || n < 1 {
				continue
			}
			out = append(out, noteScan{
				dir:        filepath.ToSlash(filepath.Join(d.Name(), l.Name())),
				discipline: d.Name(),
				number:     n,
			})
		}
	}
	return out
}

// scanNoteID даёт стабильный отрицательный id каталогу, которого нет в базе.
// Коллизия FNV-64 практически исключена; отрицательный диапазон не пересекается
// с AUTOINCREMENT id из lecture_packs.
func scanNoteID(rel string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(rel))
	id := int64(h.Sum64() & 0x7fffffffffffffff)
	if id == 0 {
		id = 1
	}
	return -id
}

// enrichNote выставляет признаки файлов и нормализует относительный путь.
func enrichNote(p *notePack, root string) {
	p.dir = filepath.ToSlash(strings.TrimSpace(p.dir))
	p.hasMarkdown = noteFileExists(root, p.dir, "notes.md")
	p.hasPDF = noteFileExists(root, p.dir, "notes.pdf")
	p.hasTranscript = noteFileExists(root, p.dir, "transcript.txt")
	p.hasSlides = noteFileExists(root, p.dir, "slides/slides.json") ||
		noteSlidesExist(root, p.dir)
}

// noteFileExists проверяет обычный файл внутри каталога записей.
func noteFileExists(root, rel, name string) bool {
	abs, err := noteFilePath(root, rel, name)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	return err == nil && info.Mode().IsRegular()
}

// noteSlidesExist проверяет картинки слайдов, даже если slides.json нет.
func noteSlidesExist(root, rel string) bool {
	abs, err := noteFilePath(root, rel, "slides")
	if err != nil {
		return false
	}
	return archive.HasSlides(abs)
}

// noteFilePath безопасно собирает путь к файлу конспекта. Абсолютные пути и
// «..» отклоняются до archive.UnderRoot, а существующий путь дополнительно
// проверяется на symlink-побег: EvalSymlinks не должен вывести за каталог
// записей.
func noteFilePath(root, rel, name string) (string, error) {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if !safeNoteRel(rel) {
		return "", fmt.Errorf("%w: %q", errNoteBadPath, rel)
	}
	target := path.Join(rel, filepath.ToSlash(name))
	abs, err := archive.UnderRoot(root, target)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errNoteBadPath, err)
	}
	if err := checkNoteSymlinks(root, abs); err != nil {
		return "", err
	}
	return abs, nil
}

// checkNoteSymlinks проверяет, что существующий путь после раскрытия симлинков
// остаётся внутри каталога записей. Несуществующий файл проверять нечего:
// чтение всё равно вернёт not_found, а не содержимое.
func checkNoteSymlinks(root, abs string) error {
	if _, err := os.Lstat(abs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	evalTarget, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("%w: не удалось раскрыть симлинки", errNoteBadPath)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("%w: не удалось разрешить каталог записей", errNoteBadPath)
	}
	evalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return fmt.Errorf("%w: не удалось раскрыть каталог записей", errNoteBadPath)
	}
	rel, err := filepath.Rel(evalRoot, evalTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: путь вне каталога записей", errNoteBadPath)
	}
	return nil
}

// safeNoteRel проверяет относительный путь каталога конспекта.
func safeNoteRel(rel string) bool {
	if rel == "" || rel == "." || rel == ".." {
		return false
	}
	if filepath.IsAbs(rel) || path.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return false
	}
	if strings.ContainsRune(rel, 0) || strings.Contains(rel, `\`) {
		return false
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// noteRelKey нормализует путь для сравнения базы и сканирования.
func noteRelKey(rel string) string {
	return path.Clean(filepath.ToSlash(strings.TrimSpace(rel)))
}

// readNoteText читает источник конспекта. auto пробует markdown, PDF,
// расшифровку и слайды по порядку.
func readNoteText(root string, p notePack, source string, maxChars int) (string, string, bool, error) {
	order := []string{source}
	if source == "auto" {
		order = []string{"markdown", "pdf", "transcript", "slides"}
	}
	var firstErr error
	for _, kind := range order {
		text, ok, err := readNoteSource(root, p, kind)
		if err == nil && ok {
			text, truncated := TruncateText(text, maxChars)
			return text, kind, truncated, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		// Битый путь — всегда повод остановиться. Нечитаемый PDF фатален
		// только при явном source=pdf: в auto при наличии transcript или
		// slides стоит попробовать следующий источник.
		if errors.Is(err, errNoteBadPath) {
			return "", "", false, err
		}
		if errors.Is(err, errNotePDFUnreadable) && source != "auto" {
			return "", "", false, err
		}
	}
	if firstErr != nil && !errors.Is(firstErr, errNoteNotFound) {
		return "", "", false, firstErr
	}
	return "", "", false, fmt.Errorf("%w: у лекции %q нет notes.md, notes.pdf, transcript.txt или slides.json",
		errNoteNotFound, archive.Label(p.discipline, p.number))
}

// readNoteSource читает один конкретный источник. ok=false без ошибки значит
// «файла нет» — это позволяет auto перейти к следующему.
func readNoteSource(root string, p notePack, kind string) (string, bool, error) {
	name := ""
	switch kind {
	case "markdown":
		name = "notes.md"
	case "pdf":
		name = "notes.pdf"
	case "transcript":
		name = "transcript.txt"
	case "slides":
		return readNoteSlidesSource(root, p)
	default:
		return "", false, fmt.Errorf("%w: source=%q", errNoteBadInput, kind)
	}
	abs, err := noteFilePath(root, p.dir, name)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, nil
	}
	if kind == "pdf" {
		if info.Size() > maxNotePDFBytes {
			return "", false, fmt.Errorf("%w: notes.pdf больше %d МБ", errNotePDFUnreadable, maxNotePDFBytes>>20)
		}
	}
	limit := int64(maxNoteTextBytes)
	if kind == "pdf" {
		limit = maxNotePDFBytes
	}
	data, err := readLimited(abs, limit)
	if err != nil {
		return "", false, err
	}
	if kind == "pdf" {
		text := strings.TrimSpace(moodlematerial.ExtractFileText(data, "application/pdf"))
		if text == "" {
			return "", false, fmt.Errorf("%w: notes.pdf лекции %q", errNotePDFUnreadable,
				archive.Label(p.discipline, p.number))
		}
		return text, true, nil
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", false, nil
	}
	return text, true, nil
}

// readNoteSlidesSource читает slides.json, а если манифеста нет — отдаёт
// список файлов слайдов. Так has_slides=true всегда имеет читаемый source.
func readNoteSlidesSource(root string, p notePack) (string, bool, error) {
	abs, err := noteFilePath(root, p.dir, "slides/slides.json")
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(abs)
	switch {
	case err == nil && info.Mode().IsRegular():
		data, rerr := readLimited(abs, maxNoteTextBytes)
		if rerr != nil {
			return "", false, rerr
		}
		if text := strings.TrimSpace(string(data)); text != "" {
			return text, true, nil
		}
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", false, err
	}
	dir, err := noteFilePath(root, p.dir, "slides")
	if err != nil {
		return "", false, err
	}
	names, err := noteSlideImages(dir)
	if err != nil {
		return "", false, err
	}
	if len(names) == 0 {
		return "", false, nil
	}
	var b strings.Builder
	b.WriteString("slides.json нет. Файлы слайдов:")
	for _, name := range names {
		b.WriteString("\n- ")
		b.WriteString(name)
	}
	return b.String(), true, nil
}

// noteSlideImages возвращает имена картинок слайдов в стабильном порядке.
func noteSlideImages(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !isNoteSlideImage(e.Name()) {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// isNoteSlideImage совпадает с набором расширений archive.HasSlides.
func isNoteSlideImage(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") ||
		strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".webp")
}

// readLimited читает не больше limit+1 байт: лишний байт нужен только чтобы
// не тянуть в память огромный файл целиком.
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()
	return io.ReadAll(io.LimitReader(f, limit+1))
}

// noteToolError добавляет к ошибке код и действие для LLM.
func noteToolError(err error) error {
	code := "notes_error"
	switch {
	case errors.Is(err, errNoteBadInput):
		code = "bad_input"
	case errors.Is(err, errNoteBadPath):
		code = "bad_path"
	case errors.Is(err, errNoteNotFound):
		code = "not_found"
	case errors.Is(err, errNotePDFUnreadable):
		code = "pdf_unreadable"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	}
	return toolFailure(code, "проверьте id из action=list и наличие файлов конспекта", err)
}

// registerNoteResources отдаёт markdown и PDF конспектов как MCP-ресурсы.
func registerNoteResources(s *mcp.Server, sess *Session) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: NotesMarkdownURI,
		Name:        "notes-markdown",
		Title:       "Конспект лекции (markdown)",
		Description: "Markdown-конспект лекции по id из moodle_notes. Если notes.md нет, отдаётся текст из notes.pdf с пометкой.",
		MIMEType:    "text/markdown",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return readNoteMarkdownResource(ctx, sess, req)
	})
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: NotesPDFURI,
		Name:        "notes-pdf",
		Title:       "Конспект лекции (PDF)",
		Description: "PDF-конспект лекции по id из moodle_notes, не больше 25 МБ.",
		MIMEType:    "application/pdf",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return readNotePDFResource(ctx, sess, req)
	})
}

// readNoteMarkdownResource отдаёт notes.md, а при его отсутствии — текст из PDF.
func readNoteMarkdownResource(ctx context.Context, sess *Session, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri, id, kind, err := parseNoteURI(req)
	if err != nil {
		return nil, err
	}
	if kind != "markdown" {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	pack, err := lookupNote(ctx, sess, id)
	if err != nil {
		if errors.Is(err, errNoteNotFound) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return nil, err
	}
	root := sess.recordingsDir()
	text, _, _, err := readNoteText(root, pack, "markdown", maxToolOutputRunes)
	if err == nil {
		return noteTextResource(uri, "text/markdown", text), nil
	}
	if !errors.Is(err, errNoteNotFound) {
		return nil, err
	}
	text, _, _, err = readNoteText(root, pack, "pdf", maxToolOutputRunes)
	if err != nil {
		if errors.Is(err, errNoteNotFound) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return nil, err
	}
	note := "_notes.md нет: текст извлечён из notes.pdf, разметка недоступна._\n\n"
	return noteTextResource(uri, "text/markdown", note+text), nil
}

// readNotePDFResource отдаёт PDF как Blob с потолком 25 МБ.
func readNotePDFResource(ctx context.Context, sess *Session, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri, id, kind, err := parseNoteURI(req)
	if err != nil {
		return nil, err
	}
	if kind != "pdf" {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	pack, err := lookupNote(ctx, sess, id)
	if err != nil {
		if errors.Is(err, errNoteNotFound) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return nil, err
	}
	abs, err := noteFilePath(sess.recordingsDir(), pack.dir, "notes.pdf")
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return nil, err
	}
	if info.Size() > maxNotePDFBytes {
		return nil, fmt.Errorf("%w: notes.pdf больше %d МБ", errNotePDFUnreadable, maxNotePDFBytes>>20)
	}
	data, err := readLimited(abs, maxNotePDFBytes)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/pdf",
			Blob:     data,
		}},
	}, nil
}

// lookupNote находит конспект по id для ресурса.
func lookupNote(ctx context.Context, sess *Session, id int64) (notePack, error) {
	packs, _ := collectNotes(ctx, sess.cfg.DBPath, sess.recordingsDir())
	pack, ok := findNote(packs, id)
	if !ok {
		return notePack{}, fmt.Errorf("%w: id=%d", errNoteNotFound, id)
	}
	return pack, nil
}

// parseNoteURI разбирает moodle://notes/{id}/{kind} и проверяет шаблон.
func parseNoteURI(req *mcp.ReadResourceRequest) (uri string, id int64, kind string, err error) {
	if req == nil || req.Params == nil {
		return "", 0, "", fmt.Errorf("%w: пустой uri", errNoteBadInput)
	}
	uri = req.Params.URI
	if !strings.HasPrefix(uri, notesURIPrefix) {
		return uri, 0, "", mcp.ResourceNotFoundError(uri)
	}
	rest := strings.TrimPrefix(uri, notesURIPrefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return uri, 0, "", mcp.ResourceNotFoundError(uri)
	}
	id, perr := strconv.ParseInt(parts[0], 10, 64)
	if perr != nil {
		return uri, 0, "", mcp.ResourceNotFoundError(uri)
	}
	return uri, id, parts[1], nil
}

// noteTextResource собирает текстовый ресурс.
func noteTextResource(uri, mime, text string) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: mime,
			Text:     text,
		}},
	}
}
