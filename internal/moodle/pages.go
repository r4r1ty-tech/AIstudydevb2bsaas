package moodle

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// secretRes — sesskey в HTML страниц Moodle (JS-конфиг, ссылки, скрытые поля):
// в снимки страниц не пишем.
var secretRes = []*regexp.Regexp{
	regexp.MustCompile(`"sesskey"\s*:\s*"[^"]*"`),
	regexp.MustCompile(`sesskey=[^&"'\s<>]+`),
	regexp.MustCompile(`name="sesskey"[^>]*value="[^"]*"`),
	regexp.MustCompile(`value="[^"]*"[^>]*name="sesskey"`),
}

// dumpPage — снимок страницы Moodle (только при подробном логе), sesskey вычищен.
func pageSnapshot(name string, page []byte) {
	for _, re := range secretRes {
		page = re.ReplaceAll(page, []byte("<скрыто>"))
	}
	logx.Dump("moodle-page", name, page)
}

// dumpDoc — то же для уже разобранного документа.
func docSnapshot(name string, doc *goquery.Document) {
	h, err := doc.Html()
	if err != nil {
		return
	}
	pageSnapshot(name, []byte(h))
}

func fmtDue(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format("02.01.2006 15:04")
}

// Kinds — элементы курса, у которых бывают сроки и которые надо «сдавать».
var Kinds = map[string]bool{
	model.MoodleAssign:   true,
	model.MoodleQuiz:     true,
	model.MoodleLesson:   true,
	model.MoodleWorkshop: true,
}

type Activity struct {
	CMID     int64
	CourseID int64
	Course   string
	Section  string
	Kind     string
	Name     string
	URL      string
}

// CourseActivities — задания, тесты, лекции-уроки и семинары курса со страницы курса.
func (c *Client) CourseActivities(ctx context.Context, course Course) ([]Activity, error) {
	doc, err := c.page(ctx, fmt.Sprintf("/course/view.php?id=%d", course.ID))
	if err != nil {
		return nil, err
	}
	out := parseCourse(doc, course)
	logCoursePage(doc, course, len(out))
	return out, nil
}

// logCoursePage — сколько секций и модулей на странице курса и сколько из них нужных.
func logCoursePage(doc *goquery.Document, course Course, found int) {
	sections := doc.Find("li.section").Length()
	modules := doc.Find("li.activity").Length()
	logx.Debugf("moodle", "страница курса id=%d %q: секций %d, модулей %d, задания/тесты/уроки/семинары %d",
		course.ID, course.FullName, sections, modules, found)
	if modules == 0 {
		logx.Warnf("moodle", "страница курса id=%d %q: ни одного модуля (li.activity) — пусто или вёрстка сменилась", course.ID, course.FullName)
		if logx.Verbose() {
			docSnapshot(fmt.Sprintf("course-%d", course.ID), doc)
		}
	}
}

func parseCourse(doc *goquery.Document, course Course) []Activity {
	var out []Activity
	doc.Find("li.activity").Each(func(_ int, li *goquery.Selection) {
		kind := ""
		for _, cl := range strings.Fields(li.AttrOr("class", "")) {
			if k, ok := strings.CutPrefix(cl, "modtype_"); ok {
				kind = k
			}
		}
		if !Kinds[kind] {
			logx.Debugf("moodle", "курс id=%d: модуль тип=%s не нужен, пропущен", course.ID, kind)
			return
		}
		a := li.Find(".activityname a, .activityinstance a").First()
		href := a.AttrOr("href", "")
		cmid := cmidFromURL(href)
		if cmid == 0 {
			cmid, _ = strconv.ParseInt(li.AttrOr("data-id", strings.TrimPrefix(li.AttrOr("id", ""), "module-")), 10, 64)
		}
		if cmid == 0 {
			logx.Warnf("moodle", "курс id=%d: модуль тип=%s без cmid, пропущен", course.ID, kind)
			return
		}
		name := a.Find(".instancename").First().Clone()
		name.Find(".accesshide").Remove()
		title := clean(name.Text())
		if title == "" {
			title = clean(li.Find("[data-activityname]").First().AttrOr("data-activityname", ""))
		}
		sec := sectionName(li)
		logx.Debugf("moodle", "курс id=%d: модуль cmid=%d тип=%s секция=%q название=%q", course.ID, cmid, kind, sec, title)
		out = append(out, Activity{
			CMID:     cmid,
			CourseID: course.ID,
			Course:   course.FullName,
			Section:  sec,
			Kind:     kind,
			Name:     title,
			URL:      href,
		})
	})
	return out
}

func sectionName(li *goquery.Selection) string {
	sec := li.Closest("li.section, li.course-section, .course-section")
	if sec.Length() == 0 {
		return ""
	}
	if n := clean(sec.AttrOr("data-sectionname", "")); n != "" {
		return n
	}
	return clean(sec.Find(".sectionname, h3").First().Text())
}

// Detail — что видно на странице элемента.
type Detail struct {
	Opens  *time.Time
	Due    *time.Time
	Status string   // model.Moodle*
	Todo   []string // условия выполнения, попытки, лимит времени, описание
}

// ActivityDetail открывает страницу элемента и разбирает срок, статус и что сделать.
func (c *Client) ActivityDetail(ctx context.Context, a Activity, loc *time.Location) (Detail, error) {
	path := a.URL
	if path == "" {
		path = fmt.Sprintf("/mod/%s/view.php?id=%d", a.Kind, a.CMID)
	}
	doc, err := c.page(ctx, path)
	if err != nil {
		return Detail{}, err
	}
	d := parseDetail(doc, a.Kind, loc)
	logDetail(doc, a, d)
	return d, nil
}

// logDetail — что разобрано на странице элемента; пустой разбор задания/теста — Warnf и снимок.
func logDetail(doc *goquery.Document, a Activity, d Detail) {
	logx.Debugf("moodle", "элемент cmid=%d тип=%s %q: открыт=%s срок=%s статус=%s todo=%q",
		a.CMID, a.Kind, a.Name, fmtDue(d.Opens), fmtDue(d.Due), d.Status, strings.Join(d.Todo, " | "))
	if a.Kind != model.MoodleAssign && a.Kind != model.MoodleQuiz {
		return
	}
	const blocks = "[data-region=activity-dates], .activity-dates, .automatic-completion-conditions, " +
		"[data-region=completionrequirements], .completion-info, [data-action=toggle-manual-completion], " +
		".submissionstatustable, .quizinfo, .quizattempt, .quizstartbuttondiv, .activity-description, #intro"
	if doc.Find(blocks).Length() == 0 {
		logx.Warnf("moodle", "элемент cmid=%d тип=%s: на странице нет ни дат, ни условий, ни блоков попыток — разбор пуст", a.CMID, a.Kind)
		if logx.Verbose() {
			docSnapshot(fmt.Sprintf("%s-%d", a.Kind, a.CMID), doc)
		}
	}
}

func parseDetail(doc *goquery.Document, kind string, loc *time.Location) Detail {
	d := Detail{Status: model.MoodleTodo}
	var cutoff *time.Time
	doc.Find("[data-region=activity-dates] > div, .activity-dates > div").Each(func(_ int, row *goquery.Selection) {
		label := strings.ToLower(clean(row.Find("strong").First().Text()))
		at, ok := ParseRuDate(row.Text(), loc)
		if !ok {
			return
		}
		switch {
		case strings.HasPrefix(label, "открыт") || strings.HasPrefix(label, "откроется") ||
			strings.HasPrefix(label, "открывается") || strings.HasPrefix(label, "доступ") || strings.HasPrefix(label, "opened"):
			if d.Opens == nil {
				d.Opens = &at
			}
		case strings.HasPrefix(label, "запрет") || strings.Contains(label, "cut-off"):
			cutoff = &at
		case strings.Contains(label, "срок") || strings.HasPrefix(label, "закры") || strings.HasPrefix(label, "последн") ||
			strings.HasPrefix(label, "due") || strings.HasPrefix(label, "close"):
			if d.Due == nil {
				d.Due = &at
			}
		}
	})
	if d.Due == nil && cutoff != nil {
		d.Due = cutoff
	}

	// Условия выполнения (автоматические — бейджи, ручное — кнопка).
	allDone, seen := true, false
	doc.Find(".automatic-completion-conditions .badge, [data-region=completionrequirements] .badge, .completion-info .badge").Each(func(_ int, b *goquery.Selection) {
		t := clean(b.Text())
		if t == "" {
			return
		}
		seen = true
		done := strings.Contains(b.AttrOr("class", ""), "alert-success") || strings.HasPrefix(strings.ToLower(t), "выполнено")
		if !done {
			allDone = false
		}
		d.Todo = append(d.Todo, t)
	})
	if btn := doc.Find("[data-action=toggle-manual-completion]").First(); btn.Length() > 0 {
		seen = true
		if btn.AttrOr("data-toggletype", "") == "manual:undo" {
			d.Todo = append(d.Todo, "Отмечено как выполненное")
		} else {
			allDone = false
			d.Todo = append(d.Todo, "Отметить как выполненное вручную")
		}
	}

	switch kind {
	case model.MoodleAssign:
		d.Status = assignStatus(doc)
		doc.Find(".submissionstatustable tr").Each(func(_ int, tr *goquery.Selection) {
			th := clean(tr.Find("th").First().Text())
			td := clean(tr.Find("td").First().Text())
			if th == "" || td == "" || td == "-" {
				return
			}
			l := strings.ToLower(th)
			if strings.Contains(l, "состояние ответа") || strings.Contains(l, "оставшееся время") || strings.Contains(l, "состояние оценивания") {
				d.Todo = append(d.Todo, th+": "+td)
			}
		})
	case model.MoodleQuiz:
		d.Status = quizStatus(doc)
		doc.Find(".quizinfo p").Each(func(_ int, p *goquery.Selection) {
			if t := clean(p.Text()); t != "" {
				d.Todo = append(d.Todo, t)
			}
		})
		if g := clean(doc.Find("#feedback h3, #feedback").First().Text()); g != "" && d.Status == model.MoodleSubmitted {
			d.Todo = append(d.Todo, g)
		}
		canStart := doc.Find(".quizstartbuttondiv").Length() > 0 ||
			doc.Find("form[action*='startattempt.php']").Length() > 0 ||
			doc.Find("form[action*='attempt.php'], a[href*='attempt.php']").Length() > 0
		if !canStart && (d.Status == model.MoodleTodo || d.Status == model.MoodleDraft) {
			d.Todo = append(d.Todo, "Тест сейчас не открыт для попыток")
		}
	}
	if seen && allDone && d.Status != model.MoodleDraft {
		d.Status = model.MoodleDone
	}
	if intro := introText(doc); intro != "" {
		d.Todo = append(d.Todo, "Описание: "+intro)
	}
	return d
}

func assignStatus(doc *goquery.Document) string {
	st := model.MoodleTodo
	// В Moodle 4.5 класс висит на div-обёртке, а не на самой таблице.
	doc.Find(".submissionstatustable td").EachWithBreak(func(_ int, td *goquery.Selection) bool {
		cl := td.AttrOr("class", "")
		switch {
		case strings.Contains(cl, "submissionstatussubmitted"):
			st = model.MoodleSubmitted
			return false
		case strings.Contains(cl, "submissionstatusdraft"):
			st = model.MoodleDraft
			return false
		}
		return true
	})
	if st == model.MoodleTodo && graded(doc) {
		st = model.MoodleDone // оценено (в т.ч. «сдаётся вне сайта»)
	}
	return st
}

// graded — в блоке отзыва стоит оценка (не «-»).
func graded(doc *goquery.Document) bool {
	ok := false
	doc.Find(".feedback tr, .feedbacktable tr, table.generaltable tr").Each(func(_ int, tr *goquery.Selection) {
		th := strings.ToLower(clean(tr.Find("th").First().Text()))
		td := clean(tr.Find("td").First().Text())
		if th == "оценка" && td != "" && td != "-" && !strings.HasPrefix(td, "-") {
			ok = true
		}
	})
	return ok
}

func quizStatus(doc *goquery.Document) string {
	finished, inProgress := false, false
	// 4.5: по таблице quizreviewsummary на попытку; старые темы — quizattemptsummary.
	doc.Find("table.quizreviewsummary tr, table.quizattemptsummary tbody tr").Each(func(_ int, tr *goquery.Selection) {
		t := strings.ToLower(clean(tr.Text()))
		switch {
		case strings.Contains(t, "в процессе") || strings.Contains(t, "in progress"):
			inProgress = true
		case strings.Contains(t, "завершен") || strings.Contains(t, "завершён") || strings.Contains(t, "finished") ||
			strings.Contains(t, "отправлено"):
			finished = true
		}
	})
	if !finished && strings.Contains(strings.ToLower(clean(doc.Find("#feedback").Text())), "итоговая оценка") {
		finished = true // попыток не видно, но оценка выставлена (в т.ч. вручную)
	}
	switch {
	case inProgress:
		return model.MoodleDraft
	case finished:
		return model.MoodleSubmitted
	}
	return model.MoodleTodo
}

func introText(doc *goquery.Document) string {
	sel := doc.Find(".activity-description .no-overflow, .activity-description, #intro").First()
	if sel.Length() == 0 {
		return ""
	}
	s := sel.Clone()
	s.Find(".fileuploadsubmission, .fileuploadsubmissiontime, script, style").Remove()
	t := clean(s.Text())
	if r := []rune(t); len(r) > 400 {
		t = string(r[:400]) + "…"
	}
	return t
}

var ruMonths = map[string]time.Month{
	"января": 1, "февраля": 2, "марта": 3, "апреля": 4, "мая": 5, "июня": 6,
	"июля": 7, "августа": 8, "сентября": 9, "октября": 10, "ноября": 11, "декабря": 12,
	"январь": 1, "февраль": 2, "март": 3, "апрель": 4, "май": 5, "июнь": 6,
	"июль": 7, "август": 8, "сентябрь": 9, "октябрь": 10, "ноябрь": 11, "декабрь": 12,
}

var ruDateRe = regexp.MustCompile(`(\d{1,2})\s+([А-Яа-яЁё]+)\s+(\d{4})(?:\s*г\.?)?,?\s+(\d{1,2}):(\d{2})`)

// ParseRuDate — «воскресенье, 15 ноября 2026, 15:00» во времени сайта.
func ParseRuDate(s string, loc *time.Location) (time.Time, bool) {
	m := ruDateRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	mon, ok := ruMonths[strings.ToLower(m[2])]
	if !ok {
		return time.Time{}, false
	}
	day, _ := strconv.Atoi(m[1])
	year, _ := strconv.Atoi(m[3])
	hh, _ := strconv.Atoi(m[4])
	mm, _ := strconv.Atoi(m[5])
	if loc == nil {
		loc = time.UTC
	}
	return time.Date(year, mon, day, hh, mm, 0, 0, loc), true
}

func cmidFromURL(raw string) int64 {
	u, err := url.Parse(raw)
	if err != nil || !strings.Contains(u.Path, "/mod/") {
		return 0
	}
	n, _ := strconv.ParseInt(u.Query().Get("id"), 10, 64)
	return n
}

func clean(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
