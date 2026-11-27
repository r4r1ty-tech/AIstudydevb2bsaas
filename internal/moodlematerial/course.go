package moodlematerial

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

const (
	// DefaultMaterialMaxChars — сколько рун материала отдаём по умолчанию.
	DefaultMaterialMaxChars = 20000
	// MaxMaterialMaxChars — общий потолок одного ответа с текстом материала.
	MaxMaterialMaxChars = 100000
)

// ErrKindRequired — тип материала неизвестен, а курс не указан: определять его
// перебором всех /mod/* запрещено.
var ErrKindRequired = errors.New("moodlematerial: не указан тип материала (kind) или course_id")

// ErrModuleNotFound — модуль не найден в структуре курса.
var ErrModuleNotFound = errors.New("moodlematerial: модуль не найден в курсе")

// Options — границы одного сбора текста курса. Нулевые поля заменяются
// значениями по умолчанию, как в старом collectMoodleCourseText.
type Options struct {
	MaxChars       int // общий лимит рун
	FileMaxChars   int // лимит текста одного файла
	MaxPages       int // сколько страниц-лекций открыть
	MaxResources   int // сколько файлов-ресурсов разобрать
	MaxFolders     int // сколько папок открыть
	FolderMaxFiles int // сколько файлов взять из папки
}

// DefaultCourseOptions — поведение старого collectMoodleCourseText: 16k рун,
// три страницы, три файла, три папки по три файла.
func DefaultCourseOptions() Options {
	return Options{
		MaxChars:       DefaultCourseTextMaxChars,
		FileMaxChars:   FileMaxChars,
		MaxPages:       3,
		MaxResources:   MaxResources,
		MaxFolders:     MaxFolders,
		FolderMaxFiles: FolderMaxFiles,
	}
}

func (o Options) withDefaults() Options {
	def := DefaultCourseOptions()
	if o.MaxChars <= 0 {
		o.MaxChars = def.MaxChars
	}
	if o.FileMaxChars <= 0 {
		o.FileMaxChars = def.FileMaxChars
	}
	if o.MaxPages <= 0 {
		o.MaxPages = def.MaxPages
	}
	if o.MaxResources <= 0 {
		o.MaxResources = def.MaxResources
	}
	if o.MaxFolders <= 0 {
		o.MaxFolders = def.MaxFolders
	}
	if o.FolderMaxFiles <= 0 {
		o.FolderMaxFiles = def.FolderMaxFiles
	}
	return o
}

// CollectCourseText — лёгкий фолбэк, когда локальных конспектов нет: читает
// текст секций курса, страницы-лекции (modtype_page), файлы-ресурсы
// (modtype_resource) и файлы из папок (modtype_folder) Moodle.
// Ошибки отдельных страниц и файлов не валят сбор: они только логируются.
func CollectCourseText(ctx context.Context, client *moodle.Client, courseID int64, opts Options) (string, error) {
	if client == nil || courseID == 0 {
		return "", nil
	}
	opts = opts.withDefaults()
	doc, _, err := client.PageWithURL(ctx, fmt.Sprintf("/course/view.php?id=%d", courseID))
	if err != nil {
		return "", err
	}
	mb := NewBuilder(opts.MaxChars)
	doc.Find("li.section, li.course-section").EachWithBreak(func(_ int, sec *goquery.Selection) bool {
		title := CleanText(sec.Find(".sectionname, h3").First().Text())
		if title == "" {
			title = "секция курса"
		}
		sec.Find("script, style, noscript").Remove()
		return mb.Add("Курс, "+title, sec.Text())
	})
	pages := 0
	doc.Find("li.activity.modtype_page a.activityname, li.activity.modtype_page a.activityinstance").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		if pages >= opts.MaxPages || mb.Full() {
			return false
		}
		href := strings.TrimSpace(a.AttrOr("href", ""))
		if href == "" {
			return true
		}
		pages++
		page, _, perr := client.PageWithURL(ctx, href)
		if perr != nil {
			logx.Debugf("moodlematerial", "курс %d: страница %s: %v", courseID, href, perr)
			return true
		}
		page.Find("script, style, nav, header, footer, noscript").Remove()
		text := CleanText(page.Find("#region-main, .course-content").First().Text())
		return mb.Add("Лекция Moodle: "+CleanText(a.Text()), text)
	})
	resources := 0
	doc.Find("li.activity.modtype_resource").EachWithBreak(func(_ int, act *goquery.Selection) bool {
		if resources >= opts.MaxResources || mb.Full() {
			return false
		}
		a := act.Find("a.activityname, a.activityinstance").First()
		href := strings.TrimSpace(a.AttrOr("href", ""))
		if href == "" {
			return true
		}
		resources++
		label := "Файл Moodle: " + CleanText(a.Text())
		return appendFile(ctx, client, mb, label, href, opts.FileMaxChars)
	})
	folders := 0
	doc.Find("li.activity.modtype_folder").EachWithBreak(func(_ int, act *goquery.Selection) bool {
		if folders >= opts.MaxFolders || mb.Full() {
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
		name := CleanText(a.Text())
		if name == "" {
			name = "папка"
		}
		return collectFolder(ctx, client, mb, name, href, opts)
	})
	res := mb.String()
	return res, nil
}

// collectFolder открывает страницу папки Moodle и добавляет текст первых
// файлов. Возвращает false при исчерпании лимита.
func collectFolder(ctx context.Context, client *moodle.Client, mb *Builder, name, href string, opts Options) bool {
	doc, _, err := client.PageWithURL(ctx, href)
	if err != nil {
		logx.Debugf("moodlematerial", "папка %s: %v", href, err)
		return true
	}
	links := make([]string, 0, opts.FolderMaxFiles)
	seen := map[string]bool{}
	doc.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		if len(links) >= opts.FolderMaxFiles {
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
		if mb.Full() {
			return false
		}
		label := fmt.Sprintf("Папка Moodle: %s / %s", name, FileNameFromURL(src))
		if !appendFile(ctx, client, mb, label, src, opts.FileMaxChars) {
			return false
		}
	}
	return true
}

// appendFile скачивает файл Moodle и добавляет извлечённый текст.
// Любая ошибка означает «без этого файла», а не сбой сбора материалов.
func appendFile(ctx context.Context, client *moodle.Client, mb *Builder, label, src string, maxChars int) bool {
	data, contentType, err := client.FetchFile(ctx, src)
	if err != nil {
		logx.Debugf("moodlematerial", "файл %s: %v", src, err)
		return !mb.Full()
	}
	text := ExtractFileText(data, contentType)
	if text == "" {
		logx.Debugf("moodlematerial", "из файла %s текст не извлечён", src)
		return !mb.Full()
	}
	return mb.Add(label, ClipRunes(text, maxChars))
}

// ReadRequest — что прочитать из конкретного модуля Moodle.
type ReadRequest struct {
	CMID         int64
	CourseID     int64
	Kind         string
	MaxChars     int
	IncludeFiles bool // для папки: читать текст первых файлов
	AllChapters  bool // для книги: обойти главы, а не только текущую
	Module       *moodle.CourseModule
}

// Material — прочитанный материал в терминах MCP-ответа.
type Material struct {
	CMID      int64
	CourseID  int64
	Kind      string
	Name      string
	Section   string
	URL       string
	Text      string
	Status    string
	Grade     string
	Due       string
	Info      []string
	Files     []moodle.FolderEntry
	Chapters  []moodle.BookChapter
	Truncated bool
	Next      string
}

// ReadMaterial читает материал: страница, файл, папка, ссылка и урок
// открываются одним GET, книга при all_chapters обходит несколько глав.
// Если kind не задан, сначала читается outline курса, чтобы определить тип.
// Попытки тестов не начинаются: ни startattempt, ни submit не вызываются.
func ReadMaterial(ctx context.Context, client *moodle.Client, req ReadRequest) (Material, error) {
	if client == nil {
		return Material{}, errors.New("moodlematerial: нет клиента Moodle")
	}
	if req.CMID <= 0 {
		return Material{}, fmt.Errorf("moodlematerial: некорректный cmid=%d", req.CMID)
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	mod := req.Module
	courseID := req.CourseID

	if kind == "" {
		if courseID <= 0 {
			return Material{}, ErrKindRequired
		}
		resolved, err := ResolveModule(ctx, client, req.CMID, courseID)
		if err != nil {
			return Material{}, err
		}
		mod, kind = &resolved, resolved.Kind
	} else if mod == nil && courseID > 0 {
		if resolved, err := ResolveModule(ctx, client, req.CMID, courseID); err == nil {
			mod = &resolved
		} else {
			logx.Debugf("moodlematerial", "cmid=%d: структура курса %d не дала модуль: %v", req.CMID, courseID, err)
		}
	}
	if mod != nil {
		if courseID == 0 {
			courseID = mod.CourseID
		}
	}
	if kind == "" {
		return Material{}, ErrKindRequired
	}
	mat := Material{CMID: req.CMID, CourseID: courseID, Kind: kind}
	if mod != nil {
		mat.Name, mat.Section, mat.URL = mod.Name, mod.Section, mod.URL
	}

	var err error
	switch kind {
	case "page":
		err = readPage(ctx, client, req.CMID, &mat)
	case "resource":
		err = readResource(ctx, client, req.CMID, kind, mod, &mat)
	case "folder":
		err = readFolder(ctx, client, req.CMID, req.IncludeFiles, &mat)
	case "book":
		err = readBook(ctx, client, req.CMID, req.AllChapters, &mat)
	case "url":
		err = readURL(ctx, client, req.CMID, &mat)
	case "lesson":
		err = readLesson(ctx, client, req.CMID, &mat)
	case "assign", "quiz", "workshop":
		err = readActivity(ctx, client, req.CMID, &mat)
	default:
		return Material{}, fmt.Errorf("moodlematerial: тип %q не поддерживается: укажите page, resource, folder, book, url, lesson, assign, quiz или workshop", kind)
	}
	if err != nil {
		return Material{}, err
	}

	mat.Text = strings.TrimSpace(mat.Text)
	limit := req.MaxChars
	if limit <= 0 {
		limit = DefaultMaterialMaxChars
	}
	if limit > MaxMaterialMaxChars {
		limit = MaxMaterialMaxChars
	}
	if RuneLen(mat.Text) > limit {
		mat.Text = ClipRunes(mat.Text, limit)
		mat.Truncated = true
		if limit >= MaxMaterialMaxChars {
			mat.Next = fmt.Sprintf("текст усечён: достигнут предел %d рун", MaxMaterialMaxChars)
		} else {
			next := limit * 2
			if next > MaxMaterialMaxChars {
				next = MaxMaterialMaxChars
			}
			mat.Next = fmt.Sprintf("текст усечён: повторите с max_chars=%d", next)
		}
	}
	return mat, nil
}

// ResolveModule находит модуль по cmid в структуре курса. Если курс не задан,
// просматриваются страницы курсов студента (не /mod/*): так тип определяется
// без перебора всех возможных плагинов.
func ResolveModule(ctx context.Context, client *moodle.Client, cmid, courseID int64) (moodle.CourseModule, error) {
	if client == nil {
		return moodle.CourseModule{}, errors.New("moodlematerial: нет клиента Moodle")
	}
	if cmid <= 0 {
		return moodle.CourseModule{}, fmt.Errorf("moodlematerial: некорректный cmid=%d", cmid)
	}
	if courseID > 0 {
		return moduleFromCourse(ctx, client, cmid, moodle.Course{ID: courseID})
	}
	courses, err := client.Courses(ctx)
	if err != nil {
		return moodle.CourseModule{}, fmt.Errorf("moodlematerial: курсы для поиска cmid=%d: %w", cmid, err)
	}
	for _, cr := range courses {
		if m, err := moduleFromCourse(ctx, client, cmid, cr); err == nil {
			return m, nil
		}
	}
	return moodle.CourseModule{}, fmt.Errorf("%w: cmid=%d", ErrModuleNotFound, cmid)
}

func moduleFromCourse(ctx context.Context, client *moodle.Client, cmid int64, course moodle.Course) (moodle.CourseModule, error) {
	outline, err := client.CourseOutline(ctx, course)
	if err != nil {
		return moodle.CourseModule{}, err
	}
	for _, sec := range outline.Sections {
		for _, m := range sec.Modules {
			if m.CMID == cmid {
				if m.CourseID == 0 {
					m.CourseID = course.ID
				}
				return m, nil
			}
		}
	}
	return moodle.CourseModule{}, fmt.Errorf("%w: cmid=%d, курс %d", ErrModuleNotFound, cmid, course.ID)
}

func readPage(ctx context.Context, client *moodle.Client, cmid int64, mat *Material) error {
	p, err := client.PageContent(ctx, cmid)
	if err != nil {
		return err
	}
	if mat.Name == "" {
		mat.Name = p.Name
	}
	mat.Text = p.Content
	if mat.Text == "" {
		mat.Text = p.Intro
	}
	mat.Files = p.Files
	return nil
}

func readResource(ctx context.Context, client *moodle.Client, cmid int64, kind string, mod *moodle.CourseModule, mat *Material) error {
	src := mat.URL
	if src == "" {
		src = fmt.Sprintf("/mod/%s/view.php?id=%d", kind, cmid)
	}
	data, contentType, err := client.FetchFile(ctx, src)
	if err != nil {
		return err
	}
	text := ExtractFileText(data, contentType)
	if text == "" {
		return fmt.Errorf("moodlematerial: из файла cmid=%d текст не извлечён (возможно, это скан или двоичный формат)", cmid)
	}
	mat.Text = CleanText(text)
	if mod != nil {
		mat.Files = moduleFiles(mod)
	}
	return nil
}

func readFolder(ctx context.Context, client *moodle.Client, cmid int64, includeFiles bool, mat *Material) error {
	files, err := client.FolderFiles(ctx, cmid)
	if err != nil {
		return err
	}
	mat.Files = files
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, entryPath(f))
	}
	if !includeFiles {
		mat.Text = strings.Join(names, "\n")
		return nil
	}
	mb := NewBuilder(DefaultMaterialMaxChars)
	for i, f := range files {
		if i >= FolderMaxFiles || mb.Full() {
			break
		}
		data, contentType, ferr := client.FetchFile(ctx, f.URL)
		if ferr != nil {
			logx.Debugf("moodlematerial", "папка cmid=%d: файл %s: %v", cmid, f.URL, ferr)
			continue
		}
		text := ExtractFileText(data, contentType)
		if text == "" {
			continue
		}
		label := fmt.Sprintf("Папка Moodle: %s / %s", fallbackName(mat.Name, "папка"), f.Name)
		mb.Add(label, ClipRunes(text, FileMaxChars))
	}
	mat.Text = mb.String()
	if mat.Text == "" {
		mat.Text = strings.Join(names, "\n")
	}
	return nil
}

func readBook(ctx context.Context, client *moodle.Client, cmid int64, allChapters bool, mat *Material) error {
	var (
		b   moodle.BookContent
		err error
	)
	if allChapters {
		b, err = client.BookContentAll(ctx, cmid, 50)
	} else {
		b, err = client.BookContent(ctx, cmid)
	}
	if err != nil {
		return err
	}
	if mat.Name == "" {
		mat.Name = b.Name
	}
	mat.Chapters = b.Chapters
	mat.Text = b.Content
	if mat.Text == "" {
		mat.Text = b.Intro
	}
	if !allChapters {
		mat.Next = nextChapterHint(b)
	}
	return nil
}

func readURL(ctx context.Context, client *moodle.Client, cmid int64, mat *Material) error {
	u, err := client.URLContent(ctx, cmid)
	if err != nil {
		return err
	}
	if mat.Name == "" {
		mat.Name = u.Name
	}
	if u.URL == "" {
		return fmt.Errorf("moodlematerial: у ссылки cmid=%d не найден целевой адрес", cmid)
	}
	// На внешний хост не переходим: отдаём только сам адрес.
	mat.Text = u.URL
	if u.Intro != "" {
		mat.Info = append(mat.Info, u.Intro)
	}
	return nil
}

func readLesson(ctx context.Context, client *moodle.Client, cmid int64, mat *Material) error {
	l, err := client.LessonContent(ctx, cmid)
	if err != nil {
		return err
	}
	if mat.Name == "" {
		mat.Name = l.Name
	}
	mat.Status, mat.Grade = l.Status, l.Grade
	mat.Info = l.Info
	parts := make([]string, 0, 2+len(l.Info))
	if l.Intro != "" {
		parts = append(parts, l.Intro)
	}
	if l.Content != "" {
		parts = append(parts, l.Content)
	}
	parts = append(parts, l.Info...)
	mat.Text = strings.Join(parts, "\n")
	return nil
}

// readActivity читает только страницу просмотра задания/теста/семинара:
// описание, статус и сроки. Никаких startattempt/submit.
func readActivity(ctx context.Context, client *moodle.Client, cmid int64, mat *Material) error {
	act := moodle.Activity{
		CMID:     cmid,
		CourseID: mat.CourseID,
		Kind:     mat.Kind,
		Name:     mat.Name,
		URL:      mat.URL,
	}
	d, err := client.ActivityDetail(ctx, act, time.Local)
	if err != nil {
		return err
	}
	mat.Status = d.Status
	if d.Due != nil {
		mat.Due = d.Due.Format("02.01.2006 15:04")
	}
	mat.Text = strings.Join(d.Todo, "\n")
	return nil
}

func moduleFiles(mod *moodle.CourseModule) []moodle.FolderEntry {
	out := make([]moodle.FolderEntry, 0, len(mod.Files))
	seen := map[string]bool{}
	for _, src := range mod.Files {
		if src == "" || seen[src] {
			continue
		}
		seen[src] = true
		out = append(out, moodle.FolderEntry{Name: FileNameFromURL(src), URL: src})
	}
	return out
}

func entryPath(f moodle.FolderEntry) string {
	if f.Folder == "" {
		return f.Name
	}
	return f.Folder + "/" + f.Name
}

func fallbackName(name, def string) string {
	if strings.TrimSpace(name) == "" {
		return def
	}
	return name
}

func nextChapterHint(b moodle.BookContent) string {
	for i, ch := range b.Chapters {
		if ch.ID != b.CurrentChapterID || i+1 >= len(b.Chapters) {
			continue
		}
		next := b.Chapters[i+1]
		return fmt.Sprintf("следующая глава: %d. %s (chapterid=%d)", next.Number, next.Title, next.ID)
	}
	return ""
}
