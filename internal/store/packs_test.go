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
	mk := func(date string) model.Lesson {
		begin, _ := time.Parse(time.RFC3339, date+"T09:45:00+04:00")
		return model.Lesson{Date: date, Start: "09:45", End: "11:20", Begin: begin, Finish: begin.Add(95 * time.Minute),
			Discipline: "Статистический анализ данных", Teacher: "Колоденкова А.Е.", Place: "online", Type: "Практика", Online: true}
	}
	if err := s.ReplaceLessons([]model.Lesson{mk("2026-09-28")}); err != nil {
		t.Fatal(err)
	}
	ls, _ := s.ListLessons()
	if err := s.SetLessonBBB(ls[0].ID, "https://bbb.ssau.ru/b/kol-abc"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLessons([]model.Lesson{mk("2026-10-05")}); err != nil {
		t.Fatal(err)
	}
	ls, _ = s.ListLessons()
	if len(ls) != 1 || ls[0].Date != "2026-10-05" {
		t.Fatalf("lessons = %+v", ls)
	}
	if got := s.GetLessonBBB(ls[0].ID); got != "https://bbb.ssau.ru/b/kol-abc" {
		t.Fatalf("next week link = %q", got)
	}

	// Ключ из панели «группа|предмет|препод» тоже подхватывается и свежий побеждает.
	if err := s.SetBBB(model.BBBKey(531023229, "Статистический анализ данных", "Колоденкова А.Е."), "https://bbb.ssau.ru/b/panel"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetLessonBBB(ls[0].ID); got != "https://bbb.ssau.ru/b/panel" {
		t.Fatalf("panel link = %q", got)
	}
	other := mk("2026-10-05")
	other.Teacher = "Иванов И.И."
	other.Start = "11:30"
	if err := s.ReplaceLessons([]model.Lesson{mk("2026-10-05"), other}); err != nil {
		t.Fatal(err)
	}
	ls, _ = s.ListLessons()
	for _, l := range ls {
		if l.Teacher == "Иванов И.И." && s.GetLessonBBB(l.ID) != "" {
			t.Fatal("other teacher must not reuse the room")
		}
	}
}
