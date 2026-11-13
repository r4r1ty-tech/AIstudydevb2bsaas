package lk

import (
	"fmt"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// WeekOf — номер учебной недели для дня (неделя 1 начинается с startDate).
func WeekOf(startDate string, day time.Time, loc *time.Location) (int, error) {
	start, err := time.ParseInLocation("2006-01-02", startDate, loc)
	if err != nil {
		return 0, fmt.Errorf("lk: start date %q: %w", startDate, err)
	}
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	days := int(d.Sub(start).Hours()/24 + 0.5)
	if days < 0 {
		return 1, nil
	}
	return days/7 + 1, nil
}

// ToLessons переводит неделю ЛК в пары бота.
// groupID — своя группа (по ней берётся подгруппа у потоковых пар).
// owner — telegram_id студента, чьи пары ИОТ в ответе; 0 — ИОТ не брать.
func (t *Timetable) ToLessons(week int, groupID, owner int64, loc *time.Location) ([]model.Lesson, error) {
	if t == nil {
		return nil, nil
	}
	start, err := time.ParseInLocation("2006-01-02", t.CurrentYear.StartDate, loc)
	if err != nil {
		return nil, fmt.Errorf("lk: start date %q: %w", t.CurrentYear.StartDate, err)
	}
	var out []model.Lesson
	add := func(src TTLesson, iet bool) error {
		w, ok := src.week(week)
		if !ok {
			return nil
		}
		if src.Weekday.ID < 1 || src.Weekday.ID > 7 {
			return fmt.Errorf("lk: lesson %d: weekday %d", src.ID, src.Weekday.ID)
		}
		day := start.AddDate(0, 0, (week-1)*7+src.Weekday.ID-1)
		begin, err := clock(day, src.Time.Begin, loc)
		if err != nil {
			return fmt.Errorf("lk: lesson %d: %w", src.ID, err)
		}
		finish, err := clock(day, src.Time.End, loc)
		if err != nil {
			return fmt.Errorf("lk: lesson %d: %w", src.ID, err)
		}
		l := model.Lesson{
			Date:       day.Format("2006-01-02"),
			Start:      begin.Format("15:04"),
			End:        finish.Format("15:04"),
			Begin:      begin,
			Finish:     finish,
			Discipline: src.discipline(),
			Teacher:    src.teachers(),
			Type:       src.typ(),
			Online:     w.IsOnline == 1,
			Place:      w.place(),
		}
		if src.Conference != nil {
			l.BBBURL = strings.TrimSpace(src.Conference.URL)
		}
		if iet {
			l.Owner = owner
		} else {
			l.Subgroup = src.subgroup(groupID)
		}
		if l.Discipline == "" {
			return nil
		}
		out = append(out, l)
		return nil
	}
	for _, l := range t.Lessons {
		if err := add(l, false); err != nil {
			return nil, err
		}
	}
	if owner != 0 {
		for _, l := range t.IET {
			if err := add(l, true); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func clock(day time.Time, hm string, loc *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02 15:04", day.Format("2006-01-02")+" "+strings.TrimSpace(hm), loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q: %w", hm, err)
	}
	return t, nil
}

func (l TTLesson) week(n int) (TTWeek, bool) {
	for _, w := range l.Weeks {
		if w.Week == n {
			return w, true
		}
	}
	return TTWeek{}, false
}

func (l TTLesson) discipline() string {
	if l.Discipline != nil && strings.TrimSpace(l.Discipline.Name) != "" {
		return strings.TrimSpace(l.Discipline.Name)
	}
	for _, f := range l.Flows {
		if f.Discipline != nil && strings.TrimSpace(f.Discipline.Name) != "" {
			return strings.TrimSpace(f.Discipline.Name)
		}
	}
	return ""
}

func (l TTLesson) typ() string {
	if l.Type != nil && strings.TrimSpace(l.Type.Name) != "" {
		return strings.TrimSpace(l.Type.Name)
	}
	for _, f := range l.Flows {
		if f.LoadType != nil && strings.TrimSpace(f.LoadType.Name) != "" {
			return strings.TrimSpace(f.LoadType.Name)
		}
	}
	return "unknown"
}

func (l TTLesson) teachers() string {
	names := make([]string, 0, len(l.Teachers))
	for _, t := range l.Teachers {
		if n := strings.TrimSpace(t.Name); n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

func (l TTLesson) subgroup(groupID int64) int {
	for _, g := range l.Groups {
		if g.ID == groupID && g.Subgroup != nil {
			return *g.Subgroup
		}
	}
	return 0
}

func (w TTWeek) place() string {
	if w.IsOnline == 1 && w.Room == nil {
		return "Online"
	}
	var parts []string
	if w.Room != nil && strings.TrimSpace(w.Room.Name) != "" {
		parts = append(parts, strings.TrimSpace(w.Room.Name))
	}
	if w.Building != nil && strings.TrimSpace(w.Building.Name) != "" {
		parts = append(parts, strings.TrimSpace(w.Building.Name)+" корпус")
	}
	p := strings.Join(parts, " - ")
	if w.IsOnline == 1 {
		if p == "" {
			return "Online"
		}
		return "Online, " + p
	}
	return p
}
