package rasp

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const dateLayout = "02.01.2006"
const clockLayout = "15:04"

func Parse(html []byte, loc *time.Location) ([]model.Lesson, error) {
	if loc == nil {
		var err error
		loc, err = time.LoadLocation("Europe/Samara")
		if err != nil {
			loc = time.FixedZone("Europe/Samara", 4*3600)
		}
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
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

	var out []model.Lesson
	for t := 0; t+1 < len(times); t += 2 {
		beginClock := times[t]
		endClock := times[t+1]
		for d, date := range dates {
			idx := len(dates)*(t/2) + d
			if idx < 0 || idx >= len(cells) {
				continue
			}
			begin, finish, err := slotTimes(date, beginClock, endClock, loc)
			if err != nil {
				return nil, err
			}
			lessons, err := parseCell(cells[idx], begin, finish)
			if err != nil {
				return nil, err
			}
			out = append(out, lessons...)
		}
	}
	return out, nil
}

func slotTimes(date, beginClock, endClock string, loc *time.Location) (time.Time, time.Time, error) {
	begin, err := time.ParseInLocation(dateLayout+" "+clockLayout, date+" "+beginClock, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("rasp: begin %q %q: %w", date, beginClock, err)
	}
	finish, err := time.ParseInLocation(dateLayout+" "+clockLayout, date+" "+endClock, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("rasp: finish %q %q: %w", date, endClock, err)
	}
	return begin, finish, nil
}

func parseCell(cell *goquery.Selection, begin, finish time.Time) ([]model.Lesson, error) {
	if cell == nil {
		return nil, nil
	}
	var out []model.Lesson
	cell.Find(".schedule__lesson").Each(func(_ int, l *goquery.Selection) {
		discipline := strings.TrimSpace(l.Find(".schedule__discipline").First().Text())
		if discipline == "" {
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
	return strings.Join(names, ", ")
}

func parseSubgroup(raw string) int {
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
		return 0
	}
	return n
}
