package webapp

import (
	"fmt"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/archive"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

// Демо-данные строятся от текущего момента: сегодня одна онлайн-лекция идёт
// прямо сейчас, неделя вокруг заполнена, дедлайны лежат во всех группах.

const (
	subjOpt   = "Методы оптимизации и исследование операций"
	subjStat  = "Статистический анализ данных"
	subjTPIVS = "Технологии проектирования информационно-вычислительных систем"
	subjTFYAG = "Теория формальных языков и грамматик"
	subjSE    = "Программная инженерия"
	subjEng   = "Иностранный язык (английский)"
	subjPE    = "Физическая культура и спорт (элективная дисциплина)"

	typLecture  = "Лекция"
	typPractice = "Практика"
	typLab      = "Лабораторная"
)

type demoSlot struct {
	subj, typ, teacher, place string
	online                    bool
	subgroup                  int
}

var demoTeachers = map[string]string{
	subjOpt + typLecture:    "Лаврентьев Глеб Аркадьевич",
	subjOpt + typPractice:   "Соболева Ирина Петровна",
	subjStat + typLecture:   "Карташов Михаил Юрьевич",
	subjStat + typLab:       "Дьяконова Алина Сергеевна",
	subjTPIVS + typLecture:  "Белозёров Павел Андреевич",
	subjTPIVS + typPractice: "Белозёров Павел Андреевич",
	subjTFYAG + typLecture:  "Остапенко Вера Николаевна",
	subjTFYAG + typLab:      "Шайхутдинов Рустам Ильдарович",
	subjSE + typLecture:     "Мельников Станислав Олегович",
	subjSE + typPractice:    "Мельников Станислав Олегович",
	subjEng + typPractice:   "Гарипова Эльвира Рашидовна",
	subjPE + typPractice:    "Кузьмин Денис Валерьевич",
}

func on(subj, typ string) demoSlot {
	return demoSlot{subj: subj, typ: typ, teacher: demoTeachers[subj+typ], online: true}
}

func onSub(subj, typ string, sub int) demoSlot {
	s := on(subj, typ)
	s.subgroup = sub
	return s
}

func off(subj, typ, place string) demoSlot {
	return demoSlot{subj: subj, typ: typ, teacher: demoTeachers[subj+typ], place: place}
}

// Слоты расписания СГАУ.
var demoSlots = [][2]string{
	{"08:00", "09:35"}, {"09:45", "11:20"}, {"11:30", "13:05"},
	{"13:30", "15:05"}, {"15:15", "16:50"}, {"17:00", "18:35"},
}

// Обычная неделя: день недели (0 — понедельник) → слот → пара.
var demoWeek = map[int][]struct {
	slot int
	l    demoSlot
}{
	0: {{1, on(subjOpt, typLecture)}, {2, on(subjOpt, typPractice)}, {3, off(subjEng, typPractice, "3к. 412")}},
	1: {{0, on(subjStat, typLecture)}, {1, onSub(subjStat, typLab, 1)}, {1, onSub(subjStat, typLab, 2)}, {2, on(subjSE, typLecture)}},
	2: {{1, on(subjTFYAG, typLecture)}, {2, onSub(subjTFYAG, typLab, 1)}, {3, off(subjPE, typPractice, "Спорткомплекс")}},
	3: {{0, on(subjTPIVS, typLecture)}, {1, on(subjTPIVS, typPractice)}, {2, on(subjSE, typPractice)}},
	4: {{1, on(subjOpt, typLecture)}, {2, onSub(subjStat, typLab, 2)}, {3, off(subjEng, typPractice, "3к. 412")}, {4, on(subjTFYAG, typLecture)}},
	5: {{0, on(subjTPIVS, typLecture)}, {1, off(subjPE, typPractice, "Спорткомплекс")}},
}

var demoRooms = map[string]string{
	subjOpt + typLecture:   "https://bbb.ssau.ru/b/lav-k7m-q2x",
	subjOpt + typPractice:  "https://bbb.ssau.ru/b/sob-9fd-t4c",
	subjStat + typLecture:  "https://bbb.ssau.ru/b/kar-3hn-w8p",
	subjStat + typLab:      "https://bbb.ssau.ru/b/dya-6zt-m1r",
	subjTPIVS + typLecture: "https://bbb.ssau.ru/b/bel-2xq-v5k",
	subjTFYAG + typLecture: "https://bbb.ssau.ru/b/ost-8wr-c3n",
	subjTFYAG + typLab:     "https://bbb.ssau.ru/b/sha-4kp-y7d",
	subjSE + typLecture:    "https://bbb.ssau.ru/b/mel-5gb-h2s",
	subjSE + typPractice:   "https://bbb.ssau.ru/b/mel-5gb-h2s",
	// ТПИВС, практика — без ссылки: на ней видно подсказку «вставьте ссылку».
}

func newDemoSource(loc *time.Location, clock func() time.Time) *demoSource {
	d := &demoSource{
		loc:      loc,
		clock:    clock,
		users:    map[int64]model.User{},
		links:    map[string]model.BBBLink{},
		intents:  map[[2]int64]model.JoinIntent{},
		presence: map[int64]model.Presence{},
		topics:   map[int64]string{},
		sizes:    map[int64]int64{},
		test:     model.TestJoin{Status: model.TestIdle, Name: model.TestGuestName},
	}
	now := clock()
	d.seedUsers(now)
	cur := d.seedLessons(now)
	d.seedLinks(now)
	d.seedIntents(now, cur)
	d.seedPacks(now, cur)
	d.seedEvents(now, cur)
	d.moodle = &demoMoodle{clock: clock, connected: true, login: "sokolov.ad", syncedAt: now.Add(-7 * time.Minute).UTC(), list: demoDeadlines(now)}
	return d
}

func (d *demoSource) seedUsers(now time.Time) {
	created := now.AddDate(0, -2, 0).UTC()
	tonight := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location())
	for _, u := range []model.User{
		{TelegramID: demoAdminID, Username: "artem_s", FirstName: "Артём", FIO: "Соколов Артём Дмитриевич", Subgroup: 1, Enabled: true, Onboarded: true, OnboardStage: model.StageDone, ExtraWords: []string{"лаба", "дедлайн"}},
		{TelegramID: 700000002, Username: "dasha_kim", FirstName: "Дарья", FIO: "Ким Дарья Олеговна", Subgroup: 2, Enabled: true, Onboarded: true, OnboardStage: model.StageDone},
		{TelegramID: 700000003, FirstName: "Тимур", FIO: "Мухаметов Тимур Ильдарович", Subgroup: 1, Enabled: true, Onboarded: true, OnboardStage: model.StageDone, DisabledUntil: &tonight},
		{TelegramID: 700000004, Username: "polina_vr", FirstName: "Полина", FIO: "Воронцова Полина Андреевна", Subgroup: 2, Enabled: false, Onboarded: true, OnboardStage: model.StageDone, ExtraWords: []string{"зачёт"}},
	} {
		u.CreatedAt = created
		d.users[u.TelegramID] = u
	}
}

func (d *demoSource) addLesson(day time.Time, start, end string, s demoSlot) model.Lesson {
	parse := func(hm string) time.Time {
		t, _ := time.ParseInLocation("15:04", hm, d.loc)
		return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, d.loc)
	}
	return d.addLessonAt(parse(start), parse(end), s)
}

func (d *demoSource) addLessonAt(begin, finish time.Time, s demoSlot) model.Lesson {
	d.nextID++
	l := model.Lesson{
		ID: d.nextID, Date: begin.Format(dateLayout), Start: begin.Format("15:04"), End: finish.Format("15:04"),
		Begin: begin.UTC(), Finish: finish.UTC(), Discipline: s.subj, Teacher: s.teacher,
		Place: s.place, Subgroup: s.subgroup, Type: s.typ, Online: s.online,
	}
	d.lessons = append(d.lessons, l)
	return l
}

// seedLessons: две недели с понедельника текущей; сегодня — особый день вокруг
// «сейчас». Возвращает пару, которая идёт прямо сейчас.
func (d *demoSource) seedLessons(now time.Time) model.Lesson {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, d.loc)
	monday := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	var cur model.Lesson
	for i := 0; i < 14; i++ {
		day := monday.AddDate(0, 0, i)
		if day.Equal(today) {
			cur = d.seedToday(now)
			continue
		}
		for _, e := range demoWeek[i%7] {
			d.addLesson(day, demoSlots[e.slot][0], demoSlots[e.slot][1], e.l)
		}
	}
	return cur
}

func (d *demoSource) seedToday(now time.Time) model.Lesson {
	begin := now.Add(-38 * time.Minute).Truncate(5 * time.Minute)
	step := 105 * time.Minute
	dur := 95 * time.Minute
	at := func(n int) (time.Time, time.Time) {
		b := begin.Add(time.Duration(n) * step)
		return b, b.Add(dur)
	}
	b, f := at(-1)
	d.addLessonAt(b, f, on(subjSE, typPractice))
	b, f = at(0)
	cur := d.addLessonAt(b, f, on(subjOpt, typLecture))
	b, f = at(1)
	d.addLessonAt(b, f, onSub(subjTFYAG, typLab, 1))
	b, f = at(2)
	d.addLessonAt(b, f, off(subjEng, typPractice, "3к. 412"))
	b, f = at(3)
	d.addLessonAt(b, f, on(subjTPIVS, typPractice))
	return cur
}

func (d *demoSource) seedLinks(now time.Time) {
	for _, l := range d.lessons {
		if !l.Online {
			continue
		}
		u, ok := demoRooms[l.Discipline+l.Type]
		if !ok {
			continue
		}
		key := model.BBBRoomKey(l.Discipline, l.Teacher, l.Type)
		d.links[key] = model.BBBLink{Key: key, URL: u, UpdatedAt: now.AddDate(0, 0, -20).UTC()}
	}
}

func (d *demoSource) seedIntents(now time.Time, cur model.Lesson) {
	put := func(l model.Lesson, dec model.JoinDecision) {
		at := l.Begin.Add(-15 * time.Minute)
		in := model.JoinIntent{TelegramID: demoAdminID, LessonID: l.ID, Decision: dec, AskedAt: at}
		if dec != model.JoinPending {
			t := at.Add(2 * time.Minute)
			in.DecidedAt = &t
		}
		d.intents[[2]int64{demoAdminID, l.ID}] = in
	}
	put(cur, model.JoinPending)
	today := now.Format(dateLayout)
	tomorrow := now.AddDate(0, 0, 1).Format(dateLayout)
	weekday := (int(now.Weekday()) + 6) % 7
	// В пятницу этой недели — «заходить на все»; в понедельник следующей — «не заходить».
	friday := now.AddDate(0, 0, 4-weekday).Format(dateLayout)
	nextMonday := now.AddDate(0, 0, 7-weekday).Format(dateLayout)
	firstTomorrow := true
	for _, l := range d.lessons {
		if !l.Online || l.Date == today {
			continue
		}
		switch {
		case l.Date == friday:
			put(l, model.JoinYes)
		case l.Date == nextMonday:
			put(l, model.JoinNo)
		case l.Date == tomorrow && firstTomorrow && l.Subgroup != 2:
			put(l, model.JoinNo)
			firstTomorrow = false
		}
	}
	d.presence[demoAdminID] = model.Presence{TelegramID: demoAdminID, LessonID: cur.ID, State: model.PresenceRoom, Message: "в комнате", UpdatedAt: cur.Begin.Add(-4 * time.Minute)}
	d.presence[700000002] = model.Presence{TelegramID: 700000002, LessonID: cur.ID, State: model.PresenceLobby, Message: "ждёт модератора", UpdatedAt: now.Add(-3 * time.Minute).UTC()}
}

func (d *demoSource) seedPacks(now time.Time, cur model.Lesson) {
	type spec struct {
		subj   string
		topics []string
		tail   string // статус последнего пака
		err    string
	}
	specs := []spec{
		{subjOpt, []string{
			"Постановка задачи линейного программирования",
			"Графический метод и симплекс-метод",
			"Двойственность в линейном программировании",
			"Транспортная задача и метод потенциалов",
		}, model.PackRecording, ""},
		{subjStat, []string{
			"Выборка и описательные статистики",
			"Проверка статистических гипотез",
			"Корреляционный анализ",
		}, model.PackError, "STT: сервис расшифровки не ответил"},
		{subjTFYAG, []string{
			"Формальные грамматики и иерархия Хомского",
			"Конечные автоматы: ДКА и НКА",
			"Регулярные выражения и теорема Клини",
		}, model.PackEmpty, ""},
		{subjTPIVS, []string{
			"Жизненный цикл ИС и модели разработки",
			"Функциональное моделирование: IDEF0 и DFD",
		}, model.PackNotes, ""},
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, d.loc)
	for si, sp := range specs {
		total := len(sp.topics) + 1
		for n := 1; n <= total; n++ {
			date := day.AddDate(0, 0, -7*(total-n)-si)
			d.nextID++
			p := model.LecturePack{
				ID: d.nextID, Discipline: sp.subj, Number: n, Date: date.Format(dateLayout),
				Dir: archive.Rel(sp.subj, n), Status: model.PackDone, PublishStatus: "published",
				CreatedAt: date.UTC(), UpdatedAt: date.Add(14 * time.Hour).UTC(),
			}
			pub := p.UpdatedAt
			p.PublishedAt = &pub
			p.NotesPDF = p.Dir + "/notes.pdf"
			d.sizes[p.ID] = int64(2_400_000 + 310_000*n + 170_000*si)
			if n <= len(sp.topics) {
				d.topics[p.ID] = sp.topics[n-1]
			} else {
				p.Status = sp.tail
				p.PublishStatus = ""
				p.PublishedAt = nil
				p.NotesPDF = ""
				p.Err = sp.err
				p.Date = day.AddDate(0, 0, -1-si).Format(dateLayout)
				d.sizes[p.ID] = int64(31_000_000 + 4_000_000*si)
				if sp.tail == model.PackRecording {
					p.LessonID = cur.ID
					p.Date = cur.Date
					p.BBBURL = demoRooms[subjOpt+typLecture]
					d.sizes[p.ID] = int64(18_500_000)
				}
				if sp.tail == model.PackEmpty {
					d.sizes[p.ID] = 41_000
				}
			}
			d.packs = append(d.packs, p)
		}
	}
}

func (d *demoSource) seedEvents(now time.Time, cur model.Lesson) {
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute).UTC() }
	add := func(at time.Time, typ string, tg int64, lesson int64, msg string) {
		d.addEvent(model.Event{At: at, Type: typ, TelegramID: tg, LessonID: lesson, Message: msg})
	}
	begin := cur.Begin
	add(begin.Add(-15*time.Minute), model.EventT15, demoAdminID, cur.ID, cur.Discipline)
	add(begin.Add(-13*time.Minute), model.EventT15, 700000002, cur.ID, cur.Discipline)
	add(begin.Add(-4*time.Minute), model.EventJoin, demoAdminID, cur.ID, "зашёл в комнату: "+cur.Discipline)
	add(begin.Add(-3*time.Minute), model.EventRecord, demoAdminID, cur.ID, "запись началась: "+archive.Rel(cur.Discipline, 5))
	add(begin.Add(-1*time.Minute), model.EventLobby, 700000002, cur.ID, "ждёт модератора в лобби")
	add(ago(9), model.EventWake, demoAdminID, cur.ID, "услышал «контрольная»: «…контрольная будет через две недели, по симплекс-методу…»")
	add(ago(150), model.EventLeave, demoAdminID, cur.ID-1, "вышел: пара закончилась")
	add(ago(240), model.EventJoin, demoAdminID, cur.ID-1, "зашёл в комнату: "+subjSE)
	add(ago(25*60), model.EventNotes, 0, 0, fmt.Sprintf("конспект готов: %s", archive.Rel(subjTFYAG, 3)))
	add(ago(25*60+4), model.EventSlides, 0, 0, "слайды сохранены: 23 кадра")
	add(ago(26*60), model.EventError, 0, 0, "конспект не собран: сервис расшифровки не ответил, повтор через 10 минут")
	add(ago(31*60), model.EventNoBBB, demoAdminID, 0, "нет ссылки на комнату: "+subjTPIVS+" (практика)")
	add(ago(32*60), model.EventSkip, 700000003, 0, "отказался от пары: "+subjStat)
	add(ago(120), model.EventReparse, 0, 0, "расписание обновлено: 2 изменения")
	add(ago(3*24*60), model.EventOnboard, 700000004, 0, "настроил профиль")

	online := 0
	for _, l := range d.lessons {
		if l.Online {
			online++
		}
	}
	d.nextID++
	d.run = model.ParseRun{
		ID: d.nextID, At: ago(120), OK: true, Status: "ok", LessonCount: len(d.lessons), OnlineCount: online,
		Diff: "+ пт 13:30 " + subjStat + " (лабораторная, подгр. 2)\n~ чт 09:45 " + subjTPIVS + ": аудитория → онлайн",
	}
}

func demoDeadlines(now time.Time) []Deadline {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	at := func(days, h, m int) time.Time {
		return day.AddDate(0, 0, days).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	toMonday := (8 - int(day.Weekday())) % 7
	if toMonday == 0 {
		toMonday = 7
	}
	inWeek := toMonday - 1
	if inWeek < 2 {
		inWeek = 2
	}
	lms := func(kind string, id int) string {
		return fmt.Sprintf("https://lms.ssau.ru/mod/%s/view.php?id=%d", kind, id)
	}
	return []Deadline{
		{ID: "a-31877", Course: subjStat, Title: "Лабораторная работа №3. Регрессионный анализ", Kind: DeadlineAssign, Due: at(-2, 23, 59), Status: DeadlineDraft, URL: lms("assign", 31877)},
		{ID: "q-30411", Course: subjOpt, Title: "Тест по теме «Симплекс-метод»", Kind: DeadlineQuiz, Due: at(-1, 20, 0), Status: DeadlineTodo, URL: lms("quiz", 30411)},
		{ID: "a-32050", Course: subjTFYAG, Title: "Домашнее задание 4: построить ДКА по НКА", Kind: DeadlineAssign, Due: at(0, 23, 59), Status: DeadlineTodo, URL: lms("assign", 32050)},
		{ID: "q-32102", Course: subjSE, Title: "Тест: модели жизненного цикла ПО", Kind: DeadlineQuiz, Due: at(1, 18, 0), Status: DeadlineTodo, URL: lms("quiz", 32102)},
		{ID: "a-32119", Course: subjTPIVS, Title: "Практика 5: диаграмма IDEF0 для своей системы", Kind: DeadlineAssign, Due: at(1, 23, 59), Status: DeadlineDraft, URL: lms("assign", 32119)},
		{ID: "q-32240", Course: subjStat, Title: "Тест 2. Проверка гипотез", Kind: DeadlineQuiz, Due: at(inWeek, 12, 0), Status: DeadlineTodo, URL: lms("quiz", 32240)},
		{ID: "a-29870", Course: subjOpt, Title: "Курсовая работа: первый раздел", Kind: DeadlineAssign, Due: at(12, 23, 59), Status: DeadlineTodo, URL: lms("assign", 29870)},
		{ID: "q-32301", Course: subjTFYAG, Title: "Контрольная работа по конечным автоматам", Kind: DeadlineQuiz, Due: at(16, 10, 0), Status: DeadlineTodo, URL: lms("quiz", 32301)},
		{ID: "a-31640", Course: subjTPIVS, Title: "Практика 4: DFD-диаграмма", Kind: DeadlineAssign, Due: at(-3, 23, 59), Status: DeadlineSubmitted, URL: lms("assign", 31640)},
		{ID: "a-32007", Course: subjEng, Title: "Essay: My future profession", Kind: DeadlineAssign, Due: at(2, 23, 59), Status: DeadlineSubmitted, URL: lms("assign", 32007)},
	}
}
