package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "bot.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOpenTempDBAndUserUpsert(t *testing.T) {
	st := openTemp(t)

	missing, err := st.GetUser(42)
	if err != nil || missing != nil {
		t.Fatalf("GetUser missing: got (%v, %v)", missing, err)
	}

	until := time.Date(2026, 9, 11, 23, 59, 59, 0, time.UTC)
	u := &model.User{
		TelegramID:    1074442235,
		Username:      "admin",
		FirstName:     "A",
		LastName:      "B",
		FIO:           "Иванов Иван",
		Subgroup:      1,
		Enabled:       true,
		DisabledUntil: &until,
		Onboarded:     true,
	}
	if err := st.UpsertUser(u); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	got, err := st.GetUser(u.TelegramID)
	if err != nil || got == nil {
		t.Fatalf("GetUser: (%v, %v)", got, err)
	}
	if got.Username != u.Username || got.FIO != u.FIO || got.Subgroup != 1 || !got.Enabled || !got.Onboarded {
		t.Fatalf("user fields: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should be set when zero")
	}
	if got.DisabledUntil == nil || !got.DisabledUntil.Equal(until) {
		t.Fatalf("DisabledUntil: %v", got.DisabledUntil)
	}
	created := got.CreatedAt

	u.FIO = "Петров Пётр"
	u.CreatedAt = time.Time{}
	if err := st.UpsertUser(u); err != nil {
		t.Fatalf("UpsertUser update: %v", err)
	}
	got, err = st.GetUser(u.TelegramID)
	if err != nil || got == nil {
		t.Fatalf("GetUser after update: (%v, %v)", got, err)
	}
	if got.FIO != "Петров Пётр" {
		t.Fatalf("FIO not updated: %q", got.FIO)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt mutated on upsert: %v vs %v", got.CreatedAt, created)
	}

	if err := st.SetEnabled(u.TelegramID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if err := st.SetFIO(u.TelegramID, "Сидоров Сидор"); err != nil {
		t.Fatalf("SetFIO: %v", err)
	}
	if err := st.SetSubgroup(u.TelegramID, 2); err != nil {
		t.Fatalf("SetSubgroup: %v", err)
	}
	if err := st.SetDisabledUntil(u.TelegramID, nil); err != nil {
		t.Fatalf("SetDisabledUntil nil: %v", err)
	}
	got, err = st.GetUser(u.TelegramID)
	if err != nil || got == nil {
		t.Fatalf("GetUser after setters: (%v, %v)", got, err)
	}
	if got.Enabled || got.FIO != "Сидоров Сидор" || got.Subgroup != 2 || got.DisabledUntil != nil {
		t.Fatalf("setters: %+v", got)
	}

	if err := st.SetExtraWords(u.TelegramID, []string{"лаба", "Тест", "лаба"}); err != nil {
		t.Fatalf("SetExtraWords: %v", err)
	}
	if err := st.SetOnboardStage(u.TelegramID, model.StageDone); err != nil {
		t.Fatalf("SetOnboardStage: %v", err)
	}
	got, err = st.GetUser(u.TelegramID)
	if err != nil || got == nil {
		t.Fatalf("GetUser after words: (%v, %v)", got, err)
	}
	if got.OnboardStage != model.StageDone || len(got.ExtraWords) != 2 || got.ExtraWords[0] != "лаба" || got.ExtraWords[1] != "тест" {
		t.Fatalf("extra words: %+v", got)
	}
	got.FIO = "Сидоров Сидор"
	got.Username = "admin"
	if err := st.UpsertUser(got); err != nil {
		t.Fatalf("UpsertUser keep extras: %v", err)
	}
	got, err = st.GetUser(u.TelegramID)
	if err != nil || got == nil || len(got.ExtraWords) != 2 || got.ExtraWords[0] != "лаба" {
		t.Fatalf("extras lost on upsert: %+v %v", got, err)
	}

	users, err := st.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].TelegramID != u.TelegramID {
		t.Fatalf("ListUsers: %+v", users)
	}

	if err := st.SetEnabled(999, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetEnabled missing: %v", err)
	}
}

func TestReplaceLessonsCurrentNext(t *testing.T) {
	st := openTemp(t)

	if err := st.SetBBB("531023229|Матан|Иванов", "https://bbb.ssau.ru/b/abc"); err != nil {
		t.Fatalf("SetBBB: %v", err)
	}

	aBegin := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	aFinish := time.Date(2026, 9, 11, 11, 0, 0, 0, time.UTC)
	bBegin := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	bFinish := time.Date(2026, 9, 11, 13, 0, 0, 0, time.UTC)
	offlineBegin := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	offlineFinish := time.Date(2026, 9, 11, 11, 0, 0, 0, time.UTC)

	first := []model.Lesson{
		{Date: "2026-09-11", Start: "10:00", End: "11:00", Begin: aBegin, Finish: aFinish, Discipline: "Матан", Teacher: "Иванов", Place: "online", Subgroup: 0, Type: "лек", Online: true},
		{Date: "2026-09-11", Start: "12:00", End: "13:00", Begin: bBegin, Finish: bFinish, Discipline: "Физика", Teacher: "Петров", Place: "online", Subgroup: 1, Type: "пр", Online: true},
		{Date: "2026-09-11", Start: "10:00", End: "11:00", Begin: offlineBegin, Finish: offlineFinish, Discipline: "Очно", Teacher: "Сидоров", Place: "ауд. 1", Online: false},
	}
	if err := st.ReplaceLessons(first); err != nil {
		t.Fatalf("ReplaceLessons: %v", err)
	}
	listed, err := st.ListLessons()
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("want 3 lessons, got %d", len(listed))
	}

	mid := time.Date(2026, 9, 11, 10, 30, 0, 0, time.UTC)
	cur, err := st.CurrentLesson(mid)
	if err != nil || cur == nil {
		t.Fatalf("CurrentLesson mid: (%v, %v)", cur, err)
	}
	if cur.Discipline != "Матан" || !cur.Online {
		t.Fatalf("current: %+v", cur)
	}
	next, err := st.NextLesson(mid)
	if err != nil || next == nil {
		t.Fatalf("NextLesson mid: (%v, %v)", next, err)
	}
	if next.Discipline != "Физика" {
		t.Fatalf("next: %+v", next)
	}

	soon, err := st.LessonsInJoinWindow(aBegin.Add(-10*time.Minute), 15*time.Minute)
	if err != nil {
		t.Fatalf("LessonsInJoinWindow: %v", err)
	}
	if len(soon) != 1 || soon[0].Discipline != "Матан" {
		t.Fatalf("join window 10m before: %+v", soon)
	}
	happening, err := st.LessonsHappening(aBegin.Add(-10 * time.Minute))
	if err != nil || len(happening) != 0 {
		t.Fatalf("happening before slot: %v %+v", err, happening)
	}

	atStart, err := st.CurrentLesson(aBegin)
	if err != nil || atStart == nil || atStart.Discipline != "Матан" {
		t.Fatalf("CurrentLesson at begin: (%v, %v)", atStart, err)
	}
	atFinish, err := st.CurrentLesson(aFinish)
	if err != nil || atFinish != nil {
		t.Fatalf("CurrentLesson at finish should be none: (%v, %v)", atFinish, err)
	}

	before := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	cur, err = st.CurrentLesson(before)
	if err != nil || cur != nil {
		t.Fatalf("CurrentLesson before: (%v, %v)", cur, err)
	}
	next, err = st.NextLesson(before)
	if err != nil || next == nil || next.Discipline != "Матан" {
		t.Fatalf("NextLesson before: (%v, %v)", next, err)
	}

	after := time.Date(2026, 9, 11, 13, 30, 0, 0, time.UTC)
	cur, err = st.CurrentLesson(after)
	if err != nil || cur != nil {
		t.Fatalf("CurrentLesson after: (%v, %v)", cur, err)
	}
	next, err = st.NextLesson(after)
	if err != nil || next != nil {
		t.Fatalf("NextLesson after: (%v, %v)", next, err)
	}

	upcoming, err := st.UpcomingOnline(before, time.Date(2026, 9, 11, 12, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("UpcomingOnline: %v", err)
	}
	if len(upcoming) != 2 {
		t.Fatalf("UpcomingOnline want 2, got %d", len(upcoming))
	}

	byID, err := st.LessonByID(listed[0].ID)
	if err != nil || byID == nil || byID.ID != listed[0].ID {
		t.Fatalf("LessonByID: (%v, %v)", byID, err)
	}
	none, err := st.LessonByID(999999)
	if err != nil || none != nil {
		t.Fatalf("LessonByID missing: (%v, %v)", none, err)
	}

	if err := st.ReplaceLessons([]model.Lesson{
		{Date: "2026-09-12", Start: "08:00", End: "09:35", Begin: time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC), Finish: time.Date(2026, 9, 12, 9, 35, 0, 0, time.UTC), Discipline: "Новая", Teacher: "X", Place: "online", Online: true},
	}); err != nil {
		t.Fatalf("ReplaceLessons second: %v", err)
	}
	listed, err = st.ListLessons()
	if err != nil {
		t.Fatalf("ListLessons after replace: %v", err)
	}
	if len(listed) != 1 || listed[0].Discipline != "Новая" {
		t.Fatalf("replaced lessons: %+v", listed)
	}

	link, err := st.GetBBB("531023229|Матан|Иванов")
	if err != nil || link == nil || link.URL != "https://bbb.ssau.ru/b/abc" {
		t.Fatalf("BBB should survive ReplaceLessons: (%v, %v)", link, err)
	}
}

func TestBBBRoundtrip(t *testing.T) {
	st := openTemp(t)
	missing, err := st.GetBBB("nope")
	if err != nil || missing != nil {
		t.Fatalf("GetBBB missing: (%v, %v)", missing, err)
	}
	if err := st.SetBBB("k1", "https://bbb.ssau.ru/b/one"); err != nil {
		t.Fatalf("SetBBB: %v", err)
	}
	got, err := st.GetBBB("k1")
	if err != nil || got == nil {
		t.Fatalf("GetBBB: (%v, %v)", got, err)
	}
	if got.Key != "k1" || got.URL != "https://bbb.ssau.ru/b/one" || got.UpdatedAt.IsZero() {
		t.Fatalf("bbb: %+v", got)
	}
	if err := st.SetBBB("k1", "https://bbb.ssau.ru/b/two"); err != nil {
		t.Fatalf("SetBBB update: %v", err)
	}
	got, err = st.GetBBB("k1")
	if err != nil || got == nil || got.URL != "https://bbb.ssau.ru/b/two" {
		t.Fatalf("GetBBB updated: (%v, %v)", got, err)
	}
	all, err := st.ListBBB()
	if err != nil || len(all) != 1 {
		t.Fatalf("ListBBB: %v %+v", err, all)
	}
}

func TestEventsOrder(t *testing.T) {
	st := openTemp(t)
	t1 := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 11, 11, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	for _, e := range []model.Event{
		{At: t1, Type: model.EventJoin, TelegramID: 1, LessonID: 10, Message: "old"},
		{At: t3, Type: model.EventLeave, TelegramID: 2, LessonID: 11, Message: "new"},
		{At: t2, Type: model.EventWake, TelegramID: 3, LessonID: 12, Message: "mid"},
	} {
		if err := st.AddEvent(e); err != nil {
			t.Fatalf("AddEvent: %v", err)
		}
	}
	ev, err := st.ListEvents(0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(ev) != 3 {
		t.Fatalf("want 3 events, got %d", len(ev))
	}
	if ev[0].Message != "new" || ev[1].Message != "mid" || ev[2].Message != "old" {
		t.Fatalf("order: %+v", ev)
	}
	limited, err := st.ListEvents(1)
	if err != nil || len(limited) != 1 || limited[0].Message != "new" {
		t.Fatalf("ListEvents limit 1: %v %+v", err, limited)
	}
}

func TestIntentDecision(t *testing.T) {
	st := openTemp(t)
	asked := time.Date(2026, 9, 11, 9, 45, 0, 0, time.UTC)
	if err := st.PutIntent(model.JoinIntent{
		TelegramID: 1,
		LessonID:   99,
		Decision:   model.JoinPending,
		AskedAt:    asked,
	}); err != nil {
		t.Fatalf("PutIntent: %v", err)
	}
	got, err := st.GetIntent(1, 99)
	if err != nil || got == nil {
		t.Fatalf("GetIntent: (%v, %v)", got, err)
	}
	if got.Decision != model.JoinPending || !got.AskedAt.Equal(asked) || got.DecidedAt != nil {
		t.Fatalf("intent: %+v", got)
	}
	missing, err := st.GetIntent(1, 100)
	if err != nil || missing != nil {
		t.Fatalf("GetIntent missing: (%v, %v)", missing, err)
	}
	if err := st.SetIntentDecision(1, 99, model.JoinYes); err != nil {
		t.Fatalf("SetIntentDecision: %v", err)
	}
	got, err = st.GetIntent(1, 99)
	if err != nil || got == nil {
		t.Fatalf("GetIntent after decision: (%v, %v)", got, err)
	}
	if got.Decision != model.JoinYes || got.DecidedAt == nil {
		t.Fatalf("decision: %+v", got)
	}
	if err := st.SetIntentDecision(1, 100, model.JoinNo); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetIntentDecision missing: %v", err)
	}
}

func TestParseRunAndSettings(t *testing.T) {
	st := openTemp(t)
	run, err := st.LastParseRun()
	if err != nil || run != nil {
		t.Fatalf("LastParseRun empty: (%v, %v)", run, err)
	}
	at := time.Date(2026, 9, 11, 7, 0, 0, 0, time.UTC)
	if err := st.SaveParseRun(model.ParseRun{
		At:          at,
		OK:          true,
		Status:      "ok",
		LessonCount: 12,
		OnlineCount: 3,
		Diff:        "+1",
	}); err != nil {
		t.Fatalf("SaveParseRun: %v", err)
	}
	run, err = st.LastParseRun()
	if err != nil || run == nil {
		t.Fatalf("LastParseRun: (%v, %v)", run, err)
	}
	if !run.OK || run.LessonCount != 12 || run.OnlineCount != 3 || run.Diff != "+1" || !run.At.Equal(at) {
		t.Fatalf("parse run: %+v", run)
	}

	val, ok, err := st.GetSetting("group_id")
	if err != nil || ok || val != "" {
		t.Fatalf("GetSetting missing: %q %v %v", val, ok, err)
	}
	if err := st.SetSetting("group_id", "531023229"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	val, ok, err = st.GetSetting("group_id")
	if err != nil || !ok || val != "531023229" {
		t.Fatalf("GetSetting: %q %v %v", val, ok, err)
	}
}

func TestPresenceAndRaspKick(t *testing.T) {
	st := openTemp(t)
	ok, err := st.ConsumeRaspRefresh()
	if err != nil || ok {
		t.Fatalf("empty kick: %v %v", ok, err)
	}
	if err := st.RequestRaspRefresh(); err != nil {
		t.Fatal(err)
	}
	ok, err = st.ConsumeRaspRefresh()
	if err != nil || !ok {
		t.Fatalf("kick: %v %v", ok, err)
	}
	ok, err = st.ConsumeRaspRefresh()
	if err != nil || ok {
		t.Fatalf("second consume: %v %v", ok, err)
	}

	if err := st.SetPresence(model.Presence{
		TelegramID: 1, LessonID: 9, State: model.PresenceRoom, Message: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListPresence()
	if err != nil || len(list) != 1 || list[0].State != model.PresenceRoom {
		t.Fatalf("list: %+v %v", list, err)
	}
	if err := st.ClearPresence(1); err != nil {
		t.Fatal(err)
	}
	list, err = st.ListPresence()
	if err != nil || len(list) != 0 {
		t.Fatalf("cleared: %+v %v", list, err)
	}
}

func TestLecturePacksNumbering(t *testing.T) {
	st := openTemp(t)
	loc := time.UTC
	lessons := []model.Lesson{
		{
			Date: "2026-09-15", Discipline: "Матан", Type: "Лекция", Online: true,
			Begin: time.Date(2026, 9, 15, 8, 0, 0, 0, loc), Finish: time.Date(2026, 9, 15, 9, 35, 0, 0, loc),
		},
		{
			Date: "2026-09-15", Discipline: "Физика", Type: "Лекция", Online: true,
			Begin: time.Date(2026, 9, 15, 10, 0, 0, 0, loc), Finish: time.Date(2026, 9, 15, 11, 20, 0, 0, loc),
		},
		{
			Date: "2026-09-16", Discipline: "Матан", Type: "Лекция", Online: true,
			Begin: time.Date(2026, 9, 16, 8, 0, 0, 0, loc), Finish: time.Date(2026, 9, 16, 9, 35, 0, 0, loc),
		},
	}
	if err := st.ReplaceLessons(lessons); err != nil {
		t.Fatal(err)
	}
	listed, err := st.ListLessons()
	if err != nil || len(listed) != 3 {
		t.Fatalf("listed: %d %v", len(listed), err)
	}
	root := t.TempDir()
	p1, err := st.EnsurePack(listed[0], "https://bbb.ssau.ru/b/a", root)
	if err != nil || p1 == nil || p1.Number != 1 || p1.Discipline != "Матан" {
		t.Fatalf("p1: %+v %v", p1, err)
	}
	p2, err := st.EnsurePack(listed[1], "https://bbb.ssau.ru/b/b", root)
	if err != nil || p2 == nil || p2.Number != 1 || p2.Discipline != "Физика" {
		t.Fatalf("p2: %+v %v", p2, err)
	}
	p3, err := st.EnsurePack(listed[2], "https://bbb.ssau.ru/b/a", root)
	if err != nil || p3 == nil || p3.Number != 2 {
		t.Fatalf("p3: %+v %v", p3, err)
	}
	again, err := st.EnsurePack(listed[0], "", root)
	if err != nil || again == nil || again.ID != p1.ID || again.Number != 1 {
		t.Fatalf("again: %+v %v", again, err)
	}
	ok, err := st.AnyRecording()
	if err != nil || !ok {
		t.Fatalf("recording flag: %v %v", ok, err)
	}
}

func TestTestJoinAndAdmin(t *testing.T) {
	st := openTemp(t)
	got, err := st.GetTestJoin()
	if err != nil || got.GuestName() != "тест" {
		t.Fatalf("empty: %+v %v", got, err)
	}
	got.URL = "https://bbb.ssau.ru/b/abc"
	got.Want = model.TestWantDummy
	got.Status = model.TestJoining
	if err := st.PutTestJoin(got); err != nil {
		t.Fatal(err)
	}
	back, err := st.GetTestJoin()
	if err != nil || back.URL != got.URL || back.Want != model.TestWantDummy || back.Name != "тест" {
		t.Fatalf("roundtrip: %+v %v", back, err)
	}
}
