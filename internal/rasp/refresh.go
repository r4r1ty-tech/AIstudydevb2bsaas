package rasp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

var (
	cronTimes = map[string]struct{}{
		"07:00": {},
		"15:00": {},
		"19:00": {},
		"22:00": {},
	}
	cronInterval = 30 * time.Second
	nowFn        = time.Now

	selectedWeekRe = regexp.MustCompile(`selectedWeek=(\d+)`)
	weekLabelRe    = regexp.MustCompile(`(\d+)\s*недел`)
)

type Refresher struct {
	Store   *store.Store
	GroupID int64
	Loc     *time.Location
}

func (r *Refresher) loc() *time.Location {
	logx.Debugf("rasp", "Refresher.loc: enter")
	if r != nil && r.Loc != nil {
		logx.Debugf("rasp", "Refresher.loc: configured=%v", r.Loc)
		return r.Loc
	}
	loc, err := time.LoadLocation("Europe/Samara")
	if err != nil {
		logx.Warnf("rasp", "Refresher.loc: load Europe/Samara: %v", err)
		return time.FixedZone("Europe/Samara", 4*3600)
	}
	logx.Debugf("rasp", "Refresher.loc: loaded=%v", loc)
	return loc
}

func (r *Refresher) Refresh(ctx context.Context) (model.ParseRun, error) {
	logx.Debugf("rasp", "Refresher.Refresh: enter")
	if r == nil || r.Store == nil {
		logx.Errorf("rasp", "Refresher.Refresh: nil store")
		return model.ParseRun{}, errors.New("rasp: nil store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := nowFn().In(r.loc())
	run := model.ParseRun{At: now}
	logx.Debugf("rasp", "refresh group=%d", r.GroupID)
	logx.Debugf("rasp", "Refresher.Refresh: group=%d now=%s", r.GroupID, now.Format(time.RFC3339))

	body, status, err := Fetch(ctx, r.GroupID, 0)
	if err != nil {
		run.OK = false
		run.Status = err.Error()
		logx.Warnf("rasp", "fetch: %v", err)
		logx.Errorf("rasp", "Refresher.Refresh: fetch group=%d: %v", r.GroupID, err)
		r.persistFail(run)
		return run, err
	}
	if status != http.StatusOK {
		run.OK = false
		run.Status = strconv.Itoa(status)
		logx.Warnf("rasp", "fetch status=%d", status)
		logx.Warnf("rasp", "Refresher.Refresh: non-200 group=%d status=%d", r.GroupID, status)
		r.persistFail(run)
		return run, nil
	}
	logx.Infof("rasp", "Refresher.Refresh: fetch ok group=%d status=%d bytes=%d", r.GroupID, status, len(body))

	lessons, err := Parse(body, r.loc())
	if err != nil {
		run.OK = false
		run.Status = err.Error()
		logx.Errorf("rasp", "parse: %v", err)
		logx.Errorf("rasp", "Refresher.Refresh: parse group=%d: %v", r.GroupID, err)
		r.persistFail(run)
		return run, err
	}
	logx.Infof("rasp", "Refresher.Refresh: parsed=%d group=%d", len(lessons), r.GroupID)

	old, err := r.Store.ListLessons()
	if err != nil {
		run.OK = false
		run.Status = err.Error()
		logx.Errorf("rasp", "list lessons: %v", err)
		logx.Errorf("rasp", "Refresher.Refresh: list lessons: %v", err)
		r.persistFail(run)
		return run, err
	}
	logx.Debugf("rasp", "Refresher.Refresh: existing=%d", len(old))

	fetchedNext := false
	if nextWeek, ok := extractNextWeek(body); ok {
		logx.Debugf("rasp", "Refresher.Refresh: next week=%d", nextWeek)
		nextBody, nextStatus, nextErr := Fetch(ctx, r.GroupID, nextWeek)
		if nextErr != nil {
			logx.Warnf("rasp", "Refresher.Refresh: fetch next week=%d: %v", nextWeek, nextErr)
		}
		if nextErr == nil && nextStatus == http.StatusOK {
			more, perr := Parse(nextBody, r.loc())
			if perr != nil {
				logx.Warnf("rasp", "Refresher.Refresh: parse next week=%d: %v", nextWeek, perr)
			}
			if perr == nil {
				lessons = mergeLessons(lessons, more)
				fetchedNext = true
				logx.Infof("rasp", "Refresher.Refresh: next week=%d more=%d total=%d", nextWeek, len(more), len(lessons))
			}
		}
	} else {
		logx.Debugf("rasp", "Refresher.Refresh: next week not found")
	}
	if !fetchedNext {
		lessons = keepFutureLessons(lessons, old)
		logx.Debugf("rasp", "Refresher.Refresh: keepFuture total=%d", len(lessons))
	}

	// Сайт отдал страницу без пар (шаблон ошибки, сбой вёрстки): замена стёрла бы
	// все пары вместе с решениями и присутствием. Пустую неделю от сбоя не
	// отличить, поэтому расписание оставляем как было и ждём следующего прогона.
	if len(lessons) == 0 && len(old) > 0 {
		run.OK = false
		run.Status = "пустое расписание — оставил прежнее"
		logx.Warnf("rasp", "Refresher.Refresh: распарсено 0 пар при %d в базе — не заменяю", len(old))
		r.persistFail(run)
		return run, nil
	}

	diff := Diff(old, lessons)
	logx.Debugf("rasp", "Refresher.Refresh: diff_bytes=%d", len(diff))
	if err := r.Store.ReplaceLessons(lessons); err != nil {
		run.OK = false
		run.Status = err.Error()
		logx.Errorf("rasp", "Refresher.Refresh: replace lessons: %v", err)
		r.persistFail(run)
		return run, err
	}

	online := 0
	for _, l := range lessons {
		if l.Online {
			online++
		}
	}
	run.OK = true
	run.Status = strconv.Itoa(http.StatusOK)
	run.LessonCount = len(lessons)
	run.OnlineCount = online
	run.Diff = diff
	if diff != "" {
		logx.Infof("rasp", "diff:\n%s", diff)
	}
	if err := r.Store.SaveParseRun(run); err != nil {
		logx.Errorf("rasp", "save parse run: %v", err)
		logx.Errorf("rasp", "Refresher.Refresh: save parse run: %v", err)
		return run, err
	}
	logx.Infof("rasp", "lessons=%d online=%d next_week=%v", run.LessonCount, run.OnlineCount, fetchedNext)
	logx.Infof("rasp", "Refresher.Refresh: ok group=%d status=%s lessons=%d online=%d next_week=%v",
		r.GroupID, run.Status, run.LessonCount, run.OnlineCount, fetchedNext)
	msg := fmt.Sprintf("ok lessons=%d online=%d", run.LessonCount, run.OnlineCount)
	if diff != "" {
		msg = msg + "\n" + diff
	}
	if err := r.Store.AddEvent(model.Event{
		At:      now,
		Type:    model.EventReparse,
		Message: msg,
	}); err != nil {
		logx.Warnf("rasp", "Refresher.Refresh: add event: %v", err)
	}
	return run, nil
}

func (r *Refresher) persistFail(run model.ParseRun) {
	logx.Debugf("rasp", "Refresher.persistFail: status=%s", run.Status)
	logx.Warnf("rasp", "refresh fail: %s", run.Status)
	if err := r.Store.SaveParseRun(run); err != nil {
		logx.Errorf("rasp", "Refresher.persistFail: save parse run: %v", err)
	}
	if err := r.Store.AddEvent(model.Event{
		At:      run.At,
		Type:    model.EventReparse,
		Message: run.Status,
	}); err != nil {
		logx.Errorf("rasp", "Refresher.persistFail: add event: %v", err)
	}
}

func (r *Refresher) StartCron(ctx context.Context) {
	logx.Debugf("rasp", "Refresher.StartCron: enter interval=%s", cronInterval)
	if r == nil {
		logx.Warnf("rasp", "Refresher.StartCron: nil refresher")
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(cronInterval)
	defer ticker.Stop()

	var lastFired string
	try := func() {
		kicked, err := r.Store.ConsumeRaspRefresh()
		if err != nil {
			logx.Warnf("rasp", "Refresher.StartCron: consume refresh: %v", err)
			kicked = false
		}
		now := nowFn().In(r.loc())
		hm := now.Format("15:04")
		_, cron := cronTimes[hm]
		key := now.Format("2006-01-02 15:04")
		logx.Debugf("rasp", "Refresher.StartCron: tick hm=%s cron=%v kicked=%v lastFired=%s", hm, cron, kicked, lastFired)
		if cron {
			if lastFired == key && !kicked {
				return
			}
			if lastFired != key {
				lastFired = key
				logx.Infof("rasp", "cron %s", hm)
			}
		} else if !kicked {
			return
		}
		if kicked {
			logx.Infof("rasp", "manual refresh kick")
		}
		if _, err := r.Refresh(ctx); err != nil {
			logx.Errorf("rasp", "Refresher.StartCron: refresh: %v", err)
		}
	}

	try()
	for {
		select {
		case <-ctx.Done():
			logx.Debugf("rasp", "Refresher.StartCron: ctx done")
			return
		case <-ticker.C:
			try()
		}
	}
}

func extractSelectedWeek(html []byte) (int, bool) {
	logx.Debugf("rasp", "extractSelectedWeek: bytes=%d", len(html))
	if n, ok := weekFromCurrentLabel(html); ok {
		logx.Debugf("rasp", "extractSelectedWeek: from label week=%d", n)
		return n, true
	}
	if n, ok := weekFromPrevNext(html); ok {
		logx.Debugf("rasp", "extractSelectedWeek: from prev/next week=%d", n)
		return n, true
	}
	if n, ok := selectedWeekFromCurrentLink(html); ok {
		logx.Debugf("rasp", "extractSelectedWeek: from current link week=%d", n)
		return n, true
	}
	logx.Debugf("rasp", "extractSelectedWeek: not found")
	return 0, false
}

func extractNextWeek(html []byte) (int, bool) {
	logx.Debugf("rasp", "extractNextWeek: bytes=%d", len(html))
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		logx.Warnf("rasp", "extractNextWeek: parse html: %v", err)
		return 0, false
	}
	href := doc.Find("a.week-nav-next").First().AttrOr("href", "")
	logx.Debugf("rasp", "extractNextWeek: next_href=%q", href)
	if n, ok := weekFromHref(href); ok {
		return n, true
	}
	if cur, ok := extractSelectedWeek(html); ok {
		logx.Debugf("rasp", "extractNextWeek: current=%d", cur)
		return cur + 1, true
	}
	logx.Debugf("rasp", "extractNextWeek: not found")
	return 0, false
}

func keepFutureLessons(parsed, old []model.Lesson) []model.Lesson {
	logx.Debugf("rasp", "keepFutureLessons: parsed=%d old=%d", len(parsed), len(old))
	maxNew := ""
	for _, l := range parsed {
		if l.Date > maxNew {
			maxNew = l.Date
		}
	}
	var extra []model.Lesson
	for _, l := range old {
		if maxNew == "" || l.Date > maxNew {
			extra = append(extra, l)
		}
	}
	logx.Debugf("rasp", "keepFutureLessons: max=%s extra=%d", maxNew, len(extra))
	if len(extra) == 0 {
		return parsed
	}
	return mergeLessons(parsed, extra)
}

func weekFromCurrentLabel(html []byte) (int, bool) {
	logx.Debugf("rasp", "weekFromCurrentLabel: bytes=%d", len(html))
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		logx.Warnf("rasp", "weekFromCurrentLabel: parse html: %v", err)
		return 0, false
	}
	text := strings.TrimSpace(doc.Find(".week-nav-current_week").First().Text())
	if text == "" {
		text = strings.TrimSpace(doc.Find(".week-nav-current").First().Text())
	}
	logx.Debugf("rasp", "weekFromCurrentLabel: text=%q", text)
	return parseWeekLabel(text)
}

func weekFromPrevNext(html []byte) (int, bool) {
	logx.Debugf("rasp", "weekFromPrevNext: bytes=%d", len(html))
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		logx.Warnf("rasp", "weekFromPrevNext: parse html: %v", err)
		return 0, false
	}
	if n, ok := weekFromHref(doc.Find("a.week-nav-prev").First().AttrOr("href", "")); ok {
		logx.Debugf("rasp", "weekFromPrevNext: prev week=%d", n+1)
		return n + 1, true
	}
	if n, ok := weekFromHref(doc.Find("a.week-nav-next").First().AttrOr("href", "")); ok {
		logx.Debugf("rasp", "weekFromPrevNext: next week=%d", n-1)
		return n - 1, true
	}
	logx.Debugf("rasp", "weekFromPrevNext: not found")
	return 0, false
}

func parseWeekLabel(text string) (int, bool) {
	logx.Debugf("rasp", "parseWeekLabel: text=%q", text)
	m := weekLabelRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		logx.Warnf("rasp", "parseWeekLabel: %q: %v", text, err)
		return 0, false
	}
	logx.Debugf("rasp", "parseWeekLabel: week=%d", n)
	return n, true
}

func weekFromHref(href string) (int, bool) {
	logx.Debugf("rasp", "weekFromHref: href=%q", href)
	m := selectedWeekRe.FindStringSubmatch(href)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		logx.Warnf("rasp", "weekFromHref: %q: %v", href, err)
		return 0, false
	}
	logx.Debugf("rasp", "weekFromHref: week=%d", n)
	return n, true
}

func selectedWeekFromCurrentLink(html []byte) (int, bool) {
	logx.Debugf("rasp", "selectedWeekFromCurrentLink: bytes=%d", len(html))
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		logx.Warnf("rasp", "selectedWeekFromCurrentLink: parse html: %v", err)
		return 0, false
	}
	var found int
	ok := false
	doc.Find("a[href*='selectedWeek']").Each(func(_ int, s *goquery.Selection) {
		if ok {
			return
		}
		class := strings.ToLower(s.AttrOr("class", ""))
		if strings.Contains(class, "weekday-nav") {
			return
		}
		if !strings.Contains(class, "current") &&
			!strings.Contains(class, "active") &&
			!strings.Contains(class, "selected") {
			return
		}
		n, wok := weekFromHref(s.AttrOr("href", ""))
		if !wok {
			return
		}
		found = n
		ok = true
	})
	logx.Debugf("rasp", "selectedWeekFromCurrentLink: week=%d ok=%v", found, ok)
	return found, ok
}
