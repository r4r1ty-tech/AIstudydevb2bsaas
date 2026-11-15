package uni

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

// Существующие тесты решения написаны под пул процесса (inline). Режим очереди проверяется
// здесь явно через поле Syncer.QuizMode, поэтому по умолчанию тесты идут в inline.
func TestMain(m *testing.M) {
	_ = os.Setenv("QUIZ_MODE", QuizModeInline)
	os.Exit(m.Run())
}

// shrinkQuizTimings сокращает паузы очереди для теста и выключает разнос старта.
func shrinkQuizTimings(t *testing.T) {
	t.Helper()
	oldPoll, oldJitter := quizPollEvery, quizStartJitter
	quizPollEvery, quizStartJitter = 10*time.Millisecond, 0
	t.Cleanup(func() { quizPollEvery, quizStartJitter = oldPoll, oldJitter })
	// Темп входов в тестах почти не ограничивает, но путь ограничителя проходит.
	t.Setenv("QUIZ_MOODLE_RPS", "100000")
}

// startQueueWorker запускает RunQuizWorker в фоне; возвращает канал, закрываемый при выходе воркера.
func startQueueWorker(sy *Syncer, ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		sy.RunQuizWorker(ctx)
	}()
	return done
}

// waitJobStatus ждёт, пока задача id перейдёт в статус want.
func waitJobStatus(t *testing.T, fx *poolFixture, id int64, want string) {
	t.Helper()
	poolWaitUntil(t, 15*time.Second, "задача "+want, func() bool {
		j, err := fx.st.QuizJobByID(id)
		return err == nil && j != nil && j.Status == want
	})
}

// activeJob возвращает активную задачу теста (queued или running) фикстуры.
func activeJob(t *testing.T, fx *poolFixture) int64 {
	t.Helper()
	j, err := fx.st.ActiveQuizJob(fx.tg, poolQuizCMID)
	if err != nil || j == nil {
		t.Fatalf("активной задачи нет: %+v %v", j, err)
	}
	return j.ID
}

func TestQuizModeFromEnv(t *testing.T) {
	cases := map[string]string{
		"":          QuizModeService,
		"service":   QuizModeService,
		" INLINE ":  QuizModeInline,
		"inline":    QuizModeInline,
		"bogus-val": QuizModeService,
	}
	for v, want := range cases {
		t.Setenv("QUIZ_MODE", v)
		if got := QuizModeFromEnv(); got != want {
			t.Errorf("QUIZ_MODE=%q: got %q, want %q", v, got, want)
		}
	}
	// Явное поле Syncer.QuizMode важнее окружения.
	t.Setenv("QUIZ_MODE", QuizModeInline)
	sy := &Syncer{}
	if sy.quizServiceMode() {
		t.Fatal("QUIZ_MODE=inline: режим очереди включён")
	}
	sy.QuizMode = QuizModeService
	if !sy.quizServiceMode() {
		t.Fatal("поле QuizMode=service не включило очередь")
	}
}

func TestQuizMaxParallelAndRPSEnv(t *testing.T) {
	t.Setenv("QUIZ_MAX_PARALLEL", "")
	if got := QuizMaxParallel(); got != 50 {
		t.Errorf("по умолчанию: %d, want 50", got)
	}
	t.Setenv("QUIZ_MAX_PARALLEL", "7")
	if got := QuizMaxParallel(); got != 7 {
		t.Errorf("QUIZ_MAX_PARALLEL=7: %d", got)
	}
	for _, v := range []string{"", "abc", "NaN", "-2", "0", "Inf"} {
		t.Setenv("QUIZ_MOODLE_RPS", v)
		if got := QuizMoodleRPS(); got != 3 {
			t.Errorf("QUIZ_MOODLE_RPS=%q: %v, want 3", v, got)
		}
	}
	t.Setenv("QUIZ_MOODLE_RPS", "0.5")
	if got := QuizMoodleRPS(); got != 0.5 {
		t.Errorf("QUIZ_MOODLE_RPS=0.5: %v", got)
	}
}

// В режиме очереди синк ставит тест в базу и не решает его сам: ИИ не вызывается.
func TestQuizServiceEnqueuesInsteadOfSolving(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: -1, gated: true})
	fx.sy.QuizMode = QuizModeService
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	fx.sy.Wait()
	j, err := fx.st.ActiveQuizJob(fx.tg, poolQuizCMID)
	if err != nil || j == nil {
		t.Fatalf("задача не поставлена: %+v %v", j, err)
	}
	if j.Status != "queued" || j.Forced {
		t.Fatalf("задача %+v, want queued без forced", j)
	}
	if n := fx.llmCalls.Load(); n != 0 {
		t.Fatalf("синк решил тест сам: запросов к ИИ %d", n)
	}
	if fx.attemptStarts.Load() != 0 {
		t.Fatal("синк начал попытку Moodle в режиме очереди")
	}
}

// Воркер очереди берёт задачу и решает тест тем же путём, что inline: страницы, попытка, решено, пинок.
func TestQuizWorkerSolvesQueuedJob(t *testing.T) {
	shrinkQuizTimings(t)
	fx := newPoolFixture(t, poolOpts{pages: 2, failPage: -1})
	fx.sy.QuizMode = QuizModeService
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	id := activeJob(t, fx)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startQueueWorker(fx.sy, ctx)
	waitJobStatus(t, fx, id, "done")
	fx.checkSolvedAll()

	cancel()
	<-done
	if n := fx.llmCalls.Load(); n != 2 {
		t.Fatalf("запросов к ИИ %d, want 2 (по одному на страницу)", n)
	}
}

// Остановка воркера посреди решения возвращает задачу в queued; аренды и владельца не остаётся.
func TestQuizWorkerStopReturnsJobToQueue(t *testing.T) {
	shrinkQuizTimings(t)
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: -1, gated: true})
	fx.sy.QuizMode = QuizModeService
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	id := activeJob(t, fx)

	ctx, cancel := context.WithCancel(context.Background())
	done := startQueueWorker(fx.sy, ctx)
	fx.waitLLM() // решение идёт: ИИ получил запрос, попытка начата
	cancel()
	<-done

	j, err := fx.st.QuizJobByID(id)
	if err != nil || j == nil {
		t.Fatalf("задача пропала: %v", err)
	}
	if j.Status != "queued" || j.ClaimedBy != "" || j.LeaseUntil != nil {
		t.Fatalf("после остановки задача %+v, want queued без аренды", j)
	}
	if _, ok, _ := fx.st.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", fx.tg, poolQuizCMID)); ok {
		t.Fatal("тест помечен решённым при остановке")
	}
	if fx.finished.Load() {
		t.Fatal("попытка завершена при остановке")
	}
	fx.release()
}

// Воркер умер (SIGKILL) с открытой попыткой: аренда истекает, другой воркер перехватывает задачу,
// продолжает ту же попытку Moodle (startattempt не повторяется) и доводит тест до конца.
func TestQuizWorkerTakesOverAfterKill(t *testing.T) {
	shrinkQuizTimings(t)
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: -1})
	fx.sy.QuizMode = QuizModeService
	base := fx.sy.now()
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	id := activeJob(t, fx)

	// «Мёртвый» воркер захватил задачу, открыл попытку Moodle и пропал без продления аренды.
	dead, err := fx.st.ClaimQuizJob("dead-worker", base, quizLeaseDur)
	if err != nil || dead == nil || dead.ID != id {
		t.Fatalf("захват мёртвым воркером: %+v %v", dead, err)
	}
	cli, err := fx.sy.moodleClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.StartOrResumeAttempt(context.Background(), poolQuizCMID); err != nil {
		t.Fatalf("старт попытки мёртвым воркером: %v", err)
	}
	if n := fx.attemptStarts.Load(); n != 1 {
		t.Fatalf("попыток начато %d, want 1", n)
	}

	// Аренда ещё жива: через 5 минут задача недоступна.
	if j, err := fx.st.ClaimQuizJob("probe", base.Add(5*time.Minute), quizLeaseDur); err != nil || j != nil {
		t.Fatalf("живая аренда отдана другому: %+v %v", j, err)
	}

	// Через 11 минут аренда истекла: воркер сервиса перехватывает задачу.
	later := base.Add(11 * time.Minute)
	nowFn = func() time.Time { return later }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startQueueWorker(fx.sy, ctx)
	waitJobStatus(t, fx, id, "done")
	cancel()
	<-done

	if n := fx.attemptStarts.Load(); n != 1 {
		t.Fatalf("попытка начата заново: startattempt %d раз, want 1", n)
	}
	fx.checkSolvedAll()
	j, err := fx.st.QuizJobByID(id)
	if err != nil || j == nil || j.Attempts != 2 {
		t.Fatalf("после перехвата задача %+v, want attempts=2", j)
	}
}

// Остановка на ограничителе темпа: задача возвращается в очередь, без счётчика неудач.
func TestQuizPacerLimitsAndCancels(t *testing.T) {
	p := newMoodlePacer(100) // интервал 10 мс
	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := p.wait(context.Background()); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
	}
	if d := time.Since(start); d < 35*time.Millisecond {
		t.Fatalf("5 слотов по 10 мс заняли %s: ограничитель не работает", d)
	}

	slow := newMoodlePacer(0.5) // интервал 2 с
	if err := slow.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	began := time.Now()
	if err := slow.wait(ctx); err == nil {
		t.Fatal("отмена не прервала ожидание слота")
	}
	if time.Since(began) > time.Second {
		t.Fatal("отмена прервала ожидание слишком поздно")
	}
}

// В inline-режиме ограничителя нет: вход и старт попытки не ждут.
func TestQuizInlineHasNoPacer(t *testing.T) {
	sy := &Syncer{}
	if err := sy.paceMoodle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sy.quizPacer != nil {
		t.Fatal("в inline-режиме ограничитель создан")
	}
}

// «Пройти» при уже стоящем в очереди тесте: флаг снимается, приоритет задачи поднимается,
// пользователь получает уведомление «уже в очереди».
func TestQuizServiceForcedWhileQueued(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: -1, gated: true})
	fx.sy.QuizMode = QuizModeService
	ctx := context.Background()
	if err := fx.sy.SyncMoodle(ctx, fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	id := activeJob(t, fx)

	item := model.MoodleItem{CMID: poolQuizCMID, Kind: model.MoodleQuiz, Course: "ТФЯГ", Title: "Тест 2", Status: model.MoodleTodo}
	if err := fx.st.SetSetting(QuizForceKey(fx.tg, poolQuizCMID), "1"); err != nil {
		t.Fatal(err)
	}
	cli, err := fx.sy.moodleClient()
	if err != nil {
		t.Fatal(err)
	}
	fx.sy.detectAndHandleOpenedQuizzes(ctx, fx.acc, cli, []model.MoodleItem{item}, []model.MoodleItem{item})

	if !strings.Contains(strings.Join(*fx.sent, "\n"), "Тест уже решается или стоит в очереди") {
		t.Fatalf("нет уведомления о задаче в очереди: %v", *fx.sent)
	}
	if v, _, _ := fx.st.GetSetting(QuizForceKey(fx.tg, poolQuizCMID)); v != "" {
		t.Fatal("флаг «Пройти» не снят")
	}
	j, err := fx.st.QuizJobByID(id)
	if err != nil || j == nil || !j.Forced {
		t.Fatalf("задача после «Пройти» %+v, want forced", j)
	}
	if n := fx.llmCalls.Load(); n != 0 {
		t.Fatalf("«Пройти» в очереди решил тест в синке: %d", n)
	}
}

// Ошибка базы при постановке «Пройти» не выдаётся за «уже решается».
func TestQuizServiceEnqueueErrorIsReported(t *testing.T) {
	f := newFake(t)
	sy, st, _ := setup(t, f)
	sy.QuizMode = QuizModeService
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	acc := model.Account{TelegramID: 9, Kind: model.AccountMoodle}
	it := model.MoodleItem{CMID: poolQuizCMID, Kind: model.MoodleQuiz, Title: "Тест"}
	cli, err := sy.moodleClient()
	if err != nil {
		t.Fatal(err)
	}
	queued, err := sy.dispatchQuiz(context.Background(), acc, cli, it, true)
	if err == nil || queued {
		t.Fatalf("ошибка очереди не вернулась: queued=%v err=%v", queued, err)
	}
}

// Воркер при старте и затем раз в час чистит завершённые задачи старше 14 дней: старая удаляется,
// свежая остаётся.
func TestQuizWorkerPrunesOldFinishedJobs(t *testing.T) {
	shrinkQuizTimings(t)
	f := newFake(t)
	sy, st, _ := setup(t, f)
	base := sy.now()
	old := base.Add(-30 * 24 * time.Hour)

	finishedAt := func(tg, cmid int64, at time.Time) int64 {
		t.Helper()
		if _, err := st.EnqueueQuizJob(store.QuizJob{TelegramID: tg, CMID: cmid}, at); err != nil {
			t.Fatal(err)
		}
		j, err := st.ClaimQuizJob("w0", at, quizLeaseDur)
		if err != nil || j == nil {
			t.Fatalf("захват: %+v %v", j, err)
		}
		if ok, err := st.FinishQuizJob(j.ID, "w0", store.QuizJobDone, "", at); err != nil || !ok {
			t.Fatalf("finish: %v %v", ok, err)
		}
		return j.ID
	}
	oldID := finishedAt(1, 911, old)
	freshID := finishedAt(2, 912, base)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startQueueWorker(sy, ctx)
	poolWaitUntil(t, 10*time.Second, "старая задача удалена", func() bool {
		j, err := st.QuizJobByID(oldID)
		return err == nil && j == nil
	})
	cancel()
	<-done

	if j, err := st.QuizJobByID(freshID); err != nil || j == nil {
		t.Fatalf("свежая задача удалена: %+v %v", j, err)
	}
}

// Срок аренды: граница включительно, дальше — протухла.
func TestQuizLeaseStaleBoundary(t *testing.T) {
	last := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	if quizLeaseStale(last, last.Add(quizLeaseDur)) {
		t.Fatal("аренда на границе срока помечена протухшей")
	}
	if !quizLeaseStale(last, last.Add(quizLeaseDur+time.Second)) {
		t.Fatal("аренда за пределом срока не помечена протухшей")
	}
}

// База перестала отвечать на продление дольше срока аренды: продлитель отменяет решение, чтобы оно не шло
// параллельно с возможным перехватом задачи другим воркером. Часы реальные, срок аренды в тесте 200 мс;
// задача, которую решает сам процесс, в цикле захвата не перехватывается (см. inflight).
func TestQuizWorkerStopsWhenLeaseCannotBeRenewed(t *testing.T) {
	shrinkQuizTimings(t)
	oldRenew, oldLease := quizRenewEvery, quizLeaseDur
	t.Cleanup(func() { quizRenewEvery, quizLeaseDur = oldRenew, oldLease })
	quizRenewEvery, quizLeaseDur = 5*time.Millisecond, 200*time.Millisecond

	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: -1, gated: true})
	fx.sy.QuizMode = QuizModeService
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	activeJob(t, fx)
	nowFn = time.Now

	ctx, cancel := context.WithCancel(context.Background())
	done := startQueueWorker(fx.sy, ctx)
	// Остановка воркера при любом исходе теста: горутины не переживают тест.
	t.Cleanup(func() { cancel(); <-done })
	fx.waitLLM() // решение идёт, аренда выдана

	// База недоступна: продление падает, срок аренды проходит, продлитель отменяет решение.
	if err := fx.st.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	fx.release() // ИИ ответит только сейчас: если решение не отменено, оно дойдёт до конца
	time.Sleep(200 * time.Millisecond)

	if fx.finished.Load() {
		t.Fatal("попытка завершена после потери аренды")
	}
	fx.mu.Lock()
	submitted := len(fx.submitted)
	fx.mu.Unlock()
	if submitted != 0 {
		t.Fatalf("страницы отправлены после потери аренды: %d", submitted)
	}
}

// Частично решённый тест: часть страниц ушла в Moodle, остальные нет. Задача падает в failed
// с отдельной причиной «частично решён», а не с общей «попытка не завершилась».
func TestQuizPartialSolveGetsOwnReason(t *testing.T) {
	shrinkQuizTimings(t)
	fx := newPoolFixture(t, poolOpts{pages: 3, failPage: 1})
	fx.sy.QuizMode = QuizModeService
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	id := activeJob(t, fx)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startQueueWorker(fx.sy, ctx)
	waitJobStatus(t, fx, id, "failed")
	cancel()
	<-done

	j, err := fx.st.QuizJobByID(id)
	if err != nil || j == nil {
		t.Fatalf("задача: %v", err)
	}
	if !strings.Contains(j.LastError, "частично решён") || !strings.Contains(j.LastError, "попытка не завершена") {
		t.Fatalf("last_error = %q, want причина частичного решения", j.LastError)
	}
	if strings.Contains(j.LastError, "попытка решения не завершилась") {
		t.Fatalf("общая причина вместо частичной: %q", j.LastError)
	}
	if fx.finished.Load() {
		t.Fatal("попытка завершена при пропущенной странице")
	}
}

// Задача, захваченная QuizMaxClaims раз, в очередь воркера не попадает: нет даже вызова решения.
func TestQuizWorkerSkipsPoisonedJob(t *testing.T) {
	shrinkQuizTimings(t)
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: -1})
	fx.sy.QuizMode = QuizModeService
	base := fx.sy.now()
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	id := activeJob(t, fx)
	for i := 0; i < store.QuizMaxClaims; i++ {
		at := base.Add(time.Duration(i) * 11 * time.Minute)
		if j, err := fx.st.ClaimQuizJob("crash", at, quizLeaseDur); err != nil || j == nil {
			t.Fatalf("захват %d: %+v %v", i+1, j, err)
		}
	}
	nowFn = func() time.Time { return base.Add(time.Duration(store.QuizMaxClaims) * 11 * time.Minute) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startQueueWorker(fx.sy, ctx)
	waitJobStatus(t, fx, id, "failed")
	cancel()
	<-done
	if n := fx.attemptStarts.Load(); n != 0 {
		t.Fatalf("попытка начата для задачи сверх лимита захватов: %d", n)
	}
	j, _ := fx.st.QuizJobByID(id)
	if j == nil || j.LastError != store.QuizClaimLimitError {
		t.Fatalf("last_error = %+v", j)
	}
}
