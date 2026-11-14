package store

import (
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestPackSaveReloadAndLists(t *testing.T) {
	s := openTemp(t)
	root := t.TempDir()
	day1 := model.Lesson{ID: 10, Date: "2026-09-21", Discipline: "Сети"}
	day2 := model.Lesson{ID: 11, Date: "2026-09-22", Discipline: "Сети"}
	a, err := s.EnsurePack(day1, "", root)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.EnsurePack(day1, "https://bbb.ssau.ru/b/x", root); again.ID != a.ID || again.BBBURL != "https://bbb.ssau.ru/b/x" {
		t.Fatalf("second EnsurePack must reuse and fill the url: %+v", again)
	}
	b, err := s.EnsurePack(day2, "u", root)
	if err != nil || b.Number != 2 {
		t.Fatalf("second lecture number = %+v %v", b, err)
	}

	published := time.Date(2026, 9, 22, 20, 0, 0, 0, time.UTC)
	a.Status = model.PackDone
	a.Transcript = "Сети/лекция-1/transcript.txt"
	a.NotesPDF = "Сети/лекция-1/notes.pdf"
	a.PublishStatus = "published"
	a.PublishedAt = &published
	a.Err = "old"
	if err := s.SavePack(a); err != nil {
		t.Fatal(err)
	}
	got, err := s.PackByID(a.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Status != model.PackDone || got.Transcript != a.Transcript || got.NotesPDF != a.NotesPDF ||
		got.PublishStatus != "published" || got.PublishedAt == nil || !got.PublishedAt.Equal(published) || got.Err != "old" {
		t.Fatalf("reloaded = %+v", got)
	}
	if !got.UpdatedAt.After(a.CreatedAt.Add(-time.Second)) {
		t.Fatal("SavePack must bump updated_at")
	}
	if missing, err := s.PackByID(999); err != nil || missing != nil {
		t.Fatalf("missing = %+v %v", missing, err)
	}

	all, err := s.ListPacks()
	if err != nil || len(all) != 2 {
		t.Fatalf("ListPacks = %d %v", len(all), err)
	}
	d1, err := s.PacksByDate("2026-09-21")
	if err != nil || len(d1) != 1 || d1[0].ID != a.ID {
		t.Fatalf("PacksByDate = %+v %v", d1, err)
	}
	if none, _ := s.PacksByDate("2000-01-01"); len(none) != 0 {
		t.Fatal("no packs that day")
	}
	rec, err := s.AnyRecording()
	if err != nil || !rec {
		t.Fatalf("pack b is recording: %v %v", rec, err)
	}
	var nilStore *Store
	if _, err := nilStore.EnsurePack(day1, "", root); err == nil {
		t.Fatal("nil store")
	}
}

func TestLessonBBBAndPresence(t *testing.T) {
	s := openTemp(t)
	if s.GetLessonBBB(0) != "" || s.GetLessonBBB(5) != "" {
		t.Fatal("no link yet")
	}
	if err := s.SetLessonBBB(0, "x"); err == nil {
		t.Fatal("lesson id 0 must be rejected")
	}
	if err := s.SetLessonBBB(5, "  https://bbb.ssau.ru/b/aaa  "); err != nil {
		t.Fatal(err)
	}
	if got := s.GetLessonBBB(5); got != "https://bbb.ssau.ru/b/aaa" {
		t.Fatalf("link = %q", got)
	}
	var nilStore *Store
	if nilStore.GetLessonBBB(5) != "" {
		t.Fatal("nil store")
	}

	if p, err := s.GetPresence(7); err != nil || p != nil {
		t.Fatalf("no presence yet: %+v %v", p, err)
	}
	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	if err := s.SetPresence(model.Presence{TelegramID: 7, LessonID: 5, State: model.PresenceLobby, Message: "ждём", UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetPresence(7)
	if err != nil || p == nil || p.State != model.PresenceLobby || p.LessonID != 5 || p.Message != "ждём" || !p.UpdatedAt.Equal(at) {
		t.Fatalf("presence = %+v %v", p, err)
	}
}

// Every method must turn a dead database into an error, never a panic.
func TestClosedStoreReturnsErrors(t *testing.T) {
	s := openTemp(t)
	if err := s.db.Close(); err != nil { // the db dies, the Store stays
		t.Fatal(err)
	}
	now := time.Now()
	l := model.Lesson{ID: 1, Date: "2026-09-26", Discipline: "Сети"}
	errs := map[string]error{}
	add := func(name string, err error) { errs[name] = err }

	_, err := s.GetUser(1)
	add("GetUser", err)
	add("UpsertUser", s.UpsertUser(&model.User{TelegramID: 1}))
	_, err = s.ListUsers()
	add("ListUsers", err)
	add("SetEnabled", s.SetEnabled(1, true))
	add("SetDisabledUntil", s.SetDisabledUntil(1, &now))
	add("SetFIO", s.SetFIO(1, "x"))
	add("SetSubgroup", s.SetSubgroup(1, 2))
	add("SetExtraWords", s.SetExtraWords(1, []string{"a"}))
	add("SetOnboardStage", s.SetOnboardStage(1, 2))
	add("ReplaceLessons", s.ReplaceLessons([]model.Lesson{l}))
	_, err = s.ListLessons()
	add("ListLessons", err)
	_, err = s.LessonByID(1)
	add("LessonByID", err)
	_, err = s.CurrentLesson(now)
	add("CurrentLesson", err)
	_, err = s.NextLesson(now)
	add("NextLesson", err)
	_, err = s.UpcomingOnline(now, now.Add(time.Hour))
	add("UpcomingOnline", err)
	add("SaveParseRun", s.SaveParseRun(model.ParseRun{At: now}))
	_, err = s.LastParseRun()
	add("LastParseRun", err)
	_, err = s.EnsurePack(l, "", t.TempDir())
	add("EnsurePack", err)
	_, err = s.PackByLesson(1)
	add("PackByLesson", err)
	_, err = s.PackByID(1)
	add("PackByID", err)
	add("SavePack", s.SavePack(&model.LecturePack{ID: 1}))
	_, err = s.ListPacks()
	add("ListPacks", err)
	_, err = s.PacksByDate("2026-09-26")
	add("PacksByDate", err)
	_, err = s.AnyRecording()
	add("AnyRecording", err)
	_, err = s.GetBBB("k")
	add("GetBBB", err)
	add("SetBBB", s.SetBBB("k", "u"))
	add("SetLessonBBB", s.SetLessonBBB(1, "u"))
	_, err = s.ListBBB()
	add("ListBBB", err)
	add("AddEvent", s.AddEvent(model.Event{Type: "x"}))
	_, err = s.ListEvents(5)
	add("ListEvents", err)
	_, err = s.GetIntent(1, 1)
	add("GetIntent", err)
	add("PutIntent", s.PutIntent(model.JoinIntent{TelegramID: 1, LessonID: 1, AskedAt: now}))
	add("SetIntentDecision", s.SetIntentDecision(1, 1, model.JoinYes))
	_, _, err = s.GetSetting("k")
	add("GetSetting", err)
	add("SetSetting", s.SetSetting("k", "v"))
	add("RequestRaspRefresh", s.RequestRaspRefresh())
	_, err = s.ConsumeRaspRefresh()
	add("ConsumeRaspRefresh", err)
	add("SetPresence", s.SetPresence(model.Presence{TelegramID: 1}))
	add("ClearPresence", s.ClearPresence(1))
	_, err = s.GetPresence(1)
	add("GetPresence", err)
	_, err = s.ListPresence()
	add("ListPresence", err)
	_, err = s.LessonsHappening(now)
	add("LessonsHappening", err)
	_, err = s.LessonsInJoinWindow(now, time.Minute)
	add("LessonsInJoinWindow", err)
	_, err = s.GetTestJoin()
	add("GetTestJoin", err)
	add("PutTestJoin", s.PutTestJoin(model.TestJoin{}))

	for name, err := range errs {
		if err == nil {
			t.Errorf("%s on a closed db returned nil", name)
		}
	}
	if s.GetLessonBBB(1) != "" {
		t.Error("GetLessonBBB swallows the error into an empty link")
	}
}

// Close twice, then use: errors, not nil-pointer panics (late ticks at shutdown).
func TestUseAfterCloseIsAnError(t *testing.T) {
	s := openTemp(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := s.ListUsers(); err == nil {
		t.Fatal("use after Close must fail")
	}
	if _, err := s.GetTestJoin(); err == nil {
		t.Fatal("GetTestJoin after Close must fail")
	}
}

func TestLessonBBBCarriesToNextWeek(t *testing.T) {
	s := openTemp(t)
	mk := func(date, start, typ, teacher string) model.Lesson {
		begin, _ := time.Parse(time.RFC3339, date+"T"+start+":00+04:00")
		return model.Lesson{Date: date, Start: start, End: begin.Add(95 * time.Minute).Format("15:04"), Begin: begin, Finish: begin.Add(95 * time.Minute),
			Discipline: "Статистический анализ данных", Teacher: teacher, Place: "online", Type: typ, Online: true}
	}
	idOf := func(date, start string) int64 {
		ls, _ := s.ListLessons()
		for _, l := range ls {
			if l.Date == date && l.Start == start {
				return l.ID
			}
		}
		t.Fatalf("no lesson %s %s", date, start)
		return 0
	}
	week1 := []model.Lesson{
		mk("2026-09-28", "09:45", "Практика", "Смирнова А.Е."),
		mk("2026-09-29", "17:00", "Лекция", "Смирнова А.Е."),
	}
	if err := s.ReplaceLessons(week1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLessonBBB(idOf("2026-09-28", "09:45"), "https://bbb.ssau.ru/b/kol-prac"); err != nil {
		t.Fatal(err)
	}
	// Лекция того же препода — другой тип, ссылку практики не наследует.
	if got := s.GetLessonBBB(idOf("2026-09-29", "17:00")); got != "" {
		t.Fatalf("lecture must not reuse practice room: %q", got)
	}

	week2 := []model.Lesson{
		mk("2026-10-05", "09:45", "Практика", "Смирнова А.Е."),
		mk("2026-10-06", "17:00", "Лекция", "Смирнова А.Е."),
		mk("2026-10-07", "11:30", "Практика", "Иванов И.И."),
	}
	if err := s.ReplaceLessons(week2); err != nil {
		t.Fatal(err)
	}
	next := idOf("2026-10-05", "09:45")
	url, own, err := s.LessonBBB(next)
	if err != nil || url != "https://bbb.ssau.ru/b/kol-prac" || own {
		t.Fatalf("next week = %q own=%v err=%v", url, own, err)
	}
	if got := s.GetLessonBBB(idOf("2026-10-07", "11:30")); got != "" {
		t.Fatalf("other teacher must not reuse the room: %q", got)
	}

	// Ключ панели «группа|предмет|препод» подхватывается, если нет комнаты по типу.
	lect := idOf("2026-10-06", "17:00")
	if err := s.SetBBB(model.BBBKey(531023229, "Статистический анализ данных", "Смирнова А.Е."), "https://bbb.ssau.ru/b/panel"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetLessonBBB(lect); got != "https://bbb.ssau.ru/b/panel" {
		t.Fatalf("panel link = %q", got)
	}
	// Своя ссылка пары главнее унаследованной, пустая — снята явно.
	if err := s.SetLessonBBB(next, "https://bbb.ssau.ru/b/own"); err != nil {
		t.Fatal(err)
	}
	if url, own, _ := s.LessonBBB(next); url != "https://bbb.ssau.ru/b/own" || !own {
		t.Fatalf("own = %q %v", url, own)
	}
	if err := s.SetLessonBBB(next, ""); err != nil {
		t.Fatal(err)
	}
	if url, own, _ := s.LessonBBB(next); url != "" || !own {
		t.Fatalf("cleared = %q %v", url, own)
	}
}

func TestReplaceLessonsKeepsIdentityAndCleansOrphans(t *testing.T) {
	s := openTemp(t)
	begin, _ := time.Parse(time.RFC3339, "2026-10-05T09:45:00+04:00")
	base := model.Lesson{Date: "2026-10-05", Start: "09:45", End: "11:20", Begin: begin, Finish: begin.Add(95 * time.Minute),
		Discipline: "Сети", Teacher: "Еленев", Place: "online", Type: "Лекция", Online: true}
	if err := s.ReplaceLessons([]model.Lesson{base}); err != nil {
		t.Fatal(err)
	}
	ls, _ := s.ListLessons()
	id := ls[0].ID
	now := time.Now()
	if err := s.PutIntent(model.JoinIntent{TelegramID: 1, LessonID: id, Decision: model.JoinNo, AskedAt: now, DecidedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLessonBBB(id, "https://bbb.ssau.ru/b/net"); err != nil {
		t.Fatal(err)
	}

	// Перенесли в аудиторию — та же пара: id, «Не сегодня» и ссылка на месте.
	moved := base
	moved.Place, moved.Online = "311 - 15", false
	if err := s.ReplaceLessons([]model.Lesson{moved}); err != nil {
		t.Fatal(err)
	}
	ls, _ = s.ListLessons()
	if len(ls) != 1 || ls[0].ID != id || ls[0].Place != "311 - 15" || ls[0].Online {
		t.Fatalf("moved = %+v", ls)
	}
	if in, _ := s.GetIntent(1, id); in == nil || in.Decision != model.JoinNo {
		t.Fatalf("intent lost: %+v", in)
	}

	// Две пары с одной Identity в одном прогоне — две строки, а не одна.
	twin := moved
	twin.Teacher = "Второй"
	if err := s.ReplaceLessons([]model.Lesson{moved, twin}); err != nil {
		t.Fatal(err)
	}
	if ls, _ = s.ListLessons(); len(ls) != 2 {
		t.Fatalf("twins collapsed: %+v", ls)
	}

	// Пару убрали с сайта: ссылка переехала в комнату, согласие ушло.
	if err := s.SetBBB(model.BBBLessonKey(9999), "https://bbb.ssau.ru/b/ghost"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIntent(model.JoinIntent{TelegramID: 2, LessonID: 9999, Decision: model.JoinYes, AskedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLessons(nil); err != nil {
		t.Fatal(err)
	}
	if in, _ := s.GetIntent(1, id); in != nil {
		t.Fatalf("intent of pruned lesson kept: %+v", in)
	}
	if in, _ := s.GetIntent(2, 9999); in != nil {
		t.Fatalf("orphan intent kept: %+v", in)
	}
	links, _ := s.ListBBB()
	keys := map[string]string{}
	for _, b := range links {
		keys[b.Key] = b.URL
	}
	if _, ok := keys[model.BBBLessonKey(id)]; ok {
		t.Fatalf("lesson link kept: %v", keys)
	}
	if _, ok := keys[model.BBBLessonKey(9999)]; ok {
		t.Fatalf("orphan link kept: %v", keys)
	}
	if keys[model.BBBRoomKey("Сети", "Еленев", "Лекция")] != "https://bbb.ssau.ru/b/net" {
		t.Fatalf("room lost: %v", keys)
	}
}

func TestEnsurePackDeduplication(t *testing.T) {
	s := openTemp(t)
	root := t.TempDir()

	l1 := model.Lesson{
		ID:         101,
		Date:       "2026-10-01",
		Start:      "10:00",
		Discipline: "Физика",
		Subgroup:   1,
		BBBURL:     "https://bbb.ssau.ru/b/phys-room",
	}
	l2 := model.Lesson{
		ID:         102,
		Date:       "2026-10-01",
		Start:      "10:00",
		Discipline: "Физика",
		Subgroup:   2,
		BBBURL:     "https://bbb.ssau.ru/b/phys-room",
	}

	p1, err := s.EnsurePack(l1, "", root)
	if err != nil {
		t.Fatalf("EnsurePack l1: %v", err)
	}
	if p1.Number != 1 {
		t.Fatalf("expected number 1, got %d", p1.Number)
	}

	p2, err := s.EnsurePack(l2, "", root)
	if err != nil {
		t.Fatalf("EnsurePack l2: %v", err)
	}
	if p2.ID != p1.ID {
		t.Fatalf("expected reused pack ID %d, got %d", p1.ID, p2.ID)
	}
	if p2.Number != 1 {
		t.Fatalf("number should remain 1, got %d", p2.Number)
	}

	// Оба урока должны возвращать один и тот же пак
	got1, err := s.PackByLesson(101)
	if err != nil || got1 == nil || got1.ID != p1.ID {
		t.Fatalf("PackByLesson 101: %+v, err: %v", got1, err)
	}
	got2, err := s.PackByLesson(102)
	if err != nil || got2 == nil || got2.ID != p1.ID {
		t.Fatalf("PackByLesson 102: %+v, err: %v", got2, err)
	}

	// PackLessonIDs должен возвращать оба lesson_id
	lids, err := s.PackLessonIDs(p1.ID)
	if err != nil {
		t.Fatalf("PackLessonIDs: %v", err)
	}
	if len(lids) != 2 || lids[0] != 101 || lids[1] != 102 {
		t.Fatalf("expected lesson IDs [101, 102], got %+v", lids)
	}

	// Создаем следующую лекцию по физике в другой день — номер должен увеличиться до 2
	l3 := model.Lesson{
		ID:         103,
		Date:       "2026-10-08",
		Start:      "10:00",
		Discipline: "Физика",
		BBBURL:     "https://bbb.ssau.ru/b/phys-room",
	}
	p3, err := s.EnsurePack(l3, "", root)
	if err != nil {
		t.Fatalf("EnsurePack l3: %v", err)
	}
	if p3.ID == p1.ID {
		t.Fatalf("p3 must be a new pack")
	}
	if p3.Number != 2 {
		t.Fatalf("expected lecture number 2, got %d", p3.Number)
	}

	// Проверяем случай личных пар ИОТ у нескольких разных владельцев (Owner)
	lOwner1 := model.Lesson{
		ID:         104,
		Date:       "2026-10-15",
		Start:      "11:30",
		Discipline: "ИОТ Английский",
		Owner:      10001,
		BBBURL:     "https://bbb.ssau.ru/b/iot-eng",
	}
	lOwner2 := model.Lesson{
		ID:         105,
		Date:       "2026-10-15",
		Start:      "11:30",
		Discipline: "ИОТ Английский",
		Owner:      10002,
		BBBURL:     "https://bbb.ssau.ru/b/iot-eng",
	}
	pOwner1, err := s.EnsurePack(lOwner1, "", root)
	if err != nil {
		t.Fatalf("EnsurePack lOwner1: %v", err)
	}
	pOwner2, err := s.EnsurePack(lOwner2, "", root)
	if err != nil {
		t.Fatalf("EnsurePack lOwner2: %v", err)
	}
	if pOwner1.ID != pOwner2.ID {
		t.Fatalf("expected same pack for both owners, got %d and %d", pOwner1.ID, pOwner2.ID)
	}
	if pOwner2.Number != 1 {
		t.Fatalf("expected number 1 for first IOT lecture, got %d", pOwner2.Number)
	}
	gotOwner1, _ := s.PackByLesson(104)
	gotOwner2, _ := s.PackByLesson(105)
	if gotOwner1.ID != pOwner1.ID || gotOwner2.ID != pOwner1.ID {
		t.Fatalf("both lessons must point to pack %d", pOwner1.ID)
	}
	ownerLids, _ := s.PackLessonIDs(pOwner1.ID)
	if len(ownerLids) != 2 || ownerLids[0] != 104 || ownerLids[1] != 105 {
		t.Fatalf("expected [104, 105], got %+v", ownerLids)
	}
}

func TestMigrateDeduplicatePacks(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UTC()

	// Создаем уроки для одной встречи
	_, err := s.db.Exec(`
		INSERT INTO lessons (id, date, start, discipline, bbb_url) VALUES
		(301, '2026-11-01', '12:00', 'Матан', 'https://bbb.ssau.ru/b/matan'),
		(302, '2026-11-01', '12:00', 'Матан', 'https://bbb.ssau.ru/b/matan'),
		(303, '2026-11-01', '12:00', 'Матан', 'https://bbb.ssau.ru/b/matan')
	`)
	if err != nil {
		t.Fatalf("insert lessons: %v", err)
	}

	// Создаем паки с пустыми meeting_key и разными статусами
	res1, err := s.db.Exec(`
		INSERT INTO lecture_packs (lesson_id, discipline, number, date, bbb_url, meeting_key, status, created_at, updated_at)
		VALUES (301, 'Матан', 1, '2026-11-01', 'https://bbb.ssau.ru/b/matan', '', 'recording', ?, ?)
	`, timeArg(now), timeArg(now))
	if err != nil {
		t.Fatal(err)
	}
	id1, _ := res1.LastInsertId()

	res2, err := s.db.Exec(`
		INSERT INTO lecture_packs (lesson_id, discipline, number, date, bbb_url, meeting_key, status, created_at, updated_at)
		VALUES (302, 'Матан', 2, '2026-11-01', 'https://bbb.ssau.ru/b/matan', '', 'done', ?, ?)
	`, timeArg(now), timeArg(now))
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := res2.LastInsertId()

	res3, err := s.db.Exec(`
		INSERT INTO lecture_packs (lesson_id, discipline, number, date, bbb_url, meeting_key, status, created_at, updated_at)
		VALUES (303, 'Матан', 3, '2026-11-01', 'https://bbb.ssau.ru/b/matan', '', 'slides', ?, ?)
	`, timeArg(now), timeArg(now))
	if err != nil {
		t.Fatal(err)
	}
	id3, _ := res3.LastInsertId()

	// Запускаем склейку дублей
	if err := s.migrateDeduplicatePacks(); err != nil {
		t.Fatalf("migrateDeduplicatePacks: %v", err)
	}

	expectedKey := "bbb:https://bbb.ssau.ru/b/matan|2026-11-01|12:00"

	// Канонический пак (id2, статус done) должен сохранить meeting_key
	pack2, err := s.PackByID(id2)
	if err != nil || pack2 == nil {
		t.Fatalf("pack2: %+v %v", pack2, err)
	}
	if pack2.MeetingKey != expectedKey {
		t.Fatalf("expected meeting_key %q, got %q", expectedKey, pack2.MeetingKey)
	}

	// Дубли id1 и id3 должны иметь очищенный meeting_key
	pack1, err := s.PackByID(id1)
	if err != nil || pack1 == nil || pack1.MeetingKey != "" {
		t.Fatalf("pack1 should have empty meeting_key, got %+v", pack1)
	}
	pack3, err := s.PackByID(id3)
	if err != nil || pack3 == nil || pack3.MeetingKey != "" {
		t.Fatalf("pack3 should have empty meeting_key, got %+v", pack3)
	}

	// Все lesson_id должны резолвиться в канонический pack2
	for _, lid := range []int64{301, 302, 303} {
		p, err := s.PackByLesson(lid)
		if err != nil || p == nil || p.ID != id2 {
			t.Fatalf("lesson %d must resolve to canonical pack %d, got %+v (err %v)", lid, id2, p, err)
		}
	}

	// PackLessonIDs должен вернуть все три lesson_id
	lids, err := s.PackLessonIDs(id2)
	if err != nil {
		t.Fatalf("PackLessonIDs: %v", err)
	}
	if len(lids) != 3 || lids[0] != 301 || lids[1] != 302 || lids[2] != 303 {
		t.Fatalf("expected [301, 302, 303], got %+v", lids)
	}

	// Повторный запуск миграции (идемпотентность)
	if err := s.migrateDeduplicatePacks(); err != nil {
		t.Fatalf("second migrateDeduplicatePacks: %v", err)
	}
	pAfter, _ := s.PackByLesson(301)
	if pAfter == nil || pAfter.ID != id2 {
		t.Fatalf("after second migration lesson 301 should still point to pack %d", id2)
	}
}

func TestClaimPackForNotes(t *testing.T) {
	s := openTemp(t)
	root := t.TempDir()

	l := model.Lesson{
		ID:         401,
		Date:       "2026-11-14",
		Start:      "08:00",
		Discipline: "История",
		BBBURL:     "https://bbb.ssau.ru/b/history",
	}

	p, err := s.EnsurePack(l, "", root)
	if err != nil {
		t.Fatalf("EnsurePack: %v", err)
	}

	// Устанавливаем статус slides, чтобы разрешить захват
	p.Status = model.PackSlides
	if err := s.SavePack(p); err != nil {
		t.Fatalf("SavePack: %v", err)
	}

	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

	// 1. Первый захват успешен
	claimed, err := s.ClaimPackForNotes(p.ID, now)
	if err != nil {
		t.Fatalf("ClaimPackForNotes: %v", err)
	}
	if !claimed {
		t.Fatalf("первый захват должен быть успешным")
	}

	// Проверяем, что claimed_at проставился
	loaded, _ := s.PackByID(p.ID)
	if loaded.ClaimedAt == nil || !loaded.ClaimedAt.Equal(now) {
		t.Fatalf("claimed_at expected %s, got %v", now, loaded.ClaimedAt)
	}

	// 2. Повторный захват до истечения lease 45 минут возвращает false
	claimedAgain, err := s.ClaimPackForNotes(p.ID, now.Add(15*time.Minute))
	if err != nil {
		t.Fatalf("ClaimPackForNotes: %v", err)
	}
	if claimedAgain {
		t.Fatalf("захват во время активного lease должен возвращать false")
	}

	// Захват на 44-й минуте (до 45 минут) тоже false
	claimed44, err := s.ClaimPackForNotes(p.ID, now.Add(44*time.Minute))
	if err != nil {
		t.Fatalf("ClaimPackForNotes: %v", err)
	}
	if claimed44 {
		t.Fatalf("захват на 44 минуте должен возвращать false")
	}

	// 3. Захват после истечения lease 45 минут (на 46-й минуте) успешен
	newNow := now.Add(46 * time.Minute)
	claimedExpired, err := s.ClaimPackForNotes(p.ID, newNow)
	if err != nil {
		t.Fatalf("ClaimPackForNotes: %v", err)
	}
	if !claimedExpired {
		t.Fatalf("захват после истечения lease должен быть успешным")
	}

	// 4. Явное освобождение захвата ReleasePackClaim
	if err := s.ReleasePackClaim(p.ID); err != nil {
		t.Fatalf("ReleasePackClaim: %v", err)
	}

	// Проверяем, что claimed_at сброшен
	loaded, _ = s.PackByID(p.ID)
	if loaded.ClaimedAt != nil {
		t.Fatalf("claimed_at should be nil after release, got %v", loaded.ClaimedAt)
	}

	// Сразу после освобождения захват успешен
	claimedAfterRelease, err := s.ClaimPackForNotes(p.ID, newNow.Add(time.Minute))
	if err != nil || !claimedAfterRelease {
		t.Fatalf("захват сразу после release должен быть успешен: claimed=%v err=%v", claimedAfterRelease, err)
	}

	// 5. Переход в финальный статус PackDone сбрасывает claimed_at
	loaded, _ = s.PackByID(p.ID)
	loaded.Status = model.PackDone
	if err := s.SavePack(loaded); err != nil {
		t.Fatalf("SavePack: %v", err)
	}

	loadedDone, _ := s.PackByID(p.ID)
	if loadedDone.ClaimedAt != nil {
		t.Fatalf("claimed_at must be reset on PackDone, got %v", loadedDone.ClaimedAt)
	}

	// Попытка захватить пак в статусе PackDone отклоняется
	claimedOnDone, err := s.ClaimPackForNotes(p.ID, newNow.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("ClaimPackForNotes: %v", err)
	}
	if claimedOnDone {
		t.Fatalf("пак со статусом PackDone не должен захватываться")
	}

	// 6. Пак в статусе PackNotes со свежим claimed_at не захватывается,
	// а с просроченным (> 45 минут назад) — успешно захватывается.
	notesFresh := now.Add(3 * time.Hour)
	loaded.Status = model.PackNotes
	loaded.ClaimedAt = &notesFresh
	if err := s.SavePack(loaded); err != nil {
		t.Fatalf("SavePack PackNotes: %v", err)
	}
	claimedFreshNotes, err := s.ClaimPackForNotes(p.ID, notesFresh.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("ClaimPackForNotes fresh PackNotes: %v", err)
	}
	if claimedFreshNotes {
		t.Fatalf("пак в статусе PackNotes со свежим claimed_at не должен захватываться")
	}

	claimedExpiredNotes, err := s.ClaimPackForNotes(p.ID, notesFresh.Add(46*time.Minute))
	if err != nil {
		t.Fatalf("ClaimPackForNotes expired PackNotes: %v", err)
	}
	if !claimedExpiredNotes {
		t.Fatalf("пак в статусе PackNotes с просроченным claimed_at (> 45 мин) должен захватываться")
	}
}

func TestBindLessonToPack(t *testing.T) {
	s := openTemp(t)
	root := t.TempDir()

	l := model.Lesson{
		ID:         501,
		Date:       "2026-11-20",
		Start:      "14:00",
		Discipline: "Химия",
	}
	p, err := s.EnsurePack(l, "", root)
	if err != nil {
		t.Fatalf("EnsurePack: %v", err)
	}

	// Привязываем еще один урок к этому же паку
	if err := s.BindLessonToPack(502, p.ID); err != nil {
		t.Fatalf("BindLessonToPack: %v", err)
	}

	p502, err := s.PackByLesson(502)
	if err != nil || p502 == nil || p502.ID != p.ID {
		t.Fatalf("PackByLesson 502: %+v %v", p502, err)
	}

	ids, err := s.PackLessonIDs(p.ID)
	if err != nil {
		t.Fatalf("PackLessonIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != 501 || ids[1] != 502 {
		t.Fatalf("expected [501, 502], got %+v", ids)
	}
}

func TestEnsurePackRebindsToCanonicalOnLateBBBURL(t *testing.T) {
	s := openTemp(t)
	root := t.TempDir()

	const bbbURL = "https://bbb.ssau.ru/b/shared-room"
	l1 := model.Lesson{
		ID:         601,
		Date:       "2026-11-21",
		Start:      "10:00",
		Discipline: "Алгоритмы",
		Subgroup:   1,
	}
	l2 := model.Lesson{
		ID:         602,
		Date:       "2026-11-21",
		Start:      "10:00",
		Discipline: "Алгоритмы",
		Subgroup:   2,
		BBBURL:     bbbURL,
	}

	// Сначала создается пак для l1 с пустым bbb_url (meeting_key пустой)
	p1, err := s.EnsurePack(l1, "", root)
	if err != nil {
		t.Fatalf("EnsurePack l1 empty url: %v", err)
	}
	if p1.MeetingKey != "" {
		t.Fatalf("expected empty meeting_key for p1, got %q", p1.MeetingKey)
	}

	// Затем создается пак для l2 с заполненным MeetingKey — он становится каноническим
	p2, err := s.EnsurePack(l2, "", root)
	if err != nil {
		t.Fatalf("EnsurePack l2: %v", err)
	}
	if p2.ID == p1.ID || p2.MeetingKey == "" {
		t.Fatalf("expected separate canonical pack p2 with meeting_key, got p1=%+v p2=%+v", p1, p2)
	}

	// Повторный вызов EnsurePack(l1, bbbURL, root) не должен падать с UNIQUE constraint failed,
	// а должен вернуть канонический пак p2 и перепривязать l1 к p2.
	got, err := s.EnsurePack(l1, bbbURL, root)
	if err != nil {
		t.Fatalf("EnsurePack l1 with bbbURL failed: %v", err)
	}
	if got.ID != p2.ID {
		t.Fatalf("expected canonical pack %d, got %d", p2.ID, got.ID)
	}

	bound, err := s.PackByLesson(l1.ID)
	if err != nil || bound == nil || bound.ID != p2.ID {
		t.Fatalf("expected l1 rebound to pack %d, got %+v (err=%v)", p2.ID, bound, err)
	}

	lids, err := s.PackLessonIDs(p2.ID)
	if err != nil {
		t.Fatalf("PackLessonIDs: %v", err)
	}
	if len(lids) != 2 || lids[0] != 601 || lids[1] != 602 {
		t.Fatalf("expected [601, 602] on canonical pack, got %+v", lids)
	}
}

func TestEnsurePackZeroLessonIDConflict(t *testing.T) {
	s := openTemp(t)
	root := t.TempDir()

	l := model.Lesson{
		ID:         0,
		Date:       "2026-11-22",
		Discipline: "Сети",
	}
	p1, err := s.EnsurePack(l, "", root)
	if err != nil {
		t.Fatalf("first EnsurePack(ID=0): %v", err)
	}
	p2, err := s.EnsurePack(l, "", root)
	if err != nil {
		t.Fatalf("second EnsurePack(ID=0): %v", err)
	}
	if p2.ID != p1.ID {
		t.Fatalf("expected existing pack %d on lesson_id=0 conflict, got %d", p1.ID, p2.ID)
	}
}

func TestMigrateDeduplicatePacksHistoricalWithoutLessonsAndRerun(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UTC()

	// 1. Два исторических пака разных недель без строк в lessons, но с одинаковым постоянным bbb_url.
	// Они не должны склеиваться в один пак.
	resHist1, err := s.db.Exec(`
		INSERT INTO lecture_packs (lesson_id, discipline, number, date, bbb_url, meeting_key, status, created_at, updated_at)
		VALUES (701, 'Физика', 1, '2026-09-07', 'https://bbb.ssau.ru/b/const-room', '', 'done', ?, ?)
	`, timeArg(now), timeArg(now))
	if err != nil {
		t.Fatal(err)
	}
	histID1, _ := resHist1.LastInsertId()

	resHist2, err := s.db.Exec(`
		INSERT INTO lecture_packs (lesson_id, discipline, number, date, bbb_url, meeting_key, status, created_at, updated_at)
		VALUES (702, 'Физика', 2, '2026-09-14', 'https://bbb.ssau.ru/b/const-room', '', 'done', ?, ?)
	`, timeArg(now), timeArg(now))
	if err != nil {
		t.Fatal(err)
	}
	histID2, _ := resHist2.LastInsertId()

	if err := s.migrateDeduplicatePacks(); err != nil {
		t.Fatalf("migrateDeduplicatePacks historical: %v", err)
	}

	h1, err := s.PackByID(histID1)
	if err != nil || h1 == nil || h1.MeetingKey != "" {
		t.Fatalf("historical pack 1 must keep empty meeting_key, got %+v (err=%v)", h1, err)
	}
	h2, err := s.PackByID(histID2)
	if err != nil || h2 == nil || h2.MeetingKey != "" {
		t.Fatalf("historical pack 2 must keep empty meeting_key, got %+v (err=%v)", h2, err)
	}
	b1, err := s.PackByLesson(701)
	if err != nil || b1 == nil || b1.ID != histID1 {
		t.Fatalf("lesson 701 must stay bound to pack %d, got %+v (err=%v)", histID1, b1, err)
	}
	b2, err := s.PackByLesson(702)
	if err != nil || b2 == nil || b2.ID != histID2 {
		t.Fatalf("lesson 702 must stay bound to pack %d, got %+v (err=%v)", histID2, b2, err)
	}

	// 2. Повторная миграция при смене приоритета статусов у дублей не должна падать
	// на частичном уникальном индексе idx_lecture_packs_meeting_key.
	_, err = s.db.Exec(`
		INSERT INTO lessons (id, date, start, discipline, bbb_url) VALUES
		(801, '2026-11-25', '10:00', 'Базы данных', 'https://bbb.ssau.ru/b/db-room'),
		(802, '2026-11-25', '10:00', 'Базы данных', 'https://bbb.ssau.ru/b/db-room')
	`)
	if err != nil {
		t.Fatal(err)
	}

	resD1, err := s.db.Exec(`
		INSERT INTO lecture_packs (lesson_id, discipline, number, date, bbb_url, meeting_key, status, created_at, updated_at)
		VALUES (801, 'Базы данных', 1, '2026-11-25', 'https://bbb.ssau.ru/b/db-room', '', 'slides', ?, ?)
	`, timeArg(now), timeArg(now))
	if err != nil {
		t.Fatal(err)
	}
	dupID1, _ := resD1.LastInsertId()

	resD2, err := s.db.Exec(`
		INSERT INTO lecture_packs (lesson_id, discipline, number, date, bbb_url, meeting_key, status, created_at, updated_at)
		VALUES (802, 'Базы данных', 2, '2026-11-25', 'https://bbb.ssau.ru/b/db-room', '', 'recording', ?, ?)
	`, timeArg(now), timeArg(now))
	if err != nil {
		t.Fatal(err)
	}
	dupID2, _ := resD2.LastInsertId()

	// Первый прогон: каноническим становится dupID1 (slides > recording)
	if err := s.migrateDeduplicatePacks(); err != nil {
		t.Fatalf("first migrateDeduplicatePacks: %v", err)
	}
	expectedDBKey := "bbb:https://bbb.ssau.ru/b/db-room|2026-11-25|10:00"
	if p, _ := s.PackByID(dupID1); p == nil || p.MeetingKey != expectedDBKey {
		t.Fatalf("expected dupID1 to hold meeting_key %q, got %+v", expectedDBKey, p)
	}

	// Меняем статус второго пака на done (теперь он приоритетнее первого)
	if _, err := s.db.Exec(`UPDATE lecture_packs SET status = 'done' WHERE id = ?`, dupID2); err != nil {
		t.Fatal(err)
	}

	// Повторный прогон: сначала очищается meeting_key у старого канонического dupID1,
	// затем проставляется новому каноническому dupID2 — без UNIQUE constraint failed.
	if err := s.migrateDeduplicatePacks(); err != nil {
		t.Fatalf("second migrateDeduplicatePacks after status change: %v", err)
	}

	after1, _ := s.PackByID(dupID1)
	after2, _ := s.PackByID(dupID2)
	if after1 == nil || after1.MeetingKey != "" {
		t.Fatalf("expected dupID1 meeting_key cleared, got %+v", after1)
	}
	if after2 == nil || after2.MeetingKey != expectedDBKey {
		t.Fatalf("expected dupID2 to become canonical with key %q, got %+v", expectedDBKey, after2)
	}
	for _, lid := range []int64{801, 802} {
		p, err := s.PackByLesson(lid)
		if err != nil || p == nil || p.ID != dupID2 {
			t.Fatalf("lesson %d must point to new canonical pack %d, got %+v (err=%v)", lid, dupID2, p, err)
		}
	}
}
