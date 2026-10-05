package tg

import (
	"fmt"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const (
	introText      = "Захожу на онлайн-пары вместо тебя: в списке BBB будет твоё ФИО, без микрофона и камеры.\n\n<b>Как тебя записать в журнал?</b>\nФамилия Имя Отчество, как в ведомости."
	badFIO         = "Не похоже на ФИО. Нужно 2–4 слова, как в ведомости: Фамилия Имя Отчество."
	askFIO         = "Напиши ФИО как в ведомости — три слова: Фамилия Имя Отчество.\nПод этим именем зайду в комнату."
	askSub         = "Какая подгруппа? Чужие подгрупповые пары пропускаю."
	askWords       = "На лекции слушаю короткие слова и пишу тебе, если препод их сказал.\nУже есть: фамилия, «тест», «контрольная», «мудл».\nМожно добавить свои."
	askWordsNext   = "Напиши свои слова через запятую — например: лаба, зачёт.\n«-» — убрать свои, базовые останутся."
	askBBBLink     = "Ссылка нужна <b>на эту пару</b>. Пришли bbb.ssau.ru/b/… — без неё не зайду."
	helpText       = "<b>Что умею</b>\nЗахожу на онлайн-пары вместо тебя: в BBB в списке твоё ФИО, микрофон выключен.\n\n<b>Кнопки внизу</b>\nПары — весь день и зайду ли на онлайн; /week — неделя\nКонспекты — PDF после полуночи\nПрофиль — имя в журнале, подгруппа, слова\n\n<b>Как это работает</b>\nЗа 15 минут спрошу: заходить? Молчишь — зайду за 5 минут до звонка.\nСсылку bbb.ssau.ru/b/… кинь перед парой — привяжу к ней и запомню для этого предмета.\nОтключиться можно кнопкой в карточке пары."
	fallbackText   = "Не понял. Внизу три кнопки: Пары, Конспекты, Профиль."
	noBBBTarget    = "Не понял, к какой паре ссылка. Открой Профиль → Комнаты BBB или пришли bbb.ssau.ru/b/… ближе к паре."
	testNeedURL    = "Кинь ссылку bbb.ssau.ru/b/… — сразу покажу кнопки захода."
	testWorkerHint = "Захожу за несколько секунд. Если тихо — служба ssau-bbb не запущена."
	notesEmpty     = "Готовых конспектов пока нет.\nНа лекции пишу звук, PDF собираю после полуночи — кнопка появится здесь."
	notesHint      = "Готовый PDF — кнопкой под сообщением."
	botShortDesc   = "Захожу на онлайн-пары СГАУ вместо тебя"
	botDescription = "Гость в BBB без микрофона. За 15 минут спрошу, заходить ли. Молчишь — зайду за 5 минут до пары. Конспекты — кнопкой «Конспекты»."
	inputHint      = "Пары, Конспекты или Профиль"
	fioHint        = "Фамилия Имя Отчество"
	changeHint     = "Передумать можно в карточке пары."
	cancelText     = "Отменил. Внизу три кнопки: Пары, Конспекты, Профиль."
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
	logx.Debugf("tg", "isMenuLabel: %q", strings.TrimSpace(text))
	switch strings.TrimSpace(text) {
	case btnToday, btnTodayOld, btnNotes, btnSettings, btnSettingsOld, btnLinks, btnWords, btnHelp:
		return true
	default:
		return false
	}
}

func formatWakeReply(u model.User) string {
	logx.Debugf("tg", "formatWakeReply: tg=%d", u.TelegramID)
	base := strings.Join(u.WakeList(), ", ")
	extra := model.FormatWakeWords(u.ExtraWords)
	if extra == "" {
		extra = "пока нет"
	}
	return fmt.Sprintf("На лекции напишу, если услышу: <b>%s</b>.\nСвои добавки: %s", esc(base), esc(extra))
}

func formatOnboardDone(u model.User) string {
	logx.Debugf("tg", "formatOnboardDone: tg=%d subgroup=%d", u.TelegramID, u.Subgroup)
	return fmt.Sprintf("<b>Готово.</b>\nВ журнале — %s, подгруппа %d.\n\nВнизу: Пары, Конспекты, Профиль.\nЗа 15 мин спрошу. Молчишь — зайду за 5 мин до начала.",
		esc(u.FIO), u.Subgroup)
}

func formatSettings(u model.User) string {
	logx.Debugf("tg", "formatSettings: tg=%d subgroup=%d", u.TelegramID, u.Subgroup)
	fio := strings.TrimSpace(u.FIO)
	if fio == "" {
		fio = "не задано"
	}
	extra := model.FormatWakeWords(u.ExtraWords)
	if extra == "" {
		extra = "нет"
	}
	return fmt.Sprintf(
		"<b>Профиль</b>\n\nИмя в журнале: %s\nПод этим ФИО захожу в BBB. Камеру и микрофон не включаю.\n\nПодгруппа: %d\nПары другой подгруппы пропускаю.\n\nПинг на лекции\nВсегда: %s\nТвои слова: %s\nЕсли препод скажет — напишу сюда.",
		esc(fio), u.Subgroup, esc(strings.Join(u.WakeList(), ", ")), esc(extra),
	)
}

func lessonStamp(l model.Lesson, loc *time.Location) string {
	logx.Debugf("tg", "lessonStamp: lesson=%d loc=%v", l.ID, loc)
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
	logx.Debugf("tg", "formatLessonHead: lesson=%d", l.ID)
	var b strings.Builder
	b.WriteString(bold(dashOr(l.Discipline)))
	if strings.TrimSpace(l.Teacher) != "" {
		b.WriteString("\n")
		b.WriteString(esc(l.Teacher))
	}
	b.WriteString("\n")
	b.WriteString(code(lessonStamp(l, loc)))
	return b.String()
}

func untilPhrase(now, begin time.Time) string {
	logx.Debugf("tg", "untilPhrase: now=%s begin=%s", now.Format(time.RFC3339), begin.Format(time.RFC3339))
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
	logx.Debugf("tg", "formatT15Card: lesson=%d hasLink=%v", l.ID, hasLink)
	var b strings.Builder
	b.WriteString("<b>")
	b.WriteString(untilPhrase(now, l.Begin))
	b.WriteString("</b>\n\n")
	b.WriteString(formatLessonHead(l, loc))
	b.WriteString("\n\nЗайти за тебя? Если не ответишь — зайду за 5 минут до звонка.\nБез микрофона, имя в списке как в журнале.")
	if !hasLink {
		b.WriteString("\n\n")
		b.WriteString(askBBBLink)
	} else {
		b.WriteString("\n\nСсылка этой пары уже есть. Другая комната — пришли новый bbb.ssau.ru/b/…")
	}
	return b.String()
}

func formatJoinAck(l *model.Lesson, loc *time.Location, fio string, hasLink bool) string {
	logx.Debugf("tg", "formatJoinAck: hasLink=%v fio=%q", hasLink, fio)
	var b strings.Builder
	if l != nil {
		b.WriteString(formatLessonHead(*l, loc))
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(fio) != "" {
		fmt.Fprintf(&b, "Ок, зайду не дожидаясь звонка как <b>%s</b>.\nБез микрофона. Если не пустят — напишу.", esc(fio))
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
	logx.Debugf("tg", "formatSkipAck: lesson=%v", l)
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
	logx.Debugf("tg", "formatSavedLink: lesson=%d discipline=%q", l.ID, l.Discipline)
	title := strings.TrimSpace(l.Discipline)
	if title == "" {
		title = "паре"
	} else {
		title = "«" + title + "»"
	}
	stamp := strings.TrimSpace(l.SlotLabel() + " " + l.Date)
	if stamp == "–" || stamp == "" {
		return "Привязал ссылку к этой паре. На следующих неделях для этого предмета и препода подставлю её сам."
	}
	return fmt.Sprintf("Привязал ссылку к %s %s.\nНа следующих неделях для этого предмета и препода подставлю её сам. Сменилась комната — просто пришли новую.", esc(title), esc(stamp))
}

type todayRow struct {
	Lesson   model.Lesson
	HasLink  bool
	Decision model.JoinDecision
	Presence string
	Detail   string
}

var weekdaysRU = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

func dayTitle(t time.Time) string {
	return t.Format("02.01") + ", " + weekdaysRU[t.Weekday()]
}

// formatToday — весь день: и очные пары (с аудиторией), и прошедшие. Раньше
// показывались только онлайн-пары, которые ещё не кончились: в день военной
// кафедры бот писал «пар больше нет» — «не знает расписание».
func formatToday(now time.Time, loc *time.Location, fio string, rows []todayRow) string {
	logx.Debugf("tg", "formatToday: rows=%d fio=%q", len(rows), fio)
	var b strings.Builder
	if loc != nil {
		now = now.In(loc)
	}
	fmt.Fprintf(&b, "<b>Сегодня, %s</b>", dayTitle(now))
	if strings.TrimSpace(fio) != "" {
		fmt.Fprintf(&b, "\nВ журнале: %s", esc(fio))
	}
	if len(rows) == 0 {
		b.WriteString("\n\nСегодня пар нет.")
		return b.String()
	}
	var past, nowRows, later []todayRow
	for _, r := range rows {
		l := r.Lesson
		switch {
		case !l.Finish.IsZero() && !now.Before(l.Finish):
			past = append(past, r)
		case !l.Begin.IsZero() && !now.Before(l.Begin):
			nowRows = append(nowRows, r)
		default:
			later = append(later, r)
		}
	}
	if len(nowRows) > 0 {
		b.WriteString("\n\n<b>Сейчас</b>")
		for _, r := range nowRows {
			writeTodayRow(&b, r, loc)
		}
	}
	if len(later) > 0 {
		b.WriteString("\n\n<b>Дальше</b>")
		for _, r := range later {
			writeTodayRow(&b, r, loc)
		}
	}
	if len(past) > 0 {
		b.WriteString("\n\n<b>Прошли</b>")
		for _, r := range past {
			fmt.Fprintf(&b, "\n• %s · %s", esc(dashOr(r.Lesson.Discipline)), code(r.Lesson.SlotLabel()))
		}
	}
	if len(nowRows) == 0 && len(later) == 0 {
		b.WriteString("\n\nНа сегодня всё.")
	}
	return b.String()
}

// formatDayBlock — пары другого дня (ближайший день с парами, неделя).
func formatDayBlock(title string, rows []todayRow, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>", esc(title))
	for _, r := range rows {
		writeTodayRow(&b, r, loc)
	}
	return b.String()
}

func writeTodayRow(b *strings.Builder, r todayRow, loc *time.Location) {
	logx.Debugf("tg", "writeTodayRow: lesson=%d hasLink=%v decision=%s presence=%s", r.Lesson.ID, r.HasLink, r.Decision, r.Presence)
	l := r.Lesson
	fmt.Fprintf(b, "\n• %s · %s", bold(dashOr(l.Discipline)), code(l.SlotLabel()))
	if t := strings.TrimSpace(l.Type); t != "" && t != "unknown" {
		fmt.Fprintf(b, " · %s", esc(t))
	}
	if l.Subgroup > 0 {
		fmt.Fprintf(b, " · подгр. %d", l.Subgroup)
	}
	if note := todayNote(r); note != "" {
		fmt.Fprintf(b, "\n  %s", note)
	}
}

func todayNote(r todayRow) string {
	logx.Debugf("tg", "todayNote: lesson=%d decision=%s presence=%s", r.Lesson.ID, r.Decision, r.Presence)
	if !r.Lesson.Online {
		place := strings.TrimSpace(r.Lesson.Place)
		if place == "" {
			return "очно"
		}
		return "очно · " + esc(place)
	}
	switch r.Presence {
	case model.PresenceRoom:
		return "в комнате"
	case model.PresenceLobby:
		return "лобби, жду модератора"
	case model.PresenceError:
		if r.Detail != "" {
			return esc(r.Detail)
		}
		return "ошибка захода"
	}
	switch r.Decision {
	case model.JoinNo:
		return "онлайн · пропускаю"
	case model.JoinYes:
		if r.HasLink {
			return "онлайн · зайду за тебя"
		}
		return "онлайн · зайду, но нет ссылки на комнату"
	case model.JoinPending:
		if r.HasLink {
			return "онлайн · молчу — зайду за 5 мин"
		}
		return "онлайн · нужна ссылка комнаты bbb.ssau.ru/b/…"
	}
	if r.HasLink {
		return "онлайн · ссылка есть"
	}
	return "онлайн · нет ссылки"
}

func formatWordsHint() string {
	logx.Debugf("tg", "formatWordsHint: build")
	return askWordsNext
}

func formatLinkList(upcoming []string, saved []string) string {
	logx.Debugf("tg", "formatLinkList: upcoming=%d saved=%d", len(upcoming), len(saved))
	var b strings.Builder
	b.WriteString("<b>Комнаты BBB</b>\n\nПришли сюда ссылку bbb.ssau.ru/b/… — запомню на пару. Один раз хватит.")
	b.WriteString("\n\n<b>Ждут ссылку</b>\n")
	if len(upcoming) == 0 {
		b.WriteString("сейчас все комнаты известны")
	} else {
		b.WriteString(esc(strings.Join(upcoming, "\n")))
	}
	if len(saved) > 0 {
		b.WriteString("\n\n<b>Уже запомнил</b>\n")
		b.WriteString(esc(strings.Join(saved, "\n")))
	}
	return b.String()
}

func formatTestCard(j model.TestJoin) string {
	logx.Debugf("tg", "formatTestCard: status=%s want=%s mode=%s", j.Status, j.Want, j.Mode)
	var b strings.Builder
	active := j.Want != model.TestWantOff &&
		(j.Status == model.TestJoining || j.Status == model.TestLobby || j.Status == model.TestRoom)

	if active {
		b.WriteString("<b>Сейчас тест</b>\n\n")
		b.WriteString("Ссылка: ")
		if strings.TrimSpace(j.URL) == "" {
			b.WriteString("нет")
		} else {
			b.WriteString(hlink(strings.TrimSpace(j.URL), strings.TrimSpace(j.URL)))
		}
		b.WriteString("\nИмя в BBB: ")
		b.WriteString(bold(j.GuestName()))
		b.WriteString("\nСтатус: ")
		b.WriteString(testStatusLine(j))
		b.WriteString("\nРежим: ")
		if j.Want == model.TestWantListen || j.Mode == model.TestWantListen {
			b.WriteString("со звуком")
		} else {
			b.WriteString("болванчик")
		}
		b.WriteString("\nПока не выйдешь — сам не отключится.")
		if j.Status == model.TestJoining {
			b.WriteString("\n")
			b.WriteString(testWorkerHint)
		}
		return b.String()
	}

	b.WriteString("<b>Тест BBB</b>\n\nОтдельная комната: ссылка сюда не пишется в пары.\nВ списке зайду как «")
	b.WriteString(esc(j.GuestName()))
	b.WriteString("».\nСсылка / имя: кнопки ниже или /test &lt;url&gt;")
	url := strings.TrimSpace(j.URL)
	if url == "" {
		b.WriteString("\n\nСсылки нет. Жми «Ссылка» или пришли bbb.ssau.ru/b/…")
		return b.String()
	}
	b.WriteString("\n\n")
	b.WriteString(hlink(url, url))
	b.WriteString("\n\nСейчас: ")
	b.WriteString(testStatusLine(j))
	return b.String()
}

func testStatusLine(j model.TestJoin) string {
	logx.Debugf("tg", "testStatusLine: status=%s want=%s", j.Status, j.Want)
	switch j.Status {
	case model.TestJoining:
		return "захожу…"
	case model.TestLobby:
		return "лобби, жду модератора"
	case model.TestRoom:
		if j.Want == model.TestWantListen || j.Mode == model.TestWantListen {
			return "в комнате · слушаю"
		}
		return "в комнате · болванчик"
	case model.TestError:
		if j.Message != "" {
			return "ошибка: " + esc(j.Message)
		}
		return "ошибка захода"
	default:
		return "не в комнате"
	}
}

func packDay(date string) string {
	logx.Debugf("tg", "packDay: %q", date)
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		logx.Debugf("tg", "packDay: parse %q: %v", date, err)
		return date
	}
	return t.Format("02.01")
}

func notesButtonLabel(p model.LecturePack) string {
	logx.Debugf("tg", "notesButtonLabel: pack=%d discipline=%q", p.ID, p.Discipline)
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
	logx.Debugf("tg", "formatNotesList: ready=%d pending=%d", len(ready), len(pending))
	if len(ready) == 0 && len(pending) == 0 {
		return notesEmpty
	}
	var b strings.Builder
	b.WriteString("<b>Конспекты лекций</b>")
	if len(ready) > 0 {
		b.WriteString("\n\nГотовы — жми кнопку:")
		for i, p := range ready {
			fmt.Fprintf(&b, "\n%d. %s (%s)", i+1, esc(archiveLabel(p)), packDay(p.Date))
		}
	}
	if len(pending) > 0 {
		b.WriteString("\n\nЕщё собираю:")
		for _, p := range pending {
			fmt.Fprintf(&b, "\n• %s — %s", esc(archiveLabel(p)), notesStatus(p.Status))
		}
	}
	b.WriteString("\n\n")
	b.WriteString(notesHint)
	return b.String()
}

func archiveLabel(p model.LecturePack) string {
	logx.Debugf("tg", "archiveLabel: pack=%d", p.ID)
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
	logx.Debugf("tg", "notesStatus: %q", st)
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
	case model.PackEmpty:
		return "записи нет — конспекта не будет"
	default:
		return st
	}
}
