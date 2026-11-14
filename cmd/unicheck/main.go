// unicheck — разовая проверка клиентов ЛК и Moodle на живых сайтах.
// Логин и пароль берёт из UNI_LOGIN / UNI_PASSWORD (и UNI_MOODLE_LOGIN /
// UNI_MOODLE_PASSWORD, если в Moodle другие), ничего не сохраняет и не печатает
// секретов: только что удалось достать.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/lk"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

func main() {
	login, pass := os.Getenv("UNI_LOGIN"), os.Getenv("UNI_PASSWORD")
	if login == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "нужны UNI_LOGIN и UNI_PASSWORD")
		os.Exit(2)
	}
	mLogin, mPass := envOr("UNI_MOODLE_LOGIN", login), envOr("UNI_MOODLE_PASSWORD", pass)
	loc, err := time.LoadLocation("Europe/Samara")
	if err != nil {
		fmt.Fprintln(os.Stderr, "таймзона Europe/Samara:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ok := checkLK(ctx, login, pass, loc)
	ok = checkMoodle(ctx, mLogin, mPass, loc) && ok
	if !ok {
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func checkLK(ctx context.Context, login, pass string, loc *time.Location) bool {
	fmt.Println("== ЛК ==")
	c, err := lk.New("")
	if err != nil {
		fmt.Println("клиент ЛК:", err)
		return false
	}
	if err := c.Login(ctx, login, pass); err != nil {
		fmt.Println("вход:", err)
		return false
	}
	fmt.Println("вход: ok")
	groups, err := c.Groups(ctx)
	if err != nil || len(groups) == 0 {
		fmt.Println("группы:", err, len(groups))
		return false
	}
	years, err := c.Years(ctx)
	if err != nil {
		fmt.Println("годы:", err)
		return false
	}
	now := time.Now().In(loc)
	yid, ok := lk.CurrentYearID(years, now)
	if !ok {
		fmt.Println("текущий учебный год не найден")
		return false
	}
	first, err := c.Timetable(ctx, yid, 1, groups[0].ID)
	if err != nil {
		fmt.Println("расписание:", err)
		return false
	}
	week, err := lk.WeekOf(first.CurrentYear.StartDate, now, loc)
	if err != nil {
		fmt.Println("неделя:", err)
		return false
	}
	fmt.Printf("группа %s (id %d), год %d, старт %s, неделя %d\n", groups[0].Name, groups[0].ID, yid, first.CurrentYear.StartDate, week)
	for _, w := range []int{week, week + 1} {
		tt, err := c.Timetable(ctx, yid, w, groups[0].ID)
		if err != nil {
			fmt.Println("неделя", w, err)
			return false
		}
		ls, err := tt.ToLessons(w, groups[0].ID, 1, loc)
		if err != nil {
			fmt.Println("разбор недели", w, err)
			return false
		}
		online, bbb, iet, sub := 0, 0, 0, 0
		for _, l := range ls {
			if l.Online {
				online++
			}
			if l.BBBURL != "" {
				bbb++
			}
			if l.Owner != 0 {
				iet++
			}
			if l.Subgroup != 0 {
				sub++
			}
		}
		fmt.Printf("неделя %d: пар %d, онлайн %d, со ссылкой BBB %d, по подгруппам %d, ИОТ %d\n", w, len(ls), online, bbb, sub, iet)
		for i, l := range ls {
			if i >= 4 {
				break
			}
			fmt.Printf("  %s %s %s · %s · подгр %d · online=%v · bbb=%v\n", l.Date, l.Start, l.Discipline, l.Type, l.Subgroup, l.Online, l.BBBURL != "")
		}
	}
	if c.Session() == "" {
		fmt.Println("сессия: пусто — куки не сохранились")
		return false
	}
	// Повторный клиент на сохранённых куках — так работает синк между запусками.
	c2, err := lk.New("")
	if err != nil {
		fmt.Println("повторный клиент ЛК:", err)
		return false
	}
	c2.Restore(c.Session())
	if _, err := c2.Groups(ctx); err != nil {
		fmt.Println("сессия из кук:", err)
		return false
	}
	fmt.Println("сессия из кук: ok")
	return true
}

func checkMoodle(ctx context.Context, login, pass string, loc *time.Location) bool {
	fmt.Println("== Moodle ==")
	c, err := moodle.New("")
	if err != nil {
		fmt.Println("клиент Moodle:", err)
		return false
	}
	if err := c.Login(ctx, login, pass); err != nil {
		fmt.Println("вход:", err)
		return false
	}
	fmt.Println("вход: ok")
	courses, err := c.Courses(ctx)
	if err != nil {
		fmt.Println("курсы:", err)
		return false
	}
	fmt.Printf("курсов: %d\n", len(courses))
	evs, err := c.Timeline(ctx, time.Now().AddDate(0, 0, -14), time.Time{})
	if err != nil {
		fmt.Println("лента:", err)
		return false
	}
	fmt.Printf("в ленте сроков: %d\n", len(evs))
	shown := 0
	for _, cr := range courses {
		acts, err := c.CourseActivities(ctx, cr)
		if err != nil {
			fmt.Println("курс", cr.ID, err)
			continue
		}
		withDue := 0
		for i, a := range acts {
			if i >= 6 { // на курс — первые шесть, чтобы не долбить сайт
				break
			}
			d, err := c.ActivityDetail(ctx, a, loc)
			if err != nil {
				fmt.Println("  ", a.CMID, err)
				continue
			}
			if d.Due != nil {
				withDue++
			}
			if shown < 8 {
				shown++
				due := "—"
				if d.Due != nil {
					due = d.Due.Format("02.01 15:04")
				}
				todo := ""
				if len(d.Todo) > 0 {
					todo = " · " + trim(d.Todo[0], 60)
				}
				fmt.Printf("  [%s] %s / %s · до %s · %s%s\n", a.Kind, trim(cr.FullName, 30), trim(a.Name, 40), due, d.Status, todo)
			}
			time.Sleep(150 * time.Millisecond)
		}
		fmt.Printf("курс %q: элементов со сроками-кандидатов %d, проверено %d, со сроком %d\n", trim(cr.FullName, 40), len(acts), min(6, len(acts)), withDue)
	}
	return true
}

func errOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func trim(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
