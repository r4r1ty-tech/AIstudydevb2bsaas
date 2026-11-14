package bbb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func TestChooseRecorderKeepsLiveAndPicksMinID(t *testing.T) {
	w := NewWorker(&config.Config{BBBDryRun: true}, nil, time.UTC)
	now := time.Date(2026, 9, 15, 8, 10, 0, 0, time.UTC)
	lesson := model.Lesson{ID: 7, Type: "Лекция"}
	meetKey := meetingGroupKey(lesson, "")
	here := []pending{
		{u: model.User{TelegramID: 20}, lesson: lesson, key: sessionKey(20, 7), meetKey: meetKey},
		{u: model.User{TelegramID: 10}, lesson: lesson, key: sessionKey(10, 7), meetKey: meetKey},
	}
	if i := w.chooseRecorder(meetKey, here, now); i != 1 {
		t.Fatalf("min id: got %d", i)
	}
	// Живой рекордер 20 остаётся, второй запис не начинаем.
	w.sessions[sessionKey(20, 7)] = stubSess{room: true}
	w.recorder[meetKey] = sessionKey(20, 7)
	if i := w.chooseRecorder(meetKey, here, now); i != 0 {
		t.Fatalf("live recorder: got %d", i)
	}
	// Рекордер выпал (его нет среди тех, кто должен быть в комнате) — передаём 10.
	if i := w.chooseRecorder(meetKey, here[1:], now); i != 0 {
		t.Fatalf("handover: got %d", i)
	}
	// Запись только что не стартовала — пауза, не дёргаем вкладки каждые 15 с.
	delete(w.recorder, meetKey)
	delete(w.sessions, sessionKey(20, 7))
	w.recFailAt[meetKey] = now
	if i := w.chooseRecorder(meetKey, here, now.Add(time.Minute)); i != -1 {
		t.Fatalf("rec retry gap: got %d", i)
	}
	if i := w.chooseRecorder(meetKey, here, now.Add(3*time.Minute)); i != 1 {
		t.Fatalf("after gap: got %d", i)
	}
	// Запись не стартует раз за разом — больше не перезаходим, пара без записи.
	w.recFailN[meetKey] = maxRecFails
	if i := w.chooseRecorder(meetKey, here, now.Add(time.Hour)); i != -1 {
		t.Fatalf("после %d неудач рекордера не выбираем: got %d", maxRecFails, i)
	}
}

func TestBuildNotesDaySkipsLiveRecording(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	lesson := model.Lesson{ID: 1, Date: "2026-09-13", Discipline: "Матан", Type: "Лекция"}
	p, err := st.EnsurePack(lesson, "https://bbb.ssau.ru/b/x", root)
	if err != nil {
		t.Fatal(err)
	}
	p.Status = model.PackRecording
	p.Audio = filepath.ToSlash(filepath.Join(p.Dir, "audio.ogg"))
	if err := st.SavePack(p); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(&config.Config{BBBDryRun: true, RecordingsDir: root}, st, time.UTC)
	key := sessionKey(5, lesson.ID)
	w.sessions[key] = drySession{}
	w.recorder[meetingGroupKey(lesson, p.BBBURL)] = key
	if w.buildNotesDay(context.Background(), "2026-09-13") {
		t.Fatal("live recording must wait")
	}
	if got, _ := st.PackByID(p.ID); got.Status != model.PackRecording {
		t.Fatalf("live pack touched: %s", got.Status)
	}
}

func TestBeginJobQuiet(t *testing.T) {
	w := NewWorker(&config.Config{BBBDryRun: true}, nil, time.UTC)
	w.sessions["1:1"] = drySession{}
	if w.beginJob(true) {
		t.Fatal("notes should wait while tabs live")
	}
	if !w.beginJob(false) {
		t.Fatal("slides can start")
	}
	w.endJob()
	w.sessions = map[string]Session{}
	if !w.beginJob(true) {
		t.Fatal("notes after empty")
	}
}

func TestOpenNoteDaysIncludesToday(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	lesson := model.Lesson{ID: 10, Date: today, Discipline: "Физика", Type: "Лекция"}
	_, err = st.EnsurePack(lesson, "https://bbb.ssau.ru/b/phys", root)
	if err != nil {
		t.Fatal(err)
	}

	w := NewWorker(&config.Config{BBBDryRun: true, RecordingsDir: root}, st, time.UTC)
	days := w.openNoteDays(now)
	foundToday := false
	for _, d := range days {
		if d == today {
			foundToday = true
			break
		}
	}
	if !foundToday {
		t.Fatalf("openNoteDays should include today %s, got: %v", today, days)
	}

	w.setSetting(notesDoneKey+today, "1")
	daysAfter := w.openNoteDays(now)
	for _, d := range daysAfter {
		if d == today {
			t.Fatalf("openNoteDays should not include closed today %s, got: %v", today, daysAfter)
		}
	}
}

func TestBuildNotesDaySkipsOngoingLesson(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	lesson := model.Lesson{
		ID:         20,
		Date:       today,
		Discipline: "Химия",
		Type:       "Лекция",
		Finish:     now.Add(30 * time.Minute),
	}
	_ = st.ReplaceLessons([]model.Lesson{lesson})
	all, _ := st.ListLessons()
	if len(all) > 0 {
		lesson = all[0]
	}
	p, err := st.EnsurePack(lesson, "https://bbb.ssau.ru/b/chem", root)
	if err != nil {
		t.Fatal(err)
	}
	p.Status = model.PackRecorded
	_ = st.SavePack(p)

	w := NewWorker(&config.Config{BBBDryRun: true, RecordingsDir: root}, st, time.UTC)
	done := w.buildNotesDay(context.Background(), today)
	if done {
		t.Fatal("buildNotesDay should return false when lesson finish is in future")
	}
	got, _ := st.PackByID(p.ID)
	if got.Status != model.PackRecorded {
		t.Fatalf("ongoing lesson pack should remain PackRecorded, got: %s", got.Status)
	}
}

func TestMaybeNotesDoesNotMarkDoneWhenLecturesAhead(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	l1 := model.Lesson{
		Date:       today,
		Discipline: "Сети",
		Type:       "Лекция",
		Online:     true,
		Finish:     now.Add(-30 * time.Minute),
	}
	l2 := model.Lesson{
		Date:       today,
		Discipline: "Матан",
		Type:       "Лекция",
		Online:     true,
		Finish:     now.Add(3 * time.Hour),
	}
	_ = st.ReplaceLessons([]model.Lesson{l1, l2})
	lessons, _ := st.ListLessons()

	p1, err := st.EnsurePack(lessons[0], "https://bbb.ssau.ru/b/net", root)
	if err != nil {
		t.Fatal(err)
	}
	p1.Status = model.PackRecorded
	_ = st.SavePack(p1)

	w := NewWorker(&config.Config{BBBDryRun: true, RecordingsDir: root}, st, time.UTC)

	// Since there is no audio file, buildNotes will fail or mark empty; let's mark p1 as PackDone directly to simulate completed note
	p1.Status = model.PackDone
	_ = st.SavePack(p1)

	w.maybeNotes(context.Background(), now)
	w.jobWG.Wait()

	if w.settingOn(notesDoneKey + today) {
		t.Fatal("notesDoneKey must not be set when lecture l2 is still ahead today")
	}

	// Now finish l2
	for i := range lessons {
		lessons[i].Finish = now.Add(-10 * time.Minute)
	}
	_ = st.ReplaceLessons(lessons)

	w.maybeNotes(context.Background(), now)
	w.jobWG.Wait()

	if !w.settingOn(notesDoneKey + today) {
		t.Fatal("notesDoneKey must be set once all lectures for today are finished")
	}
}

func TestAttachRecorderPromotesToPackSlidesWhenSlidesCaptured(t *testing.T) {
	useFakeRecorder(t, nil)
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	lesson := model.Lesson{
		ID:         30,
		Date:       today,
		Discipline: "История",
		Type:       "Лекция",
		Finish:     now.Add(-10 * time.Minute),
	}
	_ = st.ReplaceLessons([]model.Lesson{lesson})

	w := NewWorker(&config.Config{BBBDryRun: true, RecordingsDir: root}, st, time.UTC)

	fake := &fakeSess{room: true, slides: 3}
	hookSess, ok := w.attachRecorder(context.Background(), fake, lesson, "https://bbb.ssau.ru/b/hist")
	if !ok {
		t.Fatal("attachRecorder failed")
	}

	p, err := st.PackByLesson(lesson.ID)
	if err != nil || p == nil {
		t.Fatal("pack not created")
	}
	if p.Status != model.PackRecording {
		t.Fatalf("initial status = %s, want recording", p.Status)
	}

	// Close the session: hook fn runs and grabs slides
	if err := hookSess.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := st.PackByID(p.ID)
	if err != nil || got == nil {
		t.Fatal("pack reload failed")
	}
	if got.Status != model.PackSlides {
		t.Fatalf("expected status %s when slides captured, got %s", model.PackSlides, got.Status)
	}
}

func TestAttachRecorderKeepsPackRecordedWhenNoSlides(t *testing.T) {
	useFakeRecorder(t, nil)
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	lesson := model.Lesson{
		ID:         40,
		Date:       today,
		Discipline: "Философия",
		Type:       "Лекция",
		Finish:     now.Add(-10 * time.Minute),
	}
	_ = st.ReplaceLessons([]model.Lesson{lesson})

	w := NewWorker(&config.Config{BBBDryRun: true, RecordingsDir: root}, st, time.UTC)

	fake := &fakeSess{room: true, slides: 0}
	hookSess, ok := w.attachRecorder(context.Background(), fake, lesson, "https://bbb.ssau.ru/b/phil")
	if !ok {
		t.Fatal("attachRecorder failed")
	}

	p, _ := st.PackByLesson(lesson.ID)
	if err := hookSess.Close(); err != nil {
		t.Fatal(err)
	}

	got, _ := st.PackByID(p.ID)
	if got.Status != model.PackRecorded {
		t.Fatalf("expected status %s when no slides, got %s", model.PackRecorded, got.Status)
	}
}

func TestOnWakeTriggersMoodleKick(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{AdminID: 777}
	w := NewWorker(cfg, st, time.UTC)

	lesson := model.Lesson{ID: 50, Discipline: "Теория вероятностей"}
	users := []model.User{{TelegramID: 101}, {TelegramID: 102}}

	w.onWake(context.Background(), lesson, users, "тест")

	val, ok, err := st.GetSetting("moodle_wake_kick")
	if err != nil || !ok || val != "50:тест" {
		t.Fatalf("moodle_wake_kick = (%q, %v), want '50:тест'", val, ok)
	}
	val, ok, _ = st.GetSetting("moodle_kick:777")
	if !ok || val != "1" {
		t.Fatalf("moodle_kick:777 = (%q, %v), want '1'", val, ok)
	}
	val, ok, _ = st.GetSetting("moodle_kick:101")
	if !ok || val != "1" {
		t.Fatalf("moodle_kick:101 = (%q, %v), want '1'", val, ok)
	}
	val, ok, _ = st.GetSetting("moodle_kick:102")
	if !ok || val != "1" {
		t.Fatalf("moodle_kick:102 = (%q, %v), want '1'", val, ok)
	}
}

func TestMissingBBBRequestsRaspRefresh(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	w := NewWorker(&config.Config{}, st, time.UTC)
	u := model.User{TelegramID: 555}
	lesson := model.Lesson{ID: 60, Discipline: "Базы данных"}
	key := sessionKey(u.TelegramID, lesson.ID)

	w.missingBBB(context.Background(), u, lesson, key)

	req, err := st.ConsumeRaspRefresh()
	if err != nil || !req {
		t.Fatalf("expected RaspRefresh requested on missingBBB, got (%v, %v)", req, err)
	}

	// Repeated call with same key must not request refresh again
	w.missingBBB(context.Background(), u, lesson, key)
	req2, err := st.ConsumeRaspRefresh()
	if err != nil || req2 {
		t.Fatalf("expected no repeat RaspRefresh on duplicate missingBBB, got (%v, %v)", req2, err)
	}
}
