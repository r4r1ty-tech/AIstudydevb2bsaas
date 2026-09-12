package tg

import (
	"fmt"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const (
	introText      = "Захожу на онлайн-пары вместо тебя: в BBB в списке будет твоё ФИО, без микрофона и камеры.\n\nКак записать тебя в журнал? Фамилия Имя Отчество, как в ведомости."
	askFIO         = "Как записать тебя в BBB? Фамилия Имя Отчество, как в ведомости."
	askSub         = "Какая подгруппа?"
	askWords       = "Свои слова для пейджера — через запятую. Фамилия, «тест», «контрольная», «мудл» уже есть.\nМожно пропустить: пейджер на паре пока не орёт, это список на потом."
	askWordsNext   = "Напиши слова через запятую. «-» — сбросить свои."
	askBBBLink     = "Ещё нет ссылки на комнату. Пришли bbb.ssau.ru/b/… сюда — без неё не зайду."
	helpText       = "За 15 мин до онлайн-пары спрошу. «Зайти» — иду сразу. Молчишь — за 5 мин до звонка.\nВ BBB сижу гостем, без микрофона. Имя как в журнале.\n\nСсылку bbb.ssau.ru/b/… пришли один раз.\n\nКнопки внизу: Сегодня, Ссылки, Настройки.\nМеню «/» тоже работает.\n\nПейджер и запись пока выключены — сейчас захожу и сижу «только слушать»."
	fallbackText   = "Жми кнопки внизу: Сегодня, Ссылки, Настройки.\nИли пришли bbb.ssau.ru/b/…"
	noBBBTarget    = "Не понял, к какой паре это. Ближайших онлайн без ссылки нет. Открой Ссылки и пришли bbb.ssau.ru/b/… ближе к паре."
	botShortDesc   = "Захожу на онлайн-пары СГАУ вместо тебя"
	botDescription = "Гость в BBB без микрофона. За 15 минут спрошу, заходить ли. Молчишь — зайду за 5 минут до пары."
	inputHint      = "bbb.ssau.ru/b/… или кнопка"
	fioHint        = "Фамилия Имя Отчество"
	changeHint     = "Передумать — кнопки выше."
)

const (
	btnToday    = "Сегодня"
	btnLinks    = "Ссылки"
	btnSettings = "Настройки"
	btnWords    = "Слова"
	btnHelp     = "Помощь"
)

func isMenuLabel(text string) bool {
	switch strings.TrimSpace(text) {
	case btnToday, btnLinks, btnSettings, btnWords, btnHelp:
		return true
	default:
		return false
	}
}

func formatWakeReply(u model.User) string {
	list := u.WakeList()
	extra := model.FormatWakeWords(u.ExtraWords)
	if extra == "" {
		extra = "нет"
	}
	return fmt.Sprintf("Пейджер: %s\nСвои слова: %s", strings.Join(list, ", "), extra)
}

func formatOnboardDone(u model.User) string {
	return fmt.Sprintf("Готово. В BBB — %s, подгруппа %d.\n\nЗа 15 мин спрошу. Молчишь — зайду за 5 мин до начала.",
		u.FIO, u.Subgroup)
}

func formatSettings(u model.User) string {
	extra := model.FormatWakeWords(u.ExtraWords)
	if extra == "" {
		extra = "нет"
	}
	fio := strings.TrimSpace(u.FIO)
	if fio == "" {
		fio = "не задано"
	}
	return fmt.Sprintf("Настройки\n\nВ BBB: %s\nПодгруппа: %d\nПейджер: %s\nСвои слова: %s",
		fio, u.Subgroup, strings.Join(u.WakeList(), ", "), extra)
}

func lessonStamp(l model.Lesson, loc *time.Location) string {
	if !l.Begin.IsZero() {
		t := l.Begin
		if loc != nil {
			t = t.In(loc)
		}
		return l.SlotLabel() + " · " + t.Format("02.01")
	}
	if l.Date != "" {
		return l.SlotLabel() + " · " + l.Date
	}
	return l.SlotLabel()
}

func formatLessonHead(l model.Lesson, loc *time.Location) string {
	var b strings.Builder
	b.WriteString(l.Discipline)
	if l.Teacher != "" {
		b.WriteString("\n")
		b.WriteString(l.Teacher)
	}
	b.WriteString("\n")
	b.WriteString(lessonStamp(l, loc))
	return b.String()
}

func untilPhrase(now, begin time.Time) string {
	if begin.IsZero() || !begin.After(now) {
		return "Сейчас пара"
	}
	d := begin.Sub(now)
	mins := int((d + time.Minute/2) / time.Minute)
	if mins <= 1 {
		return "Через минуту пара"
	}
	if mins < 60 {
		return fmt.Sprintf("Через %d мин пара", mins)
	}
	h := mins / 60
	m := mins % 60
	if m == 0 {
		return fmt.Sprintf("Через %d ч пара", h)
	}
	return fmt.Sprintf("Через %d ч %d мин пара", h, m)
}

func formatT15Card(l model.Lesson, now time.Time, loc *time.Location, hasLink bool) string {
	var b strings.Builder
	b.WriteString(untilPhrase(now, l.Begin))
	b.WriteString("\n\n")
	b.WriteString(formatLessonHead(l, loc))
	b.WriteString("\n\nЗайти за тебя? Если не ответишь — зайду за 5 минут до звонка.\nБез микрофона, имя в списке как в журнале.")
	if !hasLink {
		b.WriteString("\n\n")
		b.WriteString(askBBBLink)
	}
	return b.String()
}

func formatJoinAck(l *model.Lesson, loc *time.Location, fio string, hasLink bool) string {
	var b strings.Builder
	if l != nil {
		b.WriteString(formatLessonHead(*l, loc))
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(fio) != "" {
		fmt.Fprintf(&b, "Ок, зайду не дожидаясь звонка как %s.\nБез микрофона. Если не пустят — напишу.", fio)
	} else {
		b.WriteString("Ок, зайду не дожидаясь звонка. Без микрофона. Если не пустят — напишу.")
	}
	if l != nil && !hasLink {
		b.WriteString("\n\n")
		b.WriteString(askBBBLink)
	}
	b.WriteString("\n")
	b.WriteString(changeHint)
	return b.String()
}

func formatSkipAck(l *model.Lesson, loc *time.Location) string {
	var b strings.Builder
	if l != nil {
		b.WriteString(formatLessonHead(*l, loc))
		b.WriteString("\n\n")
	}
	b.WriteString("Ок, сегодня пропускаю.\n")
	b.WriteString(changeHint)
	return b.String()
}

func formatSavedLink(discipline string) string {
	if strings.TrimSpace(discipline) == "" {
		return "Запомнил ссылку на комнату."
	}
	return fmt.Sprintf("Запомнил комнату для «%s». В следующий раз зайду сам.", discipline)
}

type todayRow struct {
	Lesson   model.Lesson
	HasLink  bool
	Decision model.JoinDecision
	Presence string
	Detail   string
}

func formatToday(now time.Time, loc *time.Location, fio string, rows []todayRow) string {
	var b strings.Builder
	day := now.Format("02.01")
	if loc != nil {
		day = now.In(loc).Format("02.01")
	}
	fmt.Fprintf(&b, "Сегодня, %s", day)
	if strings.TrimSpace(fio) != "" {
		fmt.Fprintf(&b, "\nВ журнале: %s", fio)
	}
	if len(rows) == 0 {
		b.WriteString("\n\nОнлайн-пар на сегодня больше нет.")
		return b.String()
	}

	var nowRows, later []todayRow
	for _, r := range rows {
		if !r.Lesson.Finish.IsZero() && !now.Before(r.Lesson.Begin) && now.Before(r.Lesson.Finish) {
			nowRows = append(nowRows, r)
		} else {
			later = append(later, r)
		}
	}
	if len(nowRows) > 0 {
		b.WriteString("\n\nСейчас")
		for _, r := range nowRows {
			writeTodayRow(&b, r, loc)
		}
	}
	if len(later) > 0 {
		b.WriteString("\n\nДальше")
		for _, r := range later {
			writeTodayRow(&b, r, loc)
		}
	}
	return b.String()
}

func writeTodayRow(b *strings.Builder, r todayRow, loc *time.Location) {
	l := r.Lesson
	fmt.Fprintf(b, "\n• %s · %s", l.Discipline, lessonStamp(l, loc))
	if note := todayNote(r); note != "" {
		fmt.Fprintf(b, "\n  %s", note)
	}
}

func todayNote(r todayRow) string {
	switch r.Presence {
	case model.PresenceRoom:
		return "в комнате"
	case model.PresenceLobby:
		return "лобби, жду модератора"
	case model.PresenceError:
		if r.Detail != "" {
			return r.Detail
		}
		return "ошибка захода"
	}
	switch r.Decision {
	case model.JoinNo:
		return "пропуск"
	case model.JoinYes:
		if r.HasLink {
			return "зайду"
		}
		return "зайду, но нет ссылки"
	case model.JoinPending:
		if r.HasLink {
			return "если молчишь — зайду"
		}
		return "нужна ссылка bbb.ssau.ru/b/…"
	}
	if r.HasLink {
		return "ссылка есть"
	}
	return "нет ссылки"
}

func formatWordsHint() string {
	return askWordsNext
}

func formatLinkList(upcoming []string, saved []string) string {
	var b strings.Builder
	b.WriteString("Онлайн впереди:\n")
	if len(upcoming) == 0 {
		b.WriteString("нет ближайших пар")
	} else {
		b.WriteString(strings.Join(upcoming, "\n"))
	}
	if len(saved) > 0 {
		b.WriteString("\n\nУже есть:\n")
		b.WriteString(strings.Join(saved, "\n"))
	}
	b.WriteString("\n\nПришли bbb.ssau.ru/b/… — привяжу к ближайшей паре без комнаты.")
	return b.String()
}
