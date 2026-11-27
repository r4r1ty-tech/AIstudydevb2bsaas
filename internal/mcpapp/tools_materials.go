package mcpapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodlematerial"
)

const (
	// maxToolOutputRunes — общий потолок текстового ответа инструмента.
	maxToolOutputRunes = 100000
	// maxSearchHits — сколько совпадений поиска отдаём максимум.
	maxSearchHits = 200
	// searchSnippetRadius — сколько рун показывать вокруг совпадения.
	searchSnippetRadius = 120
	// maxDeepModules — сколько материалов обойти в deep-поиске одного курса.
	maxDeepModules = 60

	// OutlineResourceTemplate — шаблон ресурса со структурой курса.
	OutlineResourceTemplate = "moodle://course/{id}/outline"
	// MaterialResourceTemplate — шаблон ресурса с материалом по cmid.
	MaterialResourceTemplate = "moodle://material/{cmid}"
)

// OutlineInput — аргументы moodle_course_outline.
type OutlineInput struct {
	CourseID int64  `json:"course_id" jsonschema:"id курса из moodle_courses"`
	Kind     string `json:"kind,omitempty" jsonschema:"показывать только модули этого типа: page, resource, folder, book, url, lesson, assign, quiz"`
	Refresh  bool   `json:"refresh,omitempty" jsonschema:"игнорировать кэш структуры курса и перечитать страницу"`
}

// OutlineModule — компактный модуль курса.
type OutlineModule struct {
	CMID      int64  `json:"cmid"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Note      string `json:"availability_note,omitempty"`
	URL       string `json:"url,omitempty"`
}

// OutlineSection — секция курса с модулями.
type OutlineSection struct {
	Num     int             `json:"num"`
	Name    string          `json:"name"`
	Hidden  bool            `json:"hidden,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Modules []OutlineModule `json:"modules"`
}

// OutlineOutput — ответ moodle_course_outline.
type OutlineOutput struct {
	CourseID     int64            `json:"course_id"`
	Course       string           `json:"course,omitempty"`
	Sections     []OutlineSection `json:"sections"`
	TotalModules int              `json:"total_modules"`
	Truncated    bool             `json:"truncated,omitempty"`
}

// MaterialInput — аргументы moodle_material_read.
type MaterialInput struct {
	CMID         int64  `json:"cmid" jsonschema:"id модуля курса (cmid)"`
	CourseID     int64  `json:"course_id,omitempty" jsonschema:"id курса; нужен, если kind не указан"`
	Kind         string `json:"kind,omitempty" jsonschema:"тип модуля: page, resource, folder, book, url, lesson, assign, quiz, workshop"`
	MaxChars     int    `json:"max_chars,omitempty" jsonschema:"сколько рун текста вернуть: по умолчанию 20000"`
	IncludeFiles bool   `json:"include_files,omitempty" jsonschema:"для папки: прочитать текст первых файлов"`
	AllChapters  bool   `json:"all_chapters,omitempty" jsonschema:"для книги: обойти все главы, а не только текущую"`
	Refresh      bool   `json:"refresh,omitempty" jsonschema:"игнорировать кэш материала и перечитать его"`
}

// MaterialFile — файл материала.
type MaterialFile struct {
	Name   string `json:"name"`
	Folder string `json:"folder,omitempty"`
	URL    string `json:"url"`
}

// MaterialChapter — глава книги.
type MaterialChapter struct {
	ID     int64  `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url,omitempty"`
}

// MaterialOutput — ответ moodle_material_read.
type MaterialOutput struct {
	CMID      int64             `json:"cmid"`
	CourseID  int64             `json:"course_id,omitempty"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name,omitempty"`
	Section   string            `json:"section,omitempty"`
	URL       string            `json:"url,omitempty"`
	Text      string            `json:"text"`
	Status    string            `json:"status,omitempty"`
	Grade     string            `json:"grade,omitempty"`
	Due       string            `json:"due,omitempty"`
	Info      []string          `json:"info,omitempty"`
	Files     []MaterialFile    `json:"files,omitempty"`
	Chapters  []MaterialChapter `json:"chapters,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
	Next      string            `json:"next,omitempty"`
}

// SearchInput — аргументы moodle_search.
type SearchInput struct {
	Query    string `json:"query" jsonschema:"что искать: название, секция, описание или текст материала"`
	CourseID int64  `json:"course_id,omitempty" jsonschema:"ограничить поиск одним курсом"`
	Kind     string `json:"kind,omitempty" jsonschema:"искать только модули этого типа"`
	Deep     bool   `json:"deep,omitempty" jsonschema:"искать по тексту page/book/resource выбранного курса"`
	Refresh  bool   `json:"refresh,omitempty" jsonschema:"игнорировать кэш результатов поиска и повторить запрос"`
}

// SearchHit — одно совпадение.
type SearchHit struct {
	CourseID int64  `json:"course_id"`
	CMID     int64  `json:"cmid,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Section  string `json:"section,omitempty"`
	Name     string `json:"name"`
	Source   string `json:"source"` // name, section, description или text
	Snippet  string `json:"snippet"`
}

// SearchOutput — ответ moodle_search.
type SearchOutput struct {
	Query     string      `json:"query"`
	Total     int         `json:"total"`
	Results   []SearchHit `json:"results"`
	Truncated bool        `json:"truncated,omitempty"`
	Note      string      `json:"note,omitempty"`
}

// registerMaterialTools добавляет инструменты структуры/чтения/поиска и их
// ресурсы. Все они только читают Moodle.
func registerMaterialTools(s *mcp.Server, sess *Session) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_course_outline",
		Title:       "Структура курса Moodle",
		Description: "Показывает секции и модули курса Moodle по course_id: cmid, тип, название, доступность и ссылку. Можно отфильтровать по kind. Только чтение.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in OutlineInput) (*mcp.CallToolResult, OutlineOutput, error) {
		out, err := readOutline(ctx, sess, in.CourseID, in.Kind, in.Refresh)
		if err != nil {
			return nil, OutlineOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_material_read",
		Title:       "Чтение материала Moodle",
		Description: "Читает материал Moodle по cmid: текст страницы, файл ресурса, список и текст файлов папки, главу книги, целевую ссылку без перехода на внешний хост, текст урока, описание и сроки задания/теста/семинара. Попытки тестов не запускает.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in MaterialInput) (*mcp.CallToolResult, MaterialOutput, error) {
		out, err := readMaterialTool(ctx, sess, in)
		if err != nil {
			return nil, MaterialOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_search",
		Title:       "Поиск по Moodle",
		Description: "Ищет по названиям модулей, секциям и описаниям курсов Moodle; deep=true дополнительно читает текст page/book/resource выбранного курса и возвращает сниппеты вокруг совпадения.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		out, err := searchMoodle(ctx, sess, in)
		if err != nil {
			return nil, SearchOutput{}, err
		}
		return nil, out, nil
	})

	registerMaterialResources(s, sess)
}

// outlineCacheKey — ключ кэша структуры: курс и фильтр по типу.
func outlineCacheKey(courseID int64, kind string) string {
	return fmt.Sprintf("%d:%s", courseID, kind)
}

// readOutline открывает структуру курса под мьютексом сессии. Результат
// кэшируется на outlineCacheTTL; refresh=true обходит кэш.
func readOutline(ctx context.Context, sess *Session, courseID int64, kind string, refresh bool) (OutlineOutput, error) {
	if courseID <= 0 {
		return OutlineOutput{}, toolFailure("bad_input", "укажите course_id из moodle_courses", nil)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	key := outlineCacheKey(courseID, kind)
	if !refresh {
		if out, ok := sess.caches.outline.Get(key); ok {
			return out, nil
		}
	}
	var out OutlineOutput
	err := sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		name := courseName(ctx, c, courseID)
		o, err := c.CourseOutline(ctx, moodle.Course{ID: courseID, FullName: name})
		if err != nil {
			return err
		}
		out = buildOutline(o, kind)
		return nil
	})
	if err != nil {
		return OutlineOutput{}, materialToolError(err, "проверьте course_id и сессию (moodle_status)")
	}
	sess.caches.outline.Set(key, out)
	return out, nil
}

// courseName достаёт полное имя курса; ошибка не критична — останется пустым.
func courseName(ctx context.Context, c *moodle.Client, courseID int64) string {
	courses, err := c.Courses(ctx)
	if err != nil {
		logx.Debugf("mcpapp", "имя курса %d: %v", courseID, err)
		return ""
	}
	for _, cr := range courses {
		if cr.ID == courseID {
			return cr.FullName
		}
	}
	return ""
}

// buildOutline сжимает структуру курса до MCP-ответа и укладывается в лимит.
func buildOutline(o moodle.CourseOutline, kind string) OutlineOutput {
	out := OutlineOutput{
		CourseID: o.CourseID,
		Course:   o.Course,
		Sections: make([]OutlineSection, 0, len(o.Sections)),
	}
	used := moodlematerial.RuneLen(o.Course)
	stop := false
	for _, sec := range o.Sections {
		if stop {
			break
		}
		modules := make([]OutlineModule, 0, len(sec.Modules))
		for _, m := range sec.Modules {
			if kind != "" && m.Kind != kind {
				continue
			}
			if used+moodlematerial.RuneLen(m.Name) > maxToolOutputRunes {
				out.Truncated, stop = true, true
				break
			}
			used += moodlematerial.RuneLen(m.Name)
			out.TotalModules++
			modules = append(modules, OutlineModule{
				CMID:      m.CMID,
				Kind:      m.Kind,
				Name:      m.Name,
				Available: m.Available,
				Note:      m.AvailabilityNote,
				URL:       m.URL,
			})
		}
		if kind != "" && len(modules) == 0 {
			continue
		}
		summary := moodlematerial.ClipRunes(sec.Summary, 300)
		used += moodlematerial.RuneLen(summary)
		out.Sections = append(out.Sections, OutlineSection{
			Num:     sec.Num,
			Name:    sec.Name,
			Hidden:  sec.Hidden,
			Summary: summary,
			Modules: modules,
		})
	}
	return out
}

// materialCacheKey — ключ кэша материала из всех влияющих аргументов.
func materialCacheKey(in MaterialInput, kind string, maxChars int) string {
	return fmt.Sprintf("cmid=%d;course=%d;kind=%s;max=%d;files=%t;chapters=%t",
		in.CMID, in.CourseID, kind, maxChars, in.IncludeFiles, in.AllChapters)
}

// readMaterialTool читает материал и применяет общий потолок ответа.
// Результат кэшируется на materialCacheTTL; refresh=true обходит кэш.
func readMaterialTool(ctx context.Context, sess *Session, in MaterialInput) (MaterialOutput, error) {
	if in.CMID <= 0 {
		return MaterialOutput{}, toolFailure("bad_input", "укажите cmid из moodle_course_outline", nil)
	}
	maxChars := in.MaxChars
	if maxChars <= 0 {
		maxChars = moodlematerial.DefaultMaterialMaxChars
	}
	if maxChars > moodlematerial.MaxMaterialMaxChars {
		maxChars = moodlematerial.MaxMaterialMaxChars
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	key := materialCacheKey(in, kind, maxChars)
	if !in.Refresh {
		if out, ok := sess.caches.material.Get(key); ok {
			return out, nil
		}
	}
	var out MaterialOutput
	err := sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		req := moodlematerial.ReadRequest{
			CMID:         in.CMID,
			CourseID:     in.CourseID,
			Kind:         kind,
			MaxChars:     maxChars,
			IncludeFiles: in.IncludeFiles,
			AllChapters:  in.AllChapters,
		}
		if req.Kind == "" {
			mod, rerr := moodlematerial.ResolveModule(ctx, c, in.CMID, in.CourseID)
			if rerr != nil {
				return rerr
			}
			req.Kind, req.Module = mod.Kind, &mod
			if req.CourseID == 0 {
				req.CourseID = mod.CourseID
			}
		}
		m, rerr := moodlematerial.ReadMaterial(ctx, c, req)
		if rerr != nil {
			return rerr
		}
		out = materialOutput(m)
		return nil
	})
	if err != nil {
		return MaterialOutput{}, materialToolError(err, "проверьте cmid, course_id, kind и сессию (moodle_status)")
	}
	if moodlematerial.RuneLen(out.Text) > maxToolOutputRunes {
		out.Text = moodlematerial.ClipRunes(out.Text, maxToolOutputRunes)
		out.Truncated = true
	}
	sess.caches.material.Set(key, out)
	return out, nil
}

func materialOutput(m moodlematerial.Material) MaterialOutput {
	out := MaterialOutput{
		CMID:      m.CMID,
		CourseID:  m.CourseID,
		Kind:      m.Kind,
		Name:      m.Name,
		Section:   m.Section,
		URL:       m.URL,
		Text:      m.Text,
		Status:    m.Status,
		Grade:     m.Grade,
		Due:       m.Due,
		Info:      m.Info,
		Truncated: m.Truncated,
		Next:      m.Next,
	}
	for _, f := range m.Files {
		out.Files = append(out.Files, MaterialFile{Name: f.Name, Folder: f.Folder, URL: f.URL})
	}
	for _, ch := range m.Chapters {
		out.Chapters = append(out.Chapters, MaterialChapter{ID: ch.ID, Number: ch.Number, Title: ch.Title, URL: ch.URL})
	}
	return out
}

// searchCacheKey — ключ кэша поиска: запрос уже нормализован.
func searchCacheKey(query string, in SearchInput, kind string) string {
	return fmt.Sprintf("q=%s;course=%d;kind=%s;deep=%t", query, in.CourseID, kind, in.Deep)
}

// searchMoodle ищет по структуре курсов, а при deep=true — по тексту
// page/book/resource выбранного курса. Результат кэшируется на outlineCacheTTL:
// поиск опирается на ту же структуру курса.
func searchMoodle(ctx context.Context, sess *Session, in SearchInput) (SearchOutput, error) {
	query := strings.ToLower(moodlematerial.CleanText(in.Query))
	if query == "" {
		return SearchOutput{}, toolFailure("bad_input", "укажите непустой query", nil)
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if in.Deep && in.CourseID <= 0 {
		return SearchOutput{}, toolFailure("course_required", "для deep=true укажите course_id", nil)
	}
	key := searchCacheKey(query, in, kind)
	if !in.Refresh {
		if out, ok := sess.caches.search.Get(key); ok {
			return out, nil
		}
	}
	var out SearchOutput
	err := sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		res, rerr := runSearch(ctx, c, in, query, kind)
		if rerr != nil {
			return rerr
		}
		out = res
		return nil
	})
	if err != nil {
		return SearchOutput{}, materialToolError(err, "проверьте course_id, query и сессию (moodle_status)")
	}
	// Пустой ответ с Note — это сбой чтения курсов, а не результат поиска:
	// кэшировать его на 10 минут нельзя, иначе следующий вызов не повторит запрос.
	if len(out.Results) > 0 || out.Note == "" {
		sess.caches.search.Set(key, out)
	}
	return out, nil
}

// runSearch возвращает ошибку, если сессия протухла или контекст отменён:
// только тогда Session.do сможет перелогиниться и повторить запрос. Ошибки
// отдельных курсов и материалов без course_id остаются в Note/логе.
func runSearch(ctx context.Context, c *moodle.Client, in SearchInput, query, kind string) (SearchOutput, error) {
	out := SearchOutput{Query: in.Query, Results: make([]SearchHit, 0)}
	used := 0
	add := func(h SearchHit) bool {
		if len(out.Results) >= maxSearchHits || used+moodlematerial.RuneLen(h.Name)+moodlematerial.RuneLen(h.Snippet) > maxToolOutputRunes {
			out.Truncated = true
			return false
		}
		out.Results = append(out.Results, h)
		used += moodlematerial.RuneLen(h.Name) + moodlematerial.RuneLen(h.Snippet)
		return true
	}

	courses := []moodle.Course{{ID: in.CourseID}}
	if in.CourseID <= 0 {
		all, err := c.Courses(ctx)
		if err != nil {
			if searchFatal(err) {
				return out, err
			}
			out.Note = "курсы Moodle не получены: " + err.Error()
			return out, nil
		}
		courses = all
	}
	deepModules := 0
	for _, cr := range courses {
		o, err := c.CourseOutline(ctx, cr)
		if err != nil {
			if searchFatal(err) || in.CourseID > 0 {
				return out, err
			}
			logx.Debugf("mcpapp", "поиск: курс %d: %v", cr.ID, err)
			continue
		}
		courseID := o.CourseID
		if courseID == 0 {
			courseID = cr.ID
		}
		stop := false
		for _, sec := range o.Sections {
			if stop {
				break
			}
			if kind == "" && sectionMatches(sec, query) {
				if !add(SearchHit{
					CourseID: courseID,
					Section:  sec.Name,
					Name:     sec.Name,
					Source:   "section",
					Snippet:  snippetAround(sectionText(sec), query),
				}) {
					stop = true
					break
				}
			}
			for _, m := range sec.Modules {
				if kind != "" && m.Kind != kind {
					continue
				}
				hit, ok := moduleHit(courseID, m, query)
				if ok {
					if !add(hit) {
						stop = true
						break
					}
					continue
				}
				if !in.Deep || !deepKind(m.Kind) {
					continue
				}
				if deepModules >= maxDeepModules {
					out.Note = fmt.Sprintf("deep-поиск ограничен первыми %d материалами курса", maxDeepModules)
					continue
				}
				deepModules++
				text, terr := searchMaterialText(ctx, c, m)
				if terr != nil {
					if searchFatal(terr) {
						return out, terr
					}
					logx.Debugf("mcpapp", "поиск: материал cmid=%d: %v", m.CMID, terr)
					continue
				}
				if !strings.Contains(strings.ToLower(text), query) {
					continue
				}
				if !add(SearchHit{
					CourseID: courseID,
					CMID:     m.CMID,
					Kind:     m.Kind,
					Section:  m.Section,
					Name:     m.Name,
					Source:   "text",
					Snippet:  snippetAround(text, query),
				}) {
					stop = true
					break
				}
			}
		}
	}
	out.Total = len(out.Results)
	return out, nil
}

// searchFatal — ошибка, после которой Session.do обязан сбросить сессию и
// повторить запрос, а не прятать её в Note.
func searchFatal(err error) bool {
	return errors.Is(err, moodle.ErrUnauthorized) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func sectionMatches(sec moodle.CourseSection, query string) bool {
	return strings.Contains(strings.ToLower(sec.Name), query) ||
		strings.Contains(strings.ToLower(sec.Summary), query)
}

func sectionText(sec moodle.CourseSection) string {
	if sec.Summary != "" {
		return sec.Summary
	}
	return sec.Name
}

func moduleHit(courseID int64, m moodle.CourseModule, query string) (SearchHit, bool) {
	base := SearchHit{
		CourseID: courseID,
		CMID:     m.CMID,
		Kind:     m.Kind,
		Section:  m.Section,
		Name:     m.Name,
	}
	if strings.Contains(strings.ToLower(m.Name), query) {
		base.Source = "name"
		base.Snippet = snippetAround(m.Name, query)
		return base, true
	}
	if m.Description != "" && strings.Contains(strings.ToLower(m.Description), query) {
		base.Source = "description"
		base.Snippet = snippetAround(m.Description, query)
		return base, true
	}
	return SearchHit{}, false
}

func deepKind(kind string) bool {
	switch kind {
	case "page", "book", "resource":
		return true
	default:
		return false
	}
}

// searchMaterialText читает текст материала для deep-поиска. Ошибки не валят
// весь поиск: материал просто пропускается.
func searchMaterialText(ctx context.Context, c *moodle.Client, m moodle.CourseModule) (string, error) {
	switch m.Kind {
	case "page":
		p, err := c.PageContent(ctx, m.CMID)
		if err != nil {
			return "", err
		}
		return p.Intro + "\n" + p.Content, nil
	case "book":
		b, err := c.BookContentAll(ctx, m.CMID, 20)
		if err != nil {
			return "", err
		}
		return b.Content, nil
	case "resource":
		src := m.URL
		if src == "" {
			src = fmt.Sprintf("/mod/resource/view.php?id=%d", m.CMID)
		}
		data, contentType, err := c.FetchFile(ctx, src)
		if err != nil {
			return "", err
		}
		if strings.Contains(strings.ToLower(contentType), "html") {
			return moodlematerial.HTMLMainText(data), nil
		}
		return moodlematerial.ExtractFileText(data, contentType), nil
	default:
		return "", nil
	}
}

// snippetAround возвращает окно ±radius рун вокруг первого совпадения.
// Позиция ищется в lower, а номер руны считается по lower: у некоторых букв
// (İ, K) нижний регистр меняет длину в байтах и рунах.
func snippetAround(text, query string) string {
	text = moodlematerial.CleanText(text)
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	pos := strings.Index(lower, query)
	if pos < 0 {
		return moodlematerial.ClipRunes(text, searchSnippetRadius*2)
	}
	runes := []rune(text)
	start := utf8.RuneCountInString(lower[:pos])
	if start > len(runes) {
		start = len(runes)
	}
	from := start - searchSnippetRadius
	if from < 0 {
		from = 0
	}
	to := start + utf8.RuneCountInString(query) + searchSnippetRadius
	if to > len(runes) {
		to = len(runes)
	}
	if to < from {
		to = from
	}
	out := string(runes[from:to])
	if from > 0 {
		out = "…" + out
	}
	if to < len(runes) {
		out += "…"
	}
	return out
}

// registerMaterialResources добавляет шаблоны ресурсов структуры курса и
// материала. Оба отдают JSON.
func registerMaterialResources(s *mcp.Server, sess *Session) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: OutlineResourceTemplate,
		Name:        "course_outline",
		Title:       "Структура курса Moodle",
		Description: "Секции и модули курса Moodle в JSON. Подставьте id курса из moodle://courses.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := requestURI(req)
		id, err := parseTemplateID(uri, "moodle://course/", "/outline")
		if err != nil {
			return nil, err
		}
		out, err := readOutline(ctx, sess, id, "", false)
		if err != nil {
			return nil, err
		}
		return jsonResource(uri, out)
	})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: MaterialResourceTemplate,
		Name:        "material",
		Title:       "Материал Moodle",
		Description: "Текст материала Moodle по cmid в JSON. Тип определяется по структуре курса.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := requestURI(req)
		id, err := parseTemplateID(uri, "moodle://material/", "")
		if err != nil {
			return nil, err
		}
		out, err := readMaterialTool(ctx, sess, MaterialInput{CMID: id})
		if err != nil {
			return nil, err
		}
		return jsonResource(uri, out)
	})
}

func requestURI(req *mcp.ReadResourceRequest) string {
	if req != nil && req.Params != nil && req.Params.URI != "" {
		return req.Params.URI
	}
	return ""
}

func parseTemplateID(uri, prefix, suffix string) (int64, error) {
	raw := strings.TrimPrefix(uri, prefix)
	if suffix != "" {
		raw = strings.TrimSuffix(raw, suffix)
	}
	raw = strings.Trim(raw, "/")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("mcpapp: некорректный id в URI %q", uri)
	}
	return id, nil
}

func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(raw),
		}},
	}, nil
}

// materialToolError добавляет к ошибке код и действие, чтобы LLM мог
// исправить запрос, а не гадать по тексту исключения.
func materialToolError(err error, action string) error {
	code := "moodle_error"
	switch {
	case errors.Is(err, ErrNoCredentials):
		code = "auth_required"
	case errors.Is(err, moodle.ErrUnauthorized):
		code = "auth_expired"
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(err, moodlematerial.ErrKindRequired):
		code = "kind_required"
	case errors.Is(err, moodlematerial.ErrModuleNotFound):
		code = "module_not_found"
	}
	return toolFailure(code, action, err)
}

func toolFailure(code, action string, err error) error {
	if err == nil {
		return fmt.Errorf("%s: действие: %s", code, action)
	}
	return fmt.Errorf("%s: %v; действие: %s", code, err, action)
}
