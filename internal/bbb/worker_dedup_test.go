package bbb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

func TestTickOneRecorderPerMeetingAcrossSubgroupsAndIOT(t *testing.T) {
	h := newWorkerHarness(t)
	recs, _ := useFakeRecorder(t, make([]byte, 3200))

	// Два студента из разных подгрупп и с разными строками расписания в одной комнате BBB.
	u1 := model.User{TelegramID: 101, FIO: "Первый Студент", Subgroup: 1, Enabled: true, Onboarded: true, OnboardStage: model.StageDone}
	u2 := model.User{TelegramID: 102, FIO: "Второй Студент", Subgroup: 2, Enabled: true, Onboarded: true, OnboardStage: model.StageDone}
	if err := h.st.UpsertUser(&u1); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpsertUser(&u2); err != nil {
		t.Fatal(err)
	}

	begin := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	sharedURL := "https://bbb.ssau.ru/b/shared-room"
	l1 := model.Lesson{
		Date: begin.Format("2006-01-02"), Start: begin.Format("15:04"), End: begin.Add(90 * time.Minute).Format("15:04"),
		Begin: begin, Finish: begin.Add(90 * time.Minute),
		Discipline: "Алгебра", Type: "Лекция", Subgroup: 1, Online: true, BBBURL: sharedURL,
	}
	l2 := model.Lesson{
		Date: begin.Format("2006-01-02"), Start: begin.Format("15:04"), End: begin.Add(90 * time.Minute).Format("15:04"),
		Begin: begin, Finish: begin.Add(90 * time.Minute),
		Discipline: "Алгебра", Type: "Лекция", Subgroup: 2, Owner: u2.TelegramID, Online: true, BBBURL: sharedURL,
	}
	if err := h.st.ReplaceLessons([]model.Lesson{l1, l2}); err != nil {
		t.Fatal(err)
	}
	lessons, err := h.st.ListLessons()
	if err != nil || len(lessons) != 2 {
		t.Fatalf("lessons = %+v, err=%v", lessons, err)
	}
	l1, l2 = lessons[0], lessons[1]

	// u2 нажал JoinYes раньше, но оба заходят сейчас; рекордер должен быть ровно один.
	h.yes(u1.TelegramID, l1)
	h.yes(u2.TelegramID, l2)

	h.w.tick(context.Background())
	h.w.WaitIdle()

	reqs := h.j.requests()
	if len(reqs) != 2 {
		t.Fatalf("ожидали 2 захода, получили %d: %+v", len(reqs), reqs)
	}
	recRoles := 0
	presRoles := 0
	for _, r := range reqs {
		switch r.Role {
		case RoleRecord:
			recRoles++
		case RolePresence:
			presRoles++
		}
	}
	if recRoles != 1 || presRoles != 1 {
		t.Fatalf("должен быть 1 рекордер и 1 presence, получили record=%d presence=%d (%+v)", recRoles, presRoles, reqs)
	}
	if len(*recs) != 1 {
		t.Fatalf("ожидали ровно 1 запущенный рекордер, получили %d", len(*recs))
	}

	// Повторный тик не поднимает второй рекордер.
	h.w.tick(context.Background())
	h.w.WaitIdle()
	if len(*recs) != 1 || len(h.j.requests()) != 2 {
		t.Fatalf("повторный тик изменил состав: recs=%d reqs=%d", len(*recs), len(h.j.requests()))
	}

	// Проверяем, что при уходе текущего рекордера (u1) запись передаётся u2 на той же встрече.
	meetKey := meetingGroupKey(l1, sharedURL)
	h.w.mu.Lock()
	recKey := h.w.recorder[meetKey]
	h.w.mu.Unlock()
	if recKey != sessionKey(u1.TelegramID, l1.ID) {
		t.Fatalf("ожидали рекордера %s, получили %s", sessionKey(u1.TelegramID, l1.ID), recKey)
	}
	if err := h.st.SetIntentDecision(u1.TelegramID, l1.ID, model.JoinNo); err != nil {
		t.Fatal(err)
	}
	h.w.tick(context.Background())
	h.w.WaitIdle()
	// На этом тике u1 вышел и promoteRecorder закрыл presence-сессию u2 для переоткрытия с записью;
	// следующий тик переподнимет u2 как рекордера.
	h.w.tick(context.Background())
	h.w.WaitIdle()

	h.w.mu.Lock()
	newRecKey := h.w.recorder[meetKey]
	h.w.mu.Unlock()
	if newRecKey != sessionKey(u2.TelegramID, l2.ID) {
		t.Fatalf("после ухода u1 рекордером должен стать u2 (%s), получили %s", sessionKey(u2.TelegramID, l2.ID), newRecKey)
	}
	if len(*recs) != 2 {
		t.Fatalf("ожидали второй запуск рекордера после handover, получили %d", len(*recs))
	}
	p1, _ := h.st.PackByLesson(l1.ID)
	p2, _ := h.st.PackByLesson(l2.ID)
	if p1 == nil || p2 == nil || p1.ID != p2.ID {
		t.Fatalf("обе строки должны ссылаться на один канонический пак: p1=%+v p2=%+v", p1, p2)
	}
}

func TestChooseRecorderPrefersEarlierJoiner(t *testing.T) {
	h := newWorkerHarness(t)
	now := time.Date(2026, 9, 15, 9, 50, 0, 0, time.UTC)
	begin := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	l := model.Lesson{ID: 1, Date: "2026-09-15", Start: "10:00", Begin: begin, Finish: begin.Add(90 * time.Minute), Type: "Лекция"}
	mk := meetingGroupKey(l, "https://bbb.ssau.ru/b/room")

	at := now.Add(-10 * time.Minute)
	yesIntent := &model.JoinIntent{TelegramID: 20, LessonID: l.ID, Decision: model.JoinYes, AskedAt: at, DecidedAt: &at}

	// 1. Никто ещё не зашёл: выигрывает тот, у кого раньше EnterAt (JoinYes = -15m против молчуна = -5m),
	// даже если у него больший TelegramID.
	here := []pending{
		{u: model.User{TelegramID: 10}, lesson: l, key: sessionKey(10, l.ID), meetKey: mk, intent: nil},
		{u: model.User{TelegramID: 20}, lesson: l, key: sessionKey(20, l.ID), meetKey: mk, intent: yesIntent},
	}
	if got := h.w.chooseRecorder(mk, here, now); got != 1 {
		t.Fatalf("должен выиграть кандидат с более ранним EnterAt (index 1), got %d", got)
	}

	// 2. Оба уже в комнате: выигрывает тот, у кого раньше seatedAt, даже при большем TelegramID.
	h.w.sessions[here[0].key] = stubSess{room: true}
	h.w.sessions[here[1].key] = stubSess{room: true}
	h.w.seatedAt[here[0].key] = now.Add(-1 * time.Minute)
	h.w.seatedAt[here[1].key] = now.Add(-4 * time.Minute)
	if got := h.w.chooseRecorder(mk, here, now); got != 1 {
		t.Fatalf("должен выиграть кандидат с более ранним seatedAt (index 1), got %d", got)
	}

	// 3. Один уже в комнате, второй ещё не вошёл (даже с ранним EnterAt): выигрывает сидящий в комнате.
	delete(h.w.sessions, here[1].key)
	delete(h.w.seatedAt, here[1].key)
	if got := h.w.chooseRecorder(mk, here, now); got != 0 {
		t.Fatalf("должен выиграть кандидат с живой сессией (index 0), got %d", got)
	}

	// 4. Равные времена: детерминированный тай-брейк по TelegramID, затем по lesson.ID.
	delete(h.w.sessions, here[0].key)
	delete(h.w.seatedAt, here[0].key)
	l2 := l
	l2.ID = 2
	sameTime := []pending{
		{u: model.User{TelegramID: 10}, lesson: l2, key: sessionKey(10, l2.ID), meetKey: mk, intent: yesIntent},
		{u: model.User{TelegramID: 10}, lesson: l, key: sessionKey(10, l.ID), meetKey: mk, intent: yesIntent},
	}
	if got := h.w.chooseRecorder(mk, sameTime, now); got != 1 {
		t.Fatalf("тай-брейк по lesson.ID должен выбрать index 1, got %d", got)
	}
}

func TestAnnounceNotesTargetedRecipients(t *testing.T) {
	h := newWorkerHarness(t)

	uSub1 := model.User{TelegramID: 11, FIO: "Иванов", Subgroup: 1, Enabled: true, Onboarded: true, OnboardStage: model.StageDone}
	uSub2 := model.User{TelegramID: 12, FIO: "Петров", Subgroup: 2, Enabled: true, Onboarded: true, OnboardStage: model.StageDone}
	uOff := model.User{TelegramID: 13, FIO: "Выключен", Subgroup: 1, Enabled: false, Onboarded: true, OnboardStage: model.StageDone}
	for _, u := range []model.User{uSub1, uSub2, uOff} {
		uCopy := u
		if err := h.st.UpsertUser(&uCopy); err != nil {
			t.Fatal(err)
		}
	}

	begin := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	sharedURL := "https://bbb.ssau.ru/b/notes-room"
	lSub1 := model.Lesson{
		Date: begin.Format("2006-01-02"), Start: begin.Format("15:04"), End: begin.Add(90 * time.Minute).Format("15:04"),
		Begin: begin, Finish: begin.Add(90 * time.Minute),
		Discipline: "Физика", Type: "Лекция", Subgroup: 1, Online: true, BBBURL: sharedURL,
	}
	if err := h.st.ReplaceLessons([]model.Lesson{lSub1}); err != nil {
		t.Fatal(err)
	}
	lessons, _ := h.st.ListLessons()
	lSub1 = lessons[0]

	p := h.pack(lSub1, model.PackDone)
	h.api.Reset()
	h.w.announceNotes(context.Background(), p)

	if got := len(h.sentTo(uSub1.TelegramID)); got != 1 {
		t.Fatalf("студент подгруппы 1 должен получить 1 сообщение, получил %d", got)
	}
	if got := len(h.sentTo(uSub2.TelegramID)); got != 0 {
		t.Fatalf("студент другой подгруппы не должен получать сообщение, получил %d", got)
	}
	if got := len(h.sentTo(uOff.TelegramID)); got != 0 {
		t.Fatalf("выключенный студент не должен получать сообщение, получил %d", got)
	}

	// Добавляем вторую привязанную к этому же паку строку (личный ИОТ для uSub2) —
	// оба получают ровно по 1 сообщению.
	lIOT := lSub1
	lIOT.ID = 0
	lIOT.Subgroup = 2
	lIOT.Owner = uSub2.TelegramID
	if err := h.st.ReplaceLessons([]model.Lesson{lSub1, lIOT}); err != nil {
		t.Fatal(err)
	}
	lessons, _ = h.st.ListLessons()
	for _, l := range lessons {
		if l.Owner == uSub2.TelegramID {
			if err := h.st.BindLessonToPack(l.ID, p.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.api.Reset()
	h.w.announceNotes(context.Background(), p)
	if len(h.sentTo(uSub1.TelegramID)) != 1 || len(h.sentTo(uSub2.TelegramID)) != 1 {
		t.Fatalf("ожидали по 1 сообщению uSub1 и uSub2: u1=%d u2=%d", len(h.sentTo(uSub1.TelegramID)), len(h.sentTo(uSub2.TelegramID)))
	}

	// Если в расписании урок только для отключённого студента (никто из активных не подходит),
	// рассылка уходит админу, а чужой подгруппе не уходит.
	h.api.Reset()
	if err := h.st.SetEnabled(uSub1.TelegramID, false); err != nil {
		t.Fatal(err)
	}
	if err := h.st.ReplaceLessons([]model.Lesson{lSub1}); err != nil {
		t.Fatal(err)
	}
	h.w.announceNotes(context.Background(), p)
	if len(h.sentTo(uSub2.TelegramID)) != 0 {
		t.Fatalf("чужая подгруппа не должна получать уведомление: %d", len(h.sentTo(uSub2.TelegramID)))
	}
	h.wantSent(adminTG, "Конспект готов")
}

func TestBuildNotesClaimLeasePreventsDuplicate(t *testing.T) {
	h := newWorkerHarness(t)
	l := h.lesson("Сети", "Лекция", 3*time.Hour)
	p := h.pack(l, model.PackRecorded)
	h.write(p.Audio, strings.Repeat("a", minRecordedBytes+1))

	// 1. Активный lease от другого воркера не даёт запустить сборку и не накручивает попытки.
	claimed, err := h.st.ClaimPackForNotes(p.ID, time.Now().UTC())
	if err != nil || !claimed {
		t.Fatalf("ClaimPackForNotes: claimed=%v err=%v", claimed, err)
	}
	if ok := h.w.buildNotes(context.Background(), p.ID); !ok {
		t.Fatal("buildNotes при активном чужом lease должен вернуть true без ошибки")
	}
	if attempts := h.w.noteAttemptsFor(p.ID); attempts != 0 {
		t.Fatalf("попытки не должны расти при чужом lease: %d", attempts)
	}
	if got, _ := h.st.PackByID(p.ID); got.Status != model.PackRecorded {
		t.Fatalf("статус не должен меняться при чужом lease, got %s", got.Status)
	}
	// buildNotesDay не должен закрывать день, пока пак не завершён другим воркером.
	if done := h.w.buildNotesDay(context.Background(), p.Date); done {
		t.Fatal("buildNotesDay не должен закрывать день, пока другой воркер держит lease")
	}

	// Освобождаем lease и проверяем конкурентный вызов buildNotes из двух горутин.
	if err := h.st.ReleasePackClaim(p.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			h.w.buildNotes(context.Background(), p.ID)
		}()
	}
	wg.Wait()

	if attempts := h.w.noteAttemptsFor(p.ID); attempts != 1 {
		t.Fatalf("конкурентный запуск не должен выполнять две сборки: attempts=%d", attempts)
	}
	got, _ := h.st.PackByID(p.ID)
	if got.Status != model.PackError || got.ClaimedAt != nil {
		t.Fatalf("после ошибки статус должен быть error и claimed_at сброшен: %+v", got)
	}
}

func TestPublishAndCleanPackEmptyPathsDoNotTouchRecRoot(t *testing.T) {
	h := newWorkerHarness(t)
	paths := fakeGitHub(t, false)
	h.w.Cfg.GitHubToken, h.w.Cfg.GitHubOwner, h.w.Cfg.GitHubRepo, h.w.Cfg.GitHubBranch = "tok", "o", "r", "main"

	// Файл в корне recordings и слайды в корне recordings не должны пострадать.
	keepSlide := filepath.Join(h.w.recRoot(), "slides", "keep.png")
	if err := os.MkdirAll(filepath.Dir(keepSlide), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keepSlide, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := h.pack(h.lesson("Сети", "Лекция", 4*time.Hour), model.PackDone)
	p.Audio = ""
	if err := h.st.SavePack(p); err != nil {
		t.Fatal(err)
	}
	h.write(filepath.Join(p.Dir, "transcript.txt"), "текст")
	h.write(filepath.Join(p.Dir, "notes.pdf"), "%PDF")

	h.w.publishPack(context.Background(), p)
	if len(*paths) != 2 {
		t.Fatalf("ожидали публикацию 2 файлов, получили %v", *paths)
	}
	if _, err := os.Stat(h.w.recRoot()); err != nil {
		t.Fatalf("recRoot должен остаться на месте: %v", err)
	}

	h.w.cleanPack(nil)
	h.w.cleanPack(&model.LecturePack{Dir: "   "})
	if _, err := os.Stat(keepSlide); err != nil {
		t.Fatalf("cleanPack с пустым Dir не должен трогать recRoot/slides: %v", err)
	}
}

func TestForgetCleansStaleMeetingKeys(t *testing.T) {
	h := newWorkerHarness(t)
	now := time.Now().UTC()
	activeMK := "bbb:https://bbb.ssau.ru/b/active|2026-09-15|10:00"
	staleMK := "bbb:https://bbb.ssau.ru/b/stale|2026-09-15|08:00"

	h.w.mu.Lock()
	h.w.recFailAt[activeMK] = now
	h.w.recFailN[activeMK] = 1
	h.w.recFailAt[staleMK] = now
	h.w.recFailN[staleMK] = 2
	h.w.sessions["10:1"] = stubSess{room: true}
	h.w.recorder[activeMK] = "10:1"
	h.w.mu.Unlock()

	h.w.closeSession(context.Background(), 10, 1, "10:1", "done", false)
	h.w.forget(context.Background(), map[int64]struct{}{1: {}}, map[string]struct{}{activeMK: {}})

	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	if _, ok := h.w.recorder[activeMK]; ok {
		t.Fatal("closeSession должен удалять рекордера по ключу встречи")
	}
	if _, ok := h.w.recFailAt[activeMK]; !ok {
		t.Fatal("актуальный meetKey не должен удаляться из recFailAt")
	}
	if _, ok := h.w.recFailAt[staleMK]; ok {
		t.Fatal("устаревший meetKey должен удаляться из recFailAt")
	}
	if _, ok := h.w.recFailN[staleMK]; ok {
		t.Fatal("устаревший meetKey должен удаляться из recFailN")
	}
}

func TestBuildNotesDayKeepsOpenOnConcurrentClaimOrError(t *testing.T) {
	h := newWorkerHarness(t)
	l := h.lesson("Сети", "Лекция", 3*time.Hour)
	p := h.pack(l, model.PackRecorded)

	// 1. Активный чужой lease на паке в статусе PackNotes не должен закрывать день.
	now := time.Now().UTC()
	claimed, err := h.st.ClaimPackForNotes(p.ID, now)
	if err != nil || !claimed {
		t.Fatalf("ClaimPackForNotes: claimed=%v err=%v", claimed, err)
	}
	p.Status = model.PackNotes
	p.ClaimedAt = &now
	if err := h.st.SavePack(p); err != nil {
		t.Fatalf("SavePack PackNotes: %v", err)
	}
	if done := h.w.buildNotesDay(context.Background(), p.Date); done {
		t.Fatal("buildNotesDay не должен закрывать день, пока пак в PackNotes под чужим lease")
	}

	// 2. Переход в PackError с оставшимися попытками держит день открытым,
	// а после исчерпания попыток или отсутствия пака проверяем packNeedsDayOpen.
	p.Status = model.PackError
	if err := h.st.SavePack(p); err != nil {
		t.Fatalf("SavePack PackError: %v", err)
	}
	h.w.noteFail(p.ID)
	if !h.w.packNeedsDayOpen(p.ID) {
		t.Fatal("packNeedsDayOpen должен возвращать true для PackError с оставшимися попытками")
	}
	for h.w.noteAttemptsFor(p.ID) < maxNoteAttempts {
		h.w.noteFail(p.ID)
	}
	if h.w.packNeedsDayOpen(p.ID) {
		t.Fatal("packNeedsDayOpen должен возвращать false после исчерпания попыток")
	}
	if !h.w.packNeedsDayOpen(999999) {
		t.Fatal("packNeedsDayOpen должен возвращать true, если пак не найден")
	}
}

type blockingRecordJoiner struct {
	mu      sync.Mutex
	reqs    []JoinReq
	started chan struct{}
	release chan struct{}
	once    sync.Once
	sess    Session
}

func (j *blockingRecordJoiner) Join(_ context.Context, req JoinReq) (Session, error) {
	j.mu.Lock()
	j.reqs = append(j.reqs, req)
	j.mu.Unlock()
	if req.Role == RoleRecord {
		j.once.Do(func() { close(j.started) })
		<-j.release
	}
	return j.sess, nil
}

func (j *blockingRecordJoiner) requests() []JoinReq {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]JoinReq(nil), j.reqs...)
}

func TestSecondTickDoesNotStartSecondRecorderWhileFirstJoining(t *testing.T) {
	h := newWorkerHarness(t)
	recs, _ := useFakeRecorder(t, make([]byte, 3200))

	bj := &blockingRecordJoiner{
		started: make(chan struct{}),
		release: make(chan struct{}),
		sess:    &fakeSess{room: true},
	}
	h.w.Joiner = bj

	u1 := model.User{TelegramID: 201, FIO: "Первый", Subgroup: 1, Enabled: true, Onboarded: true, OnboardStage: model.StageDone}
	u2 := model.User{TelegramID: 202, FIO: "Второй", Subgroup: 2, Enabled: true, Onboarded: true, OnboardStage: model.StageDone}
	if err := h.st.UpsertUser(&u1); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpsertUser(&u2); err != nil {
		t.Fatal(err)
	}

	begin := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	sharedURL := "https://bbb.ssau.ru/b/slow-join-room"
	l1 := model.Lesson{
		Date: begin.Format("2006-01-02"), Start: begin.Format("15:04"), End: begin.Add(90 * time.Minute).Format("15:04"),
		Begin: begin, Finish: begin.Add(90 * time.Minute),
		Discipline: "Операционные системы", Type: "Лекция", Subgroup: 1, Online: true, BBBURL: sharedURL,
	}
	l2 := l1
	l2.Subgroup = 2
	if err := h.st.ReplaceLessons([]model.Lesson{l1, l2}); err != nil {
		t.Fatal(err)
	}
	lessons, err := h.st.ListLessons()
	if err != nil || len(lessons) != 2 {
		t.Fatalf("lessons = %+v err=%v", lessons, err)
	}
	l1, l2 = lessons[0], lessons[1]
	h.yes(u1.TelegramID, l1)
	h.yes(u2.TelegramID, l2)

	// Тик 1: u1 идёт в долгий Join (RoleRecord), u2 быстро садится как presence.
	h.w.tick(context.Background())

	select {
	case <-bj.started:
	case <-time.After(2 * time.Second):
		close(bj.release)
		t.Fatal("первый рекордер не начал Join")
	}

	u1Key := sessionKey(u1.TelegramID, l1.ID)
	u2Key := sessionKey(u2.TelegramID, l2.ID)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.w.mu.Lock()
		_, u2Seated := h.w.sessions[u2Key]
		h.w.mu.Unlock()
		if u2Seated {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.w.mu.Lock()
	_, u2Seated := h.w.sessions[u2Key]
	h.w.mu.Unlock()
	if !u2Seated {
		close(bj.release)
		t.Fatal("presence-сессия u2 не успела сесть")
	}

	// Тик 2 во время долгого захода u1: u2 НЕ должен переоткрываться вторым рекордером.
	h.w.tick(context.Background())
	time.Sleep(30 * time.Millisecond)

	h.w.mu.Lock()
	_, u2StillSeated := h.w.sessions[u2Key]
	h.w.mu.Unlock()
	if !u2StillSeated {
		close(bj.release)
		t.Fatal("повторный тик закрыл presence-сессию u2 для второго рекордера")
	}
	recReqCount := 0
	for _, req := range bj.requests() {
		if req.Role == RoleRecord {
			recReqCount++
		}
	}
	if recReqCount != 1 {
		close(bj.release)
		t.Fatalf("ожидали ровно 1 запрос RoleRecord, получили %d (%+v)", recReqCount, bj.requests())
	}

	close(bj.release)
	h.w.WaitIdle()

	meetKey := meetingGroupKey(l1, sharedURL)
	h.w.mu.Lock()
	gotRec := h.w.recorder[meetKey]
	gotJoining := h.w.recJoining[meetKey]
	h.w.mu.Unlock()
	if gotRec != u1Key {
		t.Fatalf("ожидали рекордера %s, получили %s", u1Key, gotRec)
	}
	if gotJoining != "" {
		t.Fatalf("recJoining должен очиститься после завершения захода, получили %q", gotJoining)
	}
	if len(*recs) != 1 {
		t.Fatalf("ожидали ровно 1 запущенный рекордер, получили %d", len(*recs))
	}

	// Прямой вызов ensureIn(record=true) при уже живом рекордере не запускает второй attachRecorder.
	u3 := model.User{TelegramID: 203, FIO: "Третий", Subgroup: 1, Enabled: true, Onboarded: true, OnboardStage: model.StageDone}
	h.w.ensureIn(context.Background(), u3, l1, sharedURL, sessionKey(u3.TelegramID, l1.ID), time.Now(), true)
	if len(*recs) != 1 {
		t.Fatalf("ensureIn при живом рекордере не должен запускать второй attachRecorder, recs=%d", len(*recs))
	}
}

func TestAnnounceNotesNoBroadcastWhenLessonsMissingAndNoHistory(t *testing.T) {
	h := newWorkerHarness(t)
	u1 := h.user(101, "Иванов")
	u2 := h.user(102, "Петров")

	l := h.lesson("Философия", "Лекция", 3*time.Hour)
	p := h.pack(l, model.PackDone)

	// Уроки ушли из таблицы lessons, истории (join_intents/events) нет:
	// broadcast всем студентам не допускается, уведомляется админ.
	if err := h.st.ReplaceLessons(nil); err != nil {
		t.Fatal(err)
	}
	h.api.Reset()
	h.w.announceNotes(context.Background(), p)
	if len(h.sentTo(u1.TelegramID)) != 0 || len(h.sentTo(u2.TelegramID)) != 0 {
		t.Fatalf("не должно быть broadcast студентам при удалённых уроках без истории: u1=%d u2=%d",
			len(h.sentTo(u1.TelegramID)), len(h.sentTo(u2.TelegramID)))
	}
	h.wantSent(adminTG, "Конспект готов")

	// При появлении истории участия только у u1, уведомление получает только u1.
	now := time.Now().UTC()
	if err := h.st.PutIntent(model.JoinIntent{
		TelegramID: u1.TelegramID, LessonID: l.ID, Decision: model.JoinYes, AskedAt: now, DecidedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	h.api.Reset()
	h.w.announceNotes(context.Background(), p)
	if len(h.sentTo(u1.TelegramID)) != 1 || len(h.sentTo(u2.TelegramID)) != 0 {
		t.Fatalf("должен получить только участник из истории: u1=%d u2=%d",
			len(h.sentTo(u1.TelegramID)), len(h.sentTo(u2.TelegramID)))
	}

	// Синтетический пак вообще без lesson_id (lessonIDs == 0) по-прежнему шлёт всем активным.
	syn, err := h.st.EnsurePack(model.Lesson{Date: "2026-09-15", Discipline: "Синтетика"}, "", h.w.recRoot())
	if err != nil {
		t.Fatal(err)
	}
	syn.Status = model.PackDone
	if err := h.st.SavePack(syn); err != nil {
		t.Fatal(err)
	}
	h.api.Reset()
	h.w.announceNotes(context.Background(), syn)
	if len(h.sentTo(u1.TelegramID)) != 1 || len(h.sentTo(u2.TelegramID)) != 1 {
		t.Fatalf("синтетический пак без lesson_id должен рассылаться активным студентам: u1=%d u2=%d",
			len(h.sentTo(u1.TelegramID)), len(h.sentTo(u2.TelegramID)))
	}
}
