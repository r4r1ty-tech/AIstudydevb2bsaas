package rasp

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const dateLayout = "02.01.2006"
const clockLayout = "15:04"

func Parse(html []byte, loc *time.Location) ([]model.Lesson, error) {
	logx.Debugf("rasp", "Parse: bytes=%d loc=%v", len(html), loc)
	if loc == nil {
		var err error
		loc, err = time.LoadLocation("Europe/Samara")
		if err != nil {
			logx.Warnf("rasp", "Parse: load Europe/Samara: %v", err)
			loc = time.FixedZone("Europe/Samara", 4*3600)
		}
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		logx.Errorf("rasp", "Parse: parse html bytes=%d: %v", len(html), err)
		return nil, fmt.Errorf("rasp: parse html: %w", err)
	}

	var dates []string
	doc.Find(".schedule__head-date").Each(func(_ int, s *goquery.Selection) {
		d := strings.TrimSpace(s.Text())
		if d != "" {
			dates = append(dates, d)
		}
	})

	var times []string
	doc.Find(".schedule__time-item").Each(func(_ int, s *goquery.Selection) {
		t := strings.TrimSpace(s.Text())
		if t != "" {
			times = append(times, t)
		}
	})

	var cells []*goquery.Selection
	doc.Find(".schedule__item:not(.schedule__head)").Each(func(_ int, s *goquery.Selection) {
		cells = append(cells, s)
	})
	logx.Debugf("rasp", "Parse: dates=%d times=%d cells=%d", len(dates), len(times), len(cells))

	var out []model.Lesson
	for t := 0; t+1 < len(times); t += 2 {
		beginClock := times[t]
		endClock := times[t+1]
		for d, date := range dates {
			idx := len(dates)*(t/2) + d
			if idx < 0 || idx >= len(cells) {
				logx.Debugf("rasp", "Parse: skip date=%s slot=%s-%s idx=%d cells=%d", date, beginClock, endClock, idx, len(cells))
				continue
			}
			begin, finish, err := slotTimes(date, beginClock, endClock, loc)
			if err != nil {
				logx.Errorf("rasp", "Parse: slot date=%s %s-%s: %v", date, beginClock, endClock, err)
				return nil, fmt.Errorf("Parse: slot %s %s-%s: %w", date, beginClock, endClock, err)
			}
			lessons, err := parseCell(cells[idx], begin, finish)
			if err != nil {
				logx.Errorf("rasp", "Parse: cell date=%s %s-%s idx=%d: %v", date, beginClock, endClock, idx, err)
				return nil, fmt.Errorf("Parse: cell %s %s-%s: %w", date, beginClock, endClock, err)
			}
			out = append(out, lessons...)
		}
	}
	logx.Infof("rasp", "Parse: lessons=%d", len(out))
	return out, nil
}

func slotTimes(date, beginClock, endClock string, loc *time.Location) (time.Time, time.Time, error) {
	logx.Debugf("rasp", "slotTimes: date=%s begin=%s end=%s loc=%v", date, beginClock, endClock, loc)
	begin, err := time.ParseInLocation(dateLayout+" "+clockLayout, date+" "+beginClock, loc)
	if err != nil {
		logx.Errorf("rasp", "slotTimes: begin date=%q clock=%q: %v", date, beginClock, err)
		return time.Time{}, time.Time{}, fmt.Errorf("rasp: begin %q %q: %w", date, beginClock, err)
	}
	finish, err := time.ParseInLocation(dateLayout+" "+clockLayout, date+" "+endClock, loc)
	if err != nil {
		logx.Errorf("rasp", "slotTimes: finish date=%q clock=%q: %v", date, endClock, err)
		return time.Time{}, time.Time{}, fmt.Errorf("rasp: finish %q %q: %w", date, endClock, err)
	}
	logx.Debugf("rasp", "slotTimes: begin=%s finish=%s", begin.Format(time.RFC3339), finish.Format(time.RFC3339))
	return begin, finish, nil
}

func parseCell(cell *goquery.Selection, begin, finish time.Time) ([]model.Lesson, error) {
	logx.Debugf("rasp", "parseCell: begin=%s finish=%s", begin.Format(time.RFC3339), finish.Format(time.RFC3339))
	if cell == nil {
		logx.Debugf("rasp", "parseCell: nil cell")
		return nil, nil
	}
	var out []model.Lesson
	cell.Find(".schedule__lesson").Each(func(_ int, l *goquery.Selection) {
		discipline := strings.TrimSpace(l.Find(".schedule__discipline").First().Text())
		if discipline == "" {
			logx.Debugf("rasp", "parseCell: skip empty discipline date=%s", begin.Format("2006-01-02"))
			return
		}
		place := strings.TrimSpace(l.Find(".schedule__place").First().Text())
		typ := strings.TrimSpace(l.Find(".schedule__lesson-type-chip").First().Text())
		if typ == "" {
			typ = strings.TrimSpace(l.Find(".schedule__lesson-type-color").First().Text())
		}
		if typ == "" {
			typ = "unknown"
		}
		lesson := model.Lesson{
			Date:       begin.Format("2006-01-02"),
			Start:      begin.Format(clockLayout),
			End:        finish.Format(clockLayout),
			Begin:      begin,
			Finish:     finish,
			Discipline: discipline,
			Teacher:    joinTeachers(l),
			Place:      place,
			Subgroup:   parseSubgroup(l.Find(".schedule__groups span").First().Text()),
			Type:       typ,
			Online:     strings.Contains(strings.ToLower(place), "online"),
		}
		out = append(out, lesson)
	})
	logx.Debugf("rasp", "parseCell: lessons=%d date=%s", len(out), begin.Format("2006-01-02"))
	return out, nil
}

func joinTeachers(l *goquery.Selection) string {
	var names []string
	l.Find(".schedule__teacher a").Each(func(_ int, a *goquery.Selection) {
		name := strings.TrimSpace(a.Text())
		if name != "" {
			names = append(names, name)
		}
	})
	if len(names) == 0 {
		plain := strings.Join(strings.Fields(l.Find(".schedule__teacher").First().Text()), " ")
		if plain != "" {
			names = append(names, plain)
		}
	}
	result := strings.Join(names, ", ")
	logx.Debugf("rasp", "joinTeachers: teachers=%d", len(names))
	return result
}

func parseSubgroup(raw string) int {
	logx.Debugf("rasp", "parseSubgroup: raw=%q", raw)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	_, after, ok := strings.Cut(raw, ":")
	if ok {
		raw = after
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		logx.Warnf("rasp", "parseSubgroup: %q: %v", raw, err)
		return 0
	}
	logx.Debugf("rasp", "parseSubgroup: subgroup=%d", n)
	return n
}
