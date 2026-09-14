package rasp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

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
	if r != nil && r.Loc != nil {
		return r.Loc
	}
	loc, err := time.LoadLocation("Europe/Samara")
	if err != nil {
		return time.FixedZone("Europe/Samara", 4*3600)
	}
	return loc
}

func (r *Refresher) Refresh(ctx context.Context) (model.ParseRun, error) {
	if r == nil || r.Store == nil {
		return model.ParseRun{}, errors.New("rasp: nil store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := nowFn().In(r.loc())
	run := model.ParseRun{At: now}

	body, status, err := Fetch(ctx, r.GroupID, 0)
	if err != nil {
		run.OK = false
		run.Status = err.Error()
		r.persistFail(run)
		return run, err
	}
	if status != http.StatusOK {
		run.OK = false
		run.Status = strconv.Itoa(status)
		r.persistFail(run)
		return run, nil
	}

	lessons, err := Parse(body, r.loc())
	if err != nil {
		run.OK = false
		run.Status = err.Error()
		r.persistFail(run)
		return run, err
	}

	old, err := r.Store.ListLessons()
	if err != nil {
		run.OK = false
		run.Status = err.Error()
		r.persistFail(run)
		return run, err
	}

	fetchedNext := false
	if nextWeek, ok := extractNextWeek(body); ok {
		nextBody, nextStatus, nextErr := Fetch(ctx, r.GroupID, nextWeek)
		if nextErr == nil && nextStatus == http.StatusOK {
			more, perr := Parse(nextBody, r.loc())
			if perr == nil {
				lessons = mergeLessons(lessons, more)
				fetchedNext = true
			}
		}
	}
	if !fetchedNext {
		lessons = keepFutureLessons(lessons, old)
	}

	diff := Diff(old, lessons)
	if err := r.Store.ReplaceLessons(lessons); err != nil {
		run.OK = false
		run.Status = err.Error()
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
	if err := r.Store.SaveParseRun(run); err != nil {
		return run, err
	}
	log.Printf("lessons=%d online=%d next_week=%v", run.LessonCount, run.OnlineCount, fetchedNext)
	msg := fmt.Sprintf("ok lessons=%d online=%d", run.LessonCount, run.OnlineCount)
	if diff != "" {
		msg = msg + "\n" + diff
	}
	_ = r.Store.AddEvent(model.Event{
		At:      now,
		Type:    model.EventReparse,
		Message: msg,
	})
	return run, nil
}

func (r *Refresher) persistFail(run model.ParseRun) {
	_ = r.Store.SaveParseRun(run)
	_ = r.Store.AddEvent(model.Event{
		At:      run.At,
		Type:    model.EventReparse,
		Message: run.Status,
	})
}

func (r *Refresher) StartCron(ctx context.Context) {
	if r == nil {
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
			kicked = false
		}
		now := nowFn().In(r.loc())
		hm := now.Format("15:04")
		_, cron := cronTimes[hm]
		key := now.Format("2006-01-02 15:04")
		if cron {
			if lastFired == key && !kicked {
				return
			}
			if lastFired != key {
				lastFired = key
			}
		} else if !kicked {
			return
		}
		_, _ = r.Refresh(ctx)
	}

	try()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			try()
		}
	}
}

func extractSelectedWeek(html []byte) (int, bool) {
	if n, ok := weekFromCurrentLabel(html); ok {
		return n, true
	}
	if n, ok := weekFromPrevNext(html); ok {
		return n, true
	}
	if n, ok := selectedWeekFromCurrentLink(html); ok {
		return n, true
	}
	return 0, false
}

func extractNextWeek(html []byte) (int, bool) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		return 0, false
	}
	href := doc.Find("a.week-nav-next").First().AttrOr("href", "")
	if n, ok := weekFromHref(href); ok {
		return n, true
	}
	if cur, ok := extractSelectedWeek(html); ok {
		return cur + 1, true
	}
	return 0, false
}

func keepFutureLessons(parsed, old []model.Lesson) []model.Lesson {
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
	if len(extra) == 0 {
		return parsed
	}
	return mergeLessons(parsed, extra)
}

func weekFromCurrentLabel(html []byte) (int, bool) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		return 0, false
	}
	text := strings.TrimSpace(doc.Find(".week-nav-current_week").First().Text())
	if text == "" {
		text = strings.TrimSpace(doc.Find(".week-nav-current").First().Text())
	}
	return parseWeekLabel(text)
}

func weekFromPrevNext(html []byte) (int, bool) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
		return 0, false
	}
	if n, ok := weekFromHref(doc.Find("a.week-nav-prev").First().AttrOr("href", "")); ok {
		return n + 1, true
	}
	if n, ok := weekFromHref(doc.Find("a.week-nav-next").First().AttrOr("href", "")); ok {
		return n - 1, true
	}
	return 0, false
}

func parseWeekLabel(text string) (int, bool) {
	m := weekLabelRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func weekFromHref(href string) (int, bool) {
	m := selectedWeekRe.FindStringSubmatch(href)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func selectedWeekFromCurrentLink(html []byte) (int, bool) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(html)))
	if err != nil {
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
	return found, ok
}
