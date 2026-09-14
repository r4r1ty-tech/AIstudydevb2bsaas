package tg

import (
	"fmt"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const (
	introText      = "Захожу на онлайн-пары вместо тебя: в BBB в списке будет твоё ФИО, без микрофона и камеры.\n\nКак тебя записать в журнал? Фамилия Имя Отчество, как в ведомости."
	askFIO         = "Напиши ФИО как в ведомости — три слова: Фамилия Имя Отчество.\nПод этим именем зайду в комнату."
	askSub         = "Какая подгруппа? Чужие подгрупповые пары пропускаю."
	askWords       = "На лекции слушаю короткие слова и пишу тебе, если препод их сказал.\nУже есть: фамилия, «тест», «контрольная», «мудл».\nМожно добавить свои через запятую или пропустить."
	askWordsNext   = "Напиши свои слова через запятую — например: лаба, зачёт.\n«-» — убрать только свои, базовые останутся."
	askBBBLink     = "Ссылка нужна на ЭТУ пару, не на предмет на семестр. Пришли bbb.ssau.ru/b/… — без неё не зайду."
	helpText       = "Внизу три кнопки.\n\nПары — что сегодня и зайду ли.\nКонспекты — PDF после полуночи.\nПрофиль — имя в журнале, подгруппа, слова для пинга.\n\nЗа 15 мин до онлайн-пары спрошу. «Зайти за меня» — иду сразу. Молчишь — зайду за 5 мин до звонка, без микрофона.\nСсылку bbb.ssau.ru/b/… кинь перед каждой парой — привяжу только к этой."
	fallbackText   = "Не понял. Внизу три кнопки: Пары, Конспекты, Профиль."
	noBBBTarget    = "Не понял, к какой паре ссылка. Открой Профиль → Комнаты BBB или пришли bbb.ssau.ru/b/… ближе к паре."
	notesEmpty     = "Готовых конспектов пока нет.\nНа лекции пишу звук, PDF собираю после полуночи — кнопка появится здесь."
	notesHint      = "Готовый PDF — кнопкой под сообщением."
	botShortDesc   = "Захожу на онлайн-пары СГАУ вместо тебя"
	botDescription = "Гость в BBB без микрофона. За 15 минут спрошу, заходить ли. Молчишь — зайду за 5 минут до пары. Конспекты — кнопкой «Конспекты»."
	inputHint      = "Пары, Конспекты или Профиль"
	fioHint        = "Фамилия Имя Отчество"
	changeHint     = "Передумать можно в карточке пары."
)

const (
	btnToday       = "Пары"
	btnTodayOld    = "Сегодня"
	btnNotes       = "Конспекты"
	btnSettings    = "Профиль"
	btnSettingsOld = "Настройки"
	btnLinks       = "Ссылки"
	btnWords       = "Слова"
	btnHelp        = "Помощь"
)

func isMenuLabel(text string) bool {
	switch strings.TrimSpace(text) {
	case btnToday, btnTodayOld, btnNotes, btnSettings, btnSettingsOld, btnLinks, btnWords, btnHelp:
		return true
	default:
		return false
	}
}

func formatWakeReply(u model.User) string {
	base := strings.Join(u.WakeList(), ", ")
	extra := model.FormatWakeWords(u.ExtraWords)
	if extra == "" {
		extra = "пока нет"
	}
	return fmt.Sprintf("На лекции напишу, если услышу: %s.\nСвои добавки: %s", base, extra)
}

func formatOnboardDone(u model.User) string {
	return fmt.Sprintf("Готово. В журнале — %s, подгруппа %d.\n\nВнизу: Пары, Конспекты, Профиль.\nЗа 15 мин спрошу. Молчишь — зайду за 5 мин до начала.",
		u.FIO, u.Subgroup)
}

func formatSettings(u model.User) string {
	fio := strings.TrimSpace(u.FIO)
	if fio == "" {
		fio = "не задано"
	}
	extra := model.FormatWakeWords(u.ExtraWords)
	if extra == "" {
		extra = "нет"
	}
	return fmt.Sprintf(
		"Профиль\n\nИмя в журнале\n%s\nПод этим ФИО захожу в BBB. Камеру и микрофон не включаю.\n\nПодгруппа: %d\nПары другой подгруппы пропускаю.\n\nПинг на лекции\nВсегда: %s\nТвои слова: %s\nЕсли препод скажет — напишу сюда.",
		fio, u.Subgroup, strings.Join(u.WakeList(), ", "), extra,
	)
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
	b.WriteString("\n\nСсылка — только на эту пару. Прошлые комнаты того же предмета не беру.")
	if !hasLink {
		b.WriteString("\n")
		b.WriteString(askBBBLink)
	} else {
		b.WriteString("\nСсылка этой пары уже есть. Другая комната — пришли новый bbb.ssau.ru/b/…")
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

func formatSavedLink(l model.Lesson) string {
	title := strings.TrimSpace(l.Discipline)
	if title == "" {
		title = "паре"
	} else {
		title = "«" + title + "»"
	}
	stamp := strings.TrimSpace(l.SlotLabel() + " " + l.Date)
	if stamp == "–" || stamp == "" {
		return "Привязал ссылку к этой паре. На следующую сам не перенесу — кинь заново."
	}
	return fmt.Sprintf("Привязал ссылку к %s %s.\nНа другую пару этот URL не пойдёт — перед следующей кинь заново.", title, stamp)
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
		return "сегодня пропускаю"
	case model.JoinYes:
		if r.HasLink {
			return "зайду за тебя"
		}
		return "зайду, но нет ссылки на комнату"
	case model.JoinPending:
		if r.HasLink {
			return "молчу — зайду за 5 мин"
		}
		return "нужна ссылка комнаты bbb.ssau.ru/b/…"
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
	b.WriteString("Комнаты BBB\n\nПришли сюда ссылку bbb.ssau.ru/b/… — запомню на предмет. Один раз хватит.")
	b.WriteString("\n\nЖдут ссылку:\n")
	if len(upcoming) == 0 {
		b.WriteString("сейчас все комнаты известны")
	} else {
		b.WriteString(strings.Join(upcoming, "\n"))
	}
	if len(saved) > 0 {
		b.WriteString("\n\nУже запомнил:\n")
		b.WriteString(strings.Join(saved, "\n"))
	}
	return b.String()
}

func packDay(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.Format("02.01")
}

func notesButtonLabel(p model.LecturePack) string {
	d := strings.TrimSpace(p.Discipline)
	if d == "" {
		d = "лекция"
	}
	if rs := []rune(d); len(rs) > 28 {
		d = string(rs[:27]) + "…"
	}
	return fmt.Sprintf("%s · %d", d, p.Number)
}

func formatNotesList(ready, pending []model.LecturePack) string {
	if len(ready) == 0 && len(pending) == 0 {
		return notesEmpty
	}
	var b strings.Builder
	b.WriteString("Конспекты лекций")
	if len(ready) > 0 {
		b.WriteString("\n\nГотовы — жми кнопку:")
		for i, p := range ready {
			fmt.Fprintf(&b, "\n%d. %s (%s)", i+1, archiveLabel(p), packDay(p.Date))
		}
	}
	if len(pending) > 0 {
		b.WriteString("\n\nЕщё собираю:")
		for _, p := range pending {
			fmt.Fprintf(&b, "\n• %s — %s", archiveLabel(p), notesStatus(p.Status))
		}
	}
	b.WriteString("\n\n")
	b.WriteString(notesHint)
	return b.String()
}

func archiveLabel(p model.LecturePack) string {
	d := strings.TrimSpace(p.Discipline)
	if d == "" {
		d = "лекция"
	}
	n := p.Number
	if n < 1 {
		n = 1
	}
	return fmt.Sprintf("%s · лекция %d", d, n)
}

func notesStatus(st string) string {
	switch st {
	case model.PackRecording:
		return "пишу звук"
	case model.PackRecorded:
		return "звук есть, PDF после полуночи"
	case model.PackSlides:
		return "слайды сняты, PDF после полуночи"
	case model.PackNotes, model.PackTranscribe:
		return "собираю PDF"
	case model.PackError:
		return "не собрался"
	default:
		return st
	}
}
