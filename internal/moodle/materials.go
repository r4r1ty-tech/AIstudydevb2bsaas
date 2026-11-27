package moodle

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// maxMaterialPage — потолок чтения HTML-страницы материала (как в websess).
const maxMaterialPage = 8 << 20

// CourseOutline — полная структура курса: секции и все модули любого типа.
type CourseOutline struct {
	CourseID int64
	Course   string
	Sections []CourseSection
}

// CourseSection — секция курса (тема/дата) с модулями.
type CourseSection struct {
	ID      int64
	Num     int
	Name    string
	Summary string
	Hidden  bool
	Modules []CourseModule
}

// CourseModule — любой элемент курса (assign, quiz, url, lesson, forum, folder,
// resource, page, book, workshop и т.п.).
type CourseModule struct {
	CMID             int64
	CourseID         int64
	Section          string
	SectionNum       int
	Kind             string
	Name             string
	URL              string
	Description      string
	Available        bool
	AvailabilityNote string
	// Files — ссылки pluginfile.php, найденные в модуле (вложения и рисунки).
	Files []string
}

// FolderEntry — файл из папки или вложения страницы.
type FolderEntry struct {
	Name   string // имя файла (подпись ссылки либо имя из URL)
	Folder string // подпапка внутри папки, если есть
	URL    string
}

// PageContent — страница курса (/mod/page/view.php).
type PageContent struct {
	CMID    int64
	Name    string
	Intro   string
	Content string
	Files   []FolderEntry
}

// BookChapter — глава книги (/mod/book/view.php?chapterid=…).
type BookChapter struct {
	ID      int64
	Number  int
	Title   string
	URL     string
	Content string
}

// BookContent — книга: содержание и текст главы.
type BookContent struct {
	CMID             int64
	Name             string
	Intro            string
	Content          string // текст текущей главы (BookContent) или всех обойдённых
	CurrentChapterID int64
	Chapters         []BookChapter
}

// URLContent — внешняя ссылка (/mod/url/view.php).
type URLContent struct {
	CMID     int64
	Name     string
	Intro    string
	URL      string // целевой адрес
	Redirect bool   // адрес получен из Location, без перехода по нему
}

// LessonContent — учебный урок (/mod/lesson/view.php), только чтение.
type LessonContent struct {
	CMID    int64
	Name    string
	Intro   string
	Content string
	Status  string // model.Moodle*
	Grade   string
	Info    []string
}

// ParseCourseOutline разбирает страницу курса: li.section/li.course-section,
// их summary и все li.activity с любым modtype_*.
func ParseCourseOutline(doc *goquery.Document, course Course) CourseOutline {
	out := CourseOutline{CourseID: course.ID, Course: course.FullName}
	if doc == nil {
		return out
	}
	sections := doc.Find("li.section, li.course-section")
	if sections.Length() == 0 {
		mods := parseOutlineModules(doc.Selection, course, "", 0)
		if len(mods) > 0 {
			out.Sections = append(out.Sections, CourseSection{Modules: mods})
		}
		return out
	}
	sections.Each(func(i int, sec *goquery.Selection) {
		num := sectionNumber(sec, i)
		name := outlineSectionName(sec)
		summary := outlineSectionSummary(sec)
		out.Sections = append(out.Sections, CourseSection{
			ID:      sectionID(sec),
			Num:     num,
			Name:    name,
			Summary: summary,
			Hidden:  isHiddenSection(sec),
			Modules: parseOutlineModules(sec, course, name, num),
		})
	})
	return out
}

// CourseOutline открывает страницу курса и разбирает полную структуру.
func (c *Client) CourseOutline(ctx context.Context, course Course) (CourseOutline, error) {
	doc, finalURL, err := c.PageWithURL(ctx, fmt.Sprintf("/course/view.php?id=%d", course.ID))
	if err != nil {
		return CourseOutline{}, err
	}
	out := ParseCourseOutline(doc, course)
	base := parseBase(finalURL)
	for si := range out.Sections {
		for mi := range out.Sections[si].Modules {
			m := &out.Sections[si].Modules[mi]
			m.URL = resolveURL(base, m.URL)
			for fi := range m.Files {
				m.Files[fi] = resolveURL(base, m.Files[fi])
			}
		}
	}
	logx.Debugf(logComponent, "структура курса id=%d %q: секций %d, модулей %d",
		course.ID, course.FullName, len(out.Sections), outlineModuleCount(out))
	return out, nil
}

func outlineModuleCount(o CourseOutline) int {
	n := 0
	for _, s := range o.Sections {
		n += len(s.Modules)
	}
	return n
}

func parseOutlineModules(scope *goquery.Selection, course Course, section string, sectionNum int) []CourseModule {
	var out []CourseModule
	scope.Find("li.activity").Each(func(_ int, li *goquery.Selection) {
		kind := moduleKind(li)
		href := moduleHref(li)
		cmid := cmidFromURL(href)
		if cmid == 0 {
			cmid = moduleID(li)
		}
		if cmid == 0 {
			logx.Warnf(logComponent, "курс id=%d: модуль тип=%s без cmid, пропущен", course.ID, kind)
			return
		}
		name := moduleName(li)
		available, note := moduleAvailability(li)
		out = append(out, CourseModule{
			CMID:             cmid,
			CourseID:         course.ID,
			Section:          section,
			SectionNum:       sectionNum,
			Kind:             kind,
			Name:             name,
			URL:              href,
			Description:      moduleDescription(li),
			Available:        available,
			AvailabilityNote: note,
			Files:            moduleFileLinks(li),
		})
	})
	return out
}

func moduleKind(li *goquery.Selection) string {
	for _, cl := range strings.Fields(li.AttrOr("class", "")) {
		if k, ok := strings.CutPrefix(cl, "modtype_"); ok && k != "" {
			return k
		}
	}
	if k := clean(li.AttrOr("data-modname", "")); k != "" {
		return k
	}
	// Запасной путь: /mod/<kind>/view.php в ссылке.
	if u, err := url.Parse(moduleHref(li)); err == nil {
		if parts := strings.Split(strings.Trim(u.Path, "/"), "/"); len(parts) >= 2 {
			for i := 0; i+1 < len(parts); i++ {
				if parts[i] == "mod" {
					return parts[i+1]
				}
			}
		}
	}
	return ""
}

func moduleHref(li *goquery.Selection) string {
	a := activityAnchor(li)
	return strings.TrimSpace(a.AttrOr("href", ""))
}

// activityAnchor — ссылка модуля только из .activityname/.activityinstance.
// Ссылки из .availabilityinfo и .contentafterlink не подходят: у недоступного
// модуля там может быть чужая ссылка, а у mod_label её нет вовсе.
func activityAnchor(li *goquery.Selection) *goquery.Selection {
	return li.Find(".activityname a, .activityinstance a").First()
}

func moduleID(li *goquery.Selection) int64 {
	if n, err := strconv.ParseInt(li.AttrOr("data-id", ""), 10, 64); err == nil && n > 0 {
		return n
	}
	id := strings.TrimPrefix(li.AttrOr("id", ""), "module-")
	n, _ := strconv.ParseInt(id, 10, 64)
	return n
}

func moduleName(li *goquery.Selection) string {
	a := activityAnchor(li)
	name := textWithout(a.Find(".instancename").First())
	if name == "" && a.Length() > 0 {
		name = textWithout(a)
	}
	if name == "" {
		name = textWithout(li.Find(".instancename").First())
	}
	if name == "" {
		name = clean(li.AttrOr("data-activityname", ""))
	}
	if name == "" {
		name = clean(li.Find("[data-activityname]").First().AttrOr("data-activityname", ""))
	}
	return name
}

func moduleDescription(li *goquery.Selection) string {
	sel := li.Find(".contentafterlink").First()
	if sel.Length() == 0 {
		return ""
	}
	return clip(textWithout(sel), 600)
}

func moduleAvailability(li *goquery.Selection) (bool, string) {
	noteSel := li.Find(".availabilityinfo, [data-region=availabilityinfo]").First()
	note := ""
	if noteSel.Length() > 0 {
		note = textWithout(noteSel)
	}
	hidden := hasClass(li, "hidden") || hasClass(li, "dimmed")
	restricted := hidden || hasClass(noteSel, "isrestricted")
	lower := strings.ToLower(note)
	for _, prefix := range []string{"недоступ", "не доступ", "restricted", "not available"} {
		if strings.HasPrefix(lower, prefix) {
			restricted = true
			break
		}
	}
	if hidden && note == "" {
		note = "Скрыто"
	}
	return !restricted, note
}

func moduleFileLinks(li *goquery.Selection) []string {
	return fileLinks(li)
}

func sectionNumber(sec *goquery.Selection, fallback int) int {
	// data-sectionid — первичный ключ секции в БД (например, 48219), это не номер.
	for _, attr := range []string{"data-sectionnum", "data-number"} {
		if n, err := strconv.Atoi(strings.TrimSpace(sec.AttrOr(attr, ""))); err == nil {
			return n
		}
	}
	if id := strings.TrimSpace(sec.AttrOr("id", "")); strings.HasPrefix(id, "section-") {
		if n, err := strconv.Atoi(strings.TrimPrefix(id, "section-")); err == nil {
			return n
		}
	}
	return fallback
}

func sectionID(sec *goquery.Selection) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(sec.AttrOr("data-sectionid", "")), 10, 64)
	return n
}

func outlineSectionName(sec *goquery.Selection) string {
	if n := clean(sec.AttrOr("data-sectionname", "")); n != "" {
		return n
	}
	sel := sec.Find(".sectionname, h3").First()
	return textWithout(sel)
}

func outlineSectionSummary(sec *goquery.Selection) string {
	sel := sec.Find(".summarytext, .summary").First()
	if sel.Length() == 0 {
		return ""
	}
	return clip(textWithout(sel), 800)
}

func isHiddenSection(sec *goquery.Selection) bool {
	if hasClass(sec, "hidden") {
		return true
	}
	if v := strings.ToLower(strings.TrimSpace(sec.AttrOr("data-hidden", ""))); v == "1" || v == "true" {
		return true
	}
	return false
}

// ParsePage разбирает страницу материала (/mod/page/view.php).
func ParsePage(doc *goquery.Document) PageContent {
	if doc == nil {
		return PageContent{}
	}
	p := PageContent{
		Name:  pageHeading(doc),
		Intro: introText(doc),
		Files: parseMaterialFileLinks(doc),
	}
	p.Content = textWithout(firstSelection(doc,
		"#page-content.generalbox, .generalbox#page-content",
		"#region-main .generalbox",
		"[role=main] .generalbox",
		"#page-content .no-overflow",
		"[role=main] .no-overflow",
	))
	return p
}

// PageContent открывает страницу материала и возвращает её текст.
func (c *Client) PageContent(ctx context.Context, cmid int64) (PageContent, error) {
	doc, finalURL, err := c.PageWithURL(ctx, fmt.Sprintf("/mod/page/view.php?id=%d", cmid))
	if err != nil {
		return PageContent{}, err
	}
	p := ParsePage(doc)
	p.CMID = cmid
	normalizeEntries(p.Files, parseBase(finalURL))
	return p, nil
}

// ParseFolder разбирает список файлов папки (/mod/folder/view.php).
func ParseFolder(doc *goquery.Document) []FolderEntry {
	if doc == nil {
		return nil
	}
	return parseMaterialFileLinks(doc)
}

// FolderFiles открывает папку и возвращает все файлы с подпапками.
func (c *Client) FolderFiles(ctx context.Context, cmid int64) ([]FolderEntry, error) {
	doc, finalURL, err := c.PageWithURL(ctx, fmt.Sprintf("/mod/folder/view.php?id=%d", cmid))
	if err != nil {
		return nil, err
	}
	files := ParseFolder(doc)
	normalizeEntries(files, parseBase(finalURL))
	return files, nil
}

// ParseBook разбирает страницу книги: название, введение, содержание (TOC)
// и текст открытой главы.
func ParseBook(doc *goquery.Document) BookContent {
	if doc == nil {
		return BookContent{}
	}
	b := BookContent{
		Name:  pageHeading(doc),
		Intro: introText(doc),
	}
	b.Content = textWithout(firstSelection(doc,
		".book_content .no-overflow",
		".book_content",
		".book_chapter .no-overflow",
		".book_chapter",
	))

	seen := map[int64]bool{}
	doc.Find(".book_toc a[href], .book_contents a[href*='chapterid=']").Each(func(_ int, a *goquery.Selection) {
		id := chapterID(a.AttrOr("href", ""))
		if id == 0 || seen[id] {
			return
		}
		seen[id] = true
		ch := BookChapter{
			ID:     id,
			Number: len(b.Chapters) + 1,
			Title:  textWithout(a),
			URL:    strings.TrimSpace(a.AttrOr("href", "")),
		}
		if n, ok := chapterNumber(ch.Title); ok {
			ch.Number = n
		}
		if chapterActive(a) && b.CurrentChapterID == 0 {
			b.CurrentChapterID = id
		}
		b.Chapters = append(b.Chapters, ch)
	})
	return b
}

// BookContent открывает книгу и возвращает TOC и текст открытой главы.
func (c *Client) BookContent(ctx context.Context, cmid int64) (BookContent, error) {
	doc, finalURL, err := c.PageWithURL(ctx, fmt.Sprintf("/mod/book/view.php?id=%d", cmid))
	if err != nil {
		return BookContent{}, err
	}
	b := ParseBook(doc)
	b.CMID = cmid
	normalizeBook(&b, parseBase(finalURL))
	return b, nil
}

// BookContentAll открывает книгу, обходит главы (не больше limit; limit <= 0 —
// все, но не более 100) и складывает их тексты в Content.
func (c *Client) BookContentAll(ctx context.Context, cmid int64, limit int) (BookContent, error) {
	b, err := c.BookContent(ctx, cmid)
	if err != nil {
		return BookContent{}, err
	}
	n := len(b.Chapters)
	if limit > 0 && limit < n {
		n = limit
	}
	if n > 100 {
		n = 100
	}
	var parts []string
	for i := 0; i < n; i++ {
		ch := &b.Chapters[i]
		if ch.URL == "" {
			continue
		}
		doc, _, err := c.PageWithURL(ctx, ch.URL)
		if err != nil {
			return b, err
		}
		page := ParseBook(doc)
		ch.Content = page.Content
		if ch.Title == "" {
			ch.Title = page.Name
		}
		if ch.Content != "" {
			parts = append(parts, ch.Content)
		}
	}
	if len(parts) > 0 {
		b.Content = strings.Join(parts, "\n\n")
	}
	return b, nil
}

// ParseURL разбирает страницу внешней ссылки: цель из .urlworkaround a,
// запасной вариант — meta refresh.
func ParseURL(doc *goquery.Document) URLContent {
	if doc == nil {
		return URLContent{}
	}
	u := URLContent{
		Name:  pageHeading(doc),
		Intro: introText(doc),
	}
	if a := doc.Find(".urlworkaround a[href]").First(); a.Length() > 0 {
		u.URL = strings.TrimSpace(a.AttrOr("href", ""))
	}
	if u.URL == "" {
		if a := doc.Find("#url a[href], .url a[href]").First(); a.Length() > 0 {
			u.URL = strings.TrimSpace(a.AttrOr("href", ""))
		}
	}
	if u.URL == "" {
		u.URL = metaRefreshURL(doc)
	}
	return u
}

// URLContent открывает страницу внешней ссылки. Редиректы не выполняются:
// на внешний сайт не уходят ни куки Moodle, ни запрос вообще.
func (c *Client) URLContent(ctx context.Context, cmid int64) (URLContent, error) {
	resp, body, err := c.getNoRedirect(ctx, fmt.Sprintf("/mod/url/view.php?id=%d", cmid))
	if err != nil {
		return URLContent{}, err
	}
	reqURL := resp.Request.URL
	if status := resp.StatusCode; status >= 300 && status < 400 {
		loc := strings.TrimSpace(resp.Header.Get("Location"))
		if loc == "" {
			return URLContent{}, fmt.Errorf("moodle: url cmid=%d: redirect without Location", cmid)
		}
		target := resolveURL(reqURL, loc)
		if isLoginURL(target) && sameHost(reqURL, target) {
			c.setSesskey("")
			return URLContent{}, ErrUnauthorized
		}
		return URLContent{CMID: cmid, URL: target, Redirect: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return URLContent{}, fmt.Errorf("moodle: url cmid=%d: status %d", cmid, resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return URLContent{}, fmt.Errorf("moodle: url cmid=%d: parse: %w", cmid, err)
	}
	u := ParseURL(doc)
	u.CMID = cmid
	if u.URL != "" {
		u.URL = resolveURL(reqURL, u.URL)
	}
	return u, nil
}

// ParseLesson разбирает учебный урок: заголовок, текст .contents, статус и оценку.
func ParseLesson(doc *goquery.Document) LessonContent {
	if doc == nil {
		return LessonContent{}
	}
	l := LessonContent{
		Name:   pageHeading(doc),
		Intro:  introText(doc),
		Status: lessonStatus(doc),
		Grade:  lessonGrade(doc),
		Info:   lessonInfoLines(doc),
	}
	l.Content = textWithout(firstSelection(doc, ".contents", ".lesson-content", "#page-content .no-overflow"))
	return l
}

// LessonContent открывает урок только через GET: без startattempt и submit.
func (c *Client) LessonContent(ctx context.Context, cmid int64) (LessonContent, error) {
	doc, _, err := c.PageWithURL(ctx, fmt.Sprintf("/mod/lesson/view.php?id=%d", cmid))
	if err != nil {
		return LessonContent{}, err
	}
	l := ParseLesson(doc)
	l.CMID = cmid
	return l, nil
}

// getNoRedirect выполняет GET без перехода по редиректам. Куки Moodle
// добавляются только к первому (своему) запросу и не уходят на чужой хост.
func (c *Client) getNoRedirect(ctx context.Context, path string) (*http.Response, []byte, error) {
	req, err := c.s.Request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if jar := c.s.Client.Jar; jar != nil {
		for _, ck := range jar.Cookies(req.URL) {
			req.AddCookie(ck)
		}
	}
	client := &http.Client{
		Transport: c.s.Client.Transport,
		Timeout:   c.s.Client.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("moodle: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMaterialPage))
	if err != nil {
		return resp, nil, fmt.Errorf("moodle: GET %s: read: %w", path, err)
	}
	return resp, body, nil
}

// lessonStatus — статус урока по .lessoninfo/.alert-info/.generaltable.
// Числовой прогресс «X из Y» читается только из строк о страницах/элементах:
// строка оценки «2,50 из 10,00» не должна превращать урок в завершённый.
func lessonStatus(doc *goquery.Document) string {
	lines := lessonInfoLines(doc)
	info := strings.ToLower(strings.Join(lines, " "))
	if containsAny(info, "не заверш", "не законч", "не пройден", "не начат", "not completed") {
		return model.MoodleTodo
	}
	for _, line := range lines {
		lower := strings.ToLower(line)
		if isLessonGradeLine(lower) || !isLessonProgressLine(lower) {
			continue
		}
		if m := lessonProgressRe.FindStringSubmatch(lower); len(m) == 3 {
			cur, _ := strconv.Atoi(m[1])
			total, _ := strconv.Atoi(m[2])
			if total > 0 && cur >= total {
				return model.MoodleDone
			}
			if cur > 0 {
				return model.MoodleDraft
			}
		}
	}
	done := containsAny(info, "завершили урок", "урок заверш", "урок законч", "урок пройден", "закончили урок", "completed", "finished")
	if done {
		return model.MoodleDone
	}
	if lessonGrade(doc) != "" {
		return model.MoodleSubmitted
	}
	return model.MoodleTodo
}

// isLessonGradeLine — строка про оценку/баллы, а не про пройденные страницы.
func isLessonGradeLine(line string) bool {
	return containsAny(line, "оценк", "балл", "score", "grade", "итог")
}

// isLessonProgressLine — строка действительно о прогрессе страниц/элементов.
func isLessonProgressLine(line string) bool {
	return containsAny(line, "страниц", "элемент", "вопрос", "page", "item", "прогресс", "progress", "выполнено", "пройдено")
}

var lessonProgressRe = regexp.MustCompile(`(\d+)\s+из\s+(\d+)`)

// lessonInfoLines — строки о ходе урока и оценке без дублей.
func lessonInfoLines(doc *goquery.Document) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = clean(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	doc.Find(".lessoninfo p, .lessoninfo div, .alert-info").Each(func(_ int, s *goquery.Selection) {
		add(s.Text())
	})
	doc.Find(".generaltable tr").Each(func(_ int, tr *goquery.Selection) {
		th := clean(tr.Find("th").First().Text())
		td := clean(tr.Find("td").First().Text())
		if th == "" || td == "" || td == "-" {
			return
		}
		l := strings.ToLower(th)
		if strings.Contains(l, "оценк") || strings.Contains(l, "выполн") ||
			strings.Contains(l, "затрачен") || strings.Contains(l, "grade") || strings.Contains(l, "score") {
			add(th + ": " + td)
		}
	})
	return out
}

// lessonGrade — оценка из таблицы .generaltable (или бейджа .grade).
func lessonGrade(doc *goquery.Document) string {
	grade := ""
	doc.Find(".generaltable tr").EachWithBreak(func(_ int, tr *goquery.Selection) bool {
		th := strings.ToLower(clean(tr.Find("th").First().Text()))
		td := clean(tr.Find("td").First().Text())
		if strings.Contains(th, "оценк") || th == "grade" {
			if td != "" && td != "-" && !strings.HasPrefix(td, "-") {
				grade = td
				return false
			}
		}
		return true
	})
	if grade == "" {
		grade = clean(doc.Find(".lessoninfo .grade, .grade").First().Text())
	}
	if grade == "-" {
		return ""
	}
	return grade
}

func pageHeading(doc *goquery.Document) string {
	if h := textWithout(doc.Find("h1").First()); h != "" {
		return h
	}
	if h := textWithout(doc.Find(".page-header-headings h1, #page-header h1").First()); h != "" {
		return h
	}
	title := clean(doc.Find("title").First().Text())
	if i := strings.Index(title, ": "); i >= 0 {
		title = title[i+2:]
	}
	return title
}

func firstSelection(doc *goquery.Document, selectors ...string) *goquery.Selection {
	for _, s := range selectors {
		if sel := doc.Find(s).First(); sel.Length() > 0 {
			return sel
		}
	}
	return doc.Find("__none__")
}

// textWithout — текст элемента без служебных подписей, скриптов и стилей.
func textWithout(sel *goquery.Selection) string {
	if sel == nil || sel.Length() == 0 {
		return ""
	}
	s := sel.Clone()
	s.Find(".accesshide, script, style, .section_action_menu, .editing_section").Remove()
	return clean(s.Text())
}

// parseMaterialFileLinks собирает pluginfile-ссылки только в основной области
// страницы, не задевая шапку с аватаркой. Если контейнеров нет — весь документ.
func parseMaterialFileLinks(doc *goquery.Document) []FolderEntry {
	scope := doc.Find("#region-main, [role=main], #page-content, .folder_tree, .filemanager, .activity-description")
	if scope.Length() == 0 {
		return parseFileLinks(doc.Selection)
	}
	return parseFileLinks(scope)
}

// parseFileLinks собирает ссылки и рисунки pluginfile.php без дублей.
func parseFileLinks(scope *goquery.Selection) []FolderEntry {
	if scope == nil || scope.Length() == 0 {
		return nil
	}
	var out []FolderEntry
	seen := map[string]bool{}
	add := func(raw, text string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || !strings.Contains(raw, "pluginfile.php") || seen[raw] {
			return
		}
		seen[raw] = true
		folder, name := splitPluginFilePath(raw)
		if t := clean(text); t != "" {
			name = t
		}
		if name == "" {
			name = "файл"
		}
		out = append(out, FolderEntry{Name: name, Folder: folder, URL: raw})
	}
	scope.Find("a[href*='pluginfile.php']").Each(func(_ int, a *goquery.Selection) {
		add(a.AttrOr("href", ""), a.Text())
	})
	scope.Find("img[src*='pluginfile.php']").Each(func(_ int, img *goquery.Selection) {
		add(img.AttrOr("src", ""), img.AttrOr("alt", ""))
	})
	return out
}

func fileLinks(scope *goquery.Selection) []string {
	entries := parseFileLinks(scope)
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.URL)
	}
	return out
}

// splitPluginFilePath достаёт имя файла и подпапку из pluginfile.php:
// /pluginfile.php/1/mod_folder/content/0/sub/file.pdf -> ("sub", "file.pdf").
func splitPluginFilePath(raw string) (folder, name string) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	name = segs[len(segs)-1]
	for i, s := range segs {
		if s != "content" || i+2 > len(segs) {
			continue
		}
		rest := segs[i+2:] // после content/<itemid>
		if len(rest) == 0 {
			break
		}
		name = rest[len(rest)-1]
		if len(rest) > 1 {
			folder = strings.Join(rest[:len(rest)-1], "/")
		}
		break
	}
	return folder, name
}

func chapterID(raw string) int64 {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(u.Query().Get("chapterid"), 10, 64)
	return n
}

func chapterActive(a *goquery.Selection) bool {
	if strings.TrimSpace(a.AttrOr("aria-current", "")) == "page" {
		return true
	}
	li := a.Closest("li")
	return hasClass(li, "active") || hasClass(li, "current")
}

func chapterNumber(title string) (int, bool) {
	m := chapterNumRe.FindStringSubmatch(title)
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

var chapterNumRe = regexp.MustCompile(`^\s*(\d+)[.)]\s`)

func metaRefreshURL(doc *goquery.Document) string {
	var target string
	doc.Find("meta[http-equiv]").EachWithBreak(func(_ int, m *goquery.Selection) bool {
		if !strings.EqualFold(strings.TrimSpace(m.AttrOr("http-equiv", "")), "refresh") {
			return true
		}
		content := strings.TrimSpace(m.AttrOr("content", ""))
		if i := strings.Index(strings.ToLower(content), "url="); i >= 0 {
			target = strings.Trim(strings.TrimSpace(content[i+4:]), `"'`)
			return false
		}
		return true
	})
	return target
}

func hasClass(sel *goquery.Selection, class string) bool {
	if sel == nil || sel.Length() == 0 {
		return false
	}
	for _, cl := range strings.Fields(sel.AttrOr("class", "")) {
		if cl == class {
			return true
		}
	}
	return false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func parseBase(raw string) *url.URL {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return u
}

func resolveURL(base *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if base == nil {
		return u.String()
	}
	return base.ResolveReference(u).String()
}

func normalizeEntries(entries []FolderEntry, base *url.URL) {
	for i := range entries {
		entries[i].URL = resolveURL(base, entries[i].URL)
	}
}

func normalizeBook(b *BookContent, base *url.URL) {
	for i := range b.Chapters {
		b.Chapters[i].URL = resolveURL(base, b.Chapters[i].URL)
	}
}

func isLoginURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.Contains(u.Path, "/login/index.php")
}

func sameHost(a *url.URL, raw string) bool {
	b, err := url.Parse(raw)
	return a != nil && err == nil && strings.EqualFold(a.Host, b.Host)
}
