package uni

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/vault"
)

// Проверка парсинга MOODLE_WORKERS (дефолт 4, кастомное значение, невалидное значение)
// и поля MoodleWorkers структуры Syncer.
func TestMoodleWorkersLimit(t *testing.T) {
	// 1. Дефолтное значение при пустой переменной
	t.Setenv("MOODLE_WORKERS", "")
	if got := MoodleWorkersLimit(); got != 4 {
		t.Errorf("MoodleWorkersLimit() для пустой env = %d; want 4", got)
	}

	// 2. Корректное кастомное значение
	t.Setenv("MOODLE_WORKERS", "8")
	if got := MoodleWorkersLimit(); got != 8 {
		t.Errorf("MoodleWorkersLimit() для env '8' = %d; want 8", got)
	}

	// 3. Невалидные значения: 0, отрицательное, строка
	t.Setenv("MOODLE_WORKERS", "0")
	if got := MoodleWorkersLimit(); got != 4 {
		t.Errorf("MoodleWorkersLimit() для env '0' = %d; want 4", got)
	}

	t.Setenv("MOODLE_WORKERS", "-5")
	if got := MoodleWorkersLimit(); got != 4 {
		t.Errorf("MoodleWorkersLimit() для env '-5' = %d; want 4", got)
	}

	t.Setenv("MOODLE_WORKERS", "invalid_num")
	if got := MoodleWorkersLimit(); got != 4 {
		t.Errorf("MoodleWorkersLimit() для env 'invalid_num' = %d; want 4", got)
	}

	// 4. Поле Syncer.MoodleWorkers имеет приоритет над переменной окружения
	t.Setenv("MOODLE_WORKERS", "2")
	sy := &Syncer{MoodleWorkers: 10}
	if got := sy.workersLimit(); got != 10 {
		t.Errorf("sy.workersLimit() = %d; want 10", got)
	}

	syDefault := &Syncer{}
	if got := syDefault.workersLimit(); got != 2 {
		t.Errorf("syDefault.workersLimit() = %d; want 2 (из env)", got)
	}
}

// Проверка приоритета kicked аккаунтов перед scheduled.
func TestSortMoodleTasksPriority(t *testing.T) {
	tasks := []moodleTask{
		{acc: model.Account{TelegramID: 1}, kicked: false, trigger: "scheduled_1"},
		{acc: model.Account{TelegramID: 2}, kicked: true, trigger: "kick_user"},
		{acc: model.Account{TelegramID: 3}, kicked: false, trigger: "scheduled_2"},
		{acc: model.Account{TelegramID: 4}, kicked: true, trigger: "wake_kick"},
	}

	sortMoodleTasks(tasks)

	// Первые две задачи должны иметь kicked == true (сигнальные)
	if !tasks[0].kicked || !tasks[1].kicked {
		t.Fatalf("первые две задачи должны быть сигнальными: got kicked=[%v, %v]", tasks[0].kicked, tasks[1].kicked)
	}
	if tasks[0].acc.TelegramID != 2 || tasks[1].acc.TelegramID != 4 {
		t.Errorf("ожидался порядок сигнальных задач [2, 4], получено [%d, %d]", tasks[0].acc.TelegramID, tasks[1].acc.TelegramID)
	}

	// Последние две задачи должны быть плановыми (scheduled)
	if tasks[2].kicked || tasks[3].kicked {
		t.Fatalf("последние две задачи должны быть плановыми: got kicked=[%v, %v]", tasks[2].kicked, tasks[3].kicked)
	}
	if tasks[2].acc.TelegramID != 1 || tasks[3].acc.TelegramID != 3 {
		t.Errorf("ожидался порядок плановых задач [1, 3], получено [%d, %d]", tasks[2].acc.TelegramID, tasks[3].acc.TelegramID)
	}
}

// Проверка tryLockAccount/unlockAccount и tryLockQuiz/unlockQuiz при конкурентном доступе.
func TestConcurrentLocks(t *testing.T) {
	sy := &Syncer{}

	// --- 1. tryLockAccount / unlockAccount ---
	const goroutines = 50
	var successCount int64
	var startWg sync.WaitGroup
	var doneWg sync.WaitGroup
	startWg.Add(goroutines)
	doneWg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			startWg.Done()
			startWg.Wait() // одновременный старт
			if sy.tryLockAccount(100) {
				atomic.AddInt64(&successCount, 1)
			}
			doneWg.Done()
		}()
	}
	doneWg.Wait()

	if successCount != 1 {
		t.Fatalf("ровно одна горутина должна была захватить лок аккаунта, захватили: %d", successCount)
	}

	// Повторный захват должен вернуть false
	if sy.tryLockAccount(100) {
		t.Fatal("tryLockAccount для уже занятого аккаунта должен вернуть false")
	}

	// После освобождения аккаунт снова можно залочить
	sy.unlockAccount(100)
	if !sy.tryLockAccount(100) {
		t.Fatal("tryLockAccount после unlockAccount должен вернуть true")
	}
	sy.unlockAccount(100)

	// --- 2. tryLockQuiz / unlockQuiz ---
	atomic.StoreInt64(&successCount, 0)
	startWg.Add(goroutines)
	doneWg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			startWg.Done()
			startWg.Wait()
			if sy.TryLockQuiz(100, "quiz_42") {
				atomic.AddInt64(&successCount, 1)
			}
			doneWg.Done()
		}()
	}
	doneWg.Wait()

	if successCount != 1 {
		t.Fatalf("ровно одна горутина должна была захватить лок теста, захватили: %d", successCount)
	}

	// Другой тест для того же пользователя должен успешно лочиться
	if !sy.TryLockQuiz(100, "quiz_99") {
		t.Fatal("другой тест для того же пользователя должен успешно блокироваться")
	}
	sy.UnlockQuiz(100, "quiz_99")

	// Тот же тест для другого пользователя должен успешно лочиться
	if !sy.TryLockQuiz(200, "quiz_42") {
		t.Fatal("тот же тест для другого пользователя должен успешно блокироваться")
	}
	sy.UnlockQuiz(200, "quiz_42")

	// Повторная блокировка того же теста
	if sy.TryLockQuiz(100, "quiz_42") {
		t.Fatal("повторный tryLockQuiz для занятого теста должен вернуть false")
	}

	// После освобождения тест снова доступен для блокировки
	sy.UnlockQuiz(100, "quiz_42")
	if !sy.TryLockQuiz(100, "quiz_42") {
		t.Fatal("tryLockQuiz после UnlockQuiz должен вернуть true")
	}
	sy.UnlockQuiz(100, "quiz_42")
}

// Проверка того, что moodle_wake_kick снимается ДО начала работы воркеров,
// и новый wake_kick, пришедший во время обхода, не теряется.
func TestWakeKickClearedBeforeWorkers(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	v, err := vault.Load("", filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatalf("vault.Load: %v", err)
	}
	sec, err := v.Seal("pass123")
	if err != nil {
		t.Fatalf("seal pass: %v", err)
	}

	// Добавляем аккаунт
	if err := st.PutAccount(model.Account{
		TelegramID: 101,
		Kind:       model.AccountMoodle,
		Status:     model.AccountOK,
		Login:      "student1",
		Secret:     sec,
	}); err != nil {
		t.Fatalf("PutAccount: %v", err)
	}

	// Устанавливаем wake_kick
	if err := WakeKick(st); err != nil {
		t.Fatalf("WakeKick: %v", err)
	}
	if !hasWakeKick(st) {
		t.Fatal("hasWakeKick должен быть true до старта обхода")
	}

	workerStarted := make(chan struct{})
	workerCanFinish := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-workerStarted:
		default:
			close(workerStarted)
		}

		// Проверяем: прямо во время выполнения воркера wake_kick уже ДОЛЖЕН быть снят!
		if hasWakeKick(st) {
			t.Error("moodle_wake_kick должен быть сброшен ДО начала работы воркеров!")
		}

		// Имитируем приход нового wake_kick во время долгого обхода
		_ = WakeKick(st)

		<-workerCanFinish
		http.Error(w, "mock error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	sy := &Syncer{
		Store:      st,
		Vault:      v,
		MoodleBase: srv.URL,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Запускаем тик
	sy.moodleTick(ctx)

	// Ждем, пока воркер дойдет до HTTP сервера
	select {
	case <-workerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("воркер не запустился вовремя")
	}

	// Отпускаем воркер и ждем завершения
	close(workerCanFinish)
	sy.Wait()

	// Проверяем: новый wake_kick, поставленный во время работы воркера, НЕ должен был стереться!
	if !hasWakeKick(st) {
		t.Error("новый wake_kick, пришедший во время обхода, был потерян (затерт)!")
	}
}

// Проверка сохранения старых заданий курса из prev при ошибке обхода одного курса.
func TestCollectMoodlePreservesPrevItemsOnCourseError(t *testing.T) {
	mux := http.NewServeMux()

	// 1. /my/ возвращает sesskey
	mux.HandleFunc("/my/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!DOCTYPE html><html><head><script>M.cfg = {"sesskey":"TESTKEY123"};</script></head><body></body></html>`))
	})

	// 2. /lib/ajax/service.php возвращает список курсов и пустой timeline
	mux.HandleFunc("/lib/ajax/service.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		method := r.URL.Query().Get("info")
		switch method {
		case "core_course_get_enrolled_courses_by_timeline_classification":
			resp := []struct {
				Data struct {
					Courses []moodle.Course `json:"courses"`
				} `json:"data"`
			}{
				{
					Data: struct {
						Courses []moodle.Course `json:"courses"`
					}{
						Courses: []moodle.Course{
							{ID: 101, FullName: "Архитектура ЭВМ"},
							{ID: 102, FullName: "Операционные системы"},
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "core_calendar_get_action_events_by_timesort":
			resp := []struct {
				Data struct {
					Events []moodle.Event `json:"events"`
				} `json:"data"`
			}{
				{
					Data: struct {
						Events []moodle.Event `json:"events"`
					}{
						Events: nil,
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.Write([]byte(`[{"data":{}}]`))
		}
	})

	// 3. Курс 101 (Архитектура ЭВМ) возвращает ошибку 500
	mux.HandleFunc("/course/view.php", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "101" {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		// Курс 102 (Операционные системы) возвращает 1 модуль задания
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`
			<!DOCTYPE html>
			<html>
			<body>
				<li class="section main">
					<li class="activity modtype_assign" id="module-2001">
						<div class="activityname">
							<a href="/mod/assign/view.php?id=2001">
								<span class="instancename">Лабораторная работа 1 по ОС</span>
							</a>
						</div>
					</li>
				</li>
			</body>
			</html>
		`))
	})

	// 4. Страница задания для курса 102
	mux.HandleFunc("/mod/assign/view.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`
			<!DOCTYPE html>
			<html>
			<body>
				<div class="activity-description">Выполните лабу по ОС</div>
			</body>
			</html>
		`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := moodle.New(srv.URL)
	if err != nil {
		t.Fatalf("moodle.New: %v", err)
	}

	sy := &Syncer{
		MoodleBase: srv.URL,
	}

	// Старые задания из БД:
	// - Два задания по курсу 101 (Архитектура ЭВМ, id=101)
	// - Одно старое задание по курсу 102 (Операционные системы, id=102)
	prev := []model.MoodleItem{
		{
			CMID:     1001,
			CourseID: 101,
			Course:   "Архитектура ЭВМ",
			Kind:     model.MoodleAssign,
			Title:    "Старое ДЗ по архитектуре",
			Status:   model.MoodleTodo,
		},
		{
			CMID:     1002,
			CourseID: 101,
			Course:   "Архитектура ЭВМ",
			Kind:     model.MoodleQuiz,
			Title:    "Старый тест по архитектуре",
			Status:   model.MoodleDone,
		},
		{
			CMID:     1999,
			CourseID: 102,
			Course:   "Операционные системы",
			Kind:     model.MoodleAssign,
			Title:    "Устаревшая лаба по ОС",
			Status:   model.MoodleTodo,
		},
	}

	items, err := sy.collectMoodle(context.Background(), cli, 101, prev, true)
	if err != nil {
		t.Fatalf("collectMoodle вернул неожиданную ошибку: %v", err)
	}

	byCM := make(map[int64]model.MoodleItem)
	for _, it := range items {
		byCM[it.CMID] = it
	}

	// 1. Задания упавшего курса 101 должны быть сохранены из prev!
	if item1, ok := byCM[1001]; !ok {
		t.Errorf("задание cmid=1001 по упавшему курсу 101 должно было сохраниться из prev")
	} else if item1.Title != "Старое ДЗ по архитектуре" {
		t.Errorf("cmid=1001: ожидался заголовок 'Старое ДЗ по архитектуре', получено %q", item1.Title)
	}

	if item2, ok := byCM[1002]; !ok {
		t.Errorf("задание cmid=1002 по упавшему курсу 101 должно было сохраниться из prev")
	} else if item2.Title != "Старый тест по архитектуре" {
		t.Errorf("cmid=1002: ожидался заголовок 'Старый тест по архитектуре', получено %q", item2.Title)
	}

	// 2. По успешно опрошенному курсу 102 должно появиться новое задание cmid=2001
	if itemNew, ok := byCM[2001]; !ok {
		t.Errorf("новое задание cmid=2001 по курсу 102 должно быть добавлено")
	} else if itemNew.Title != "Лабораторная работа 1 по ОС" {
		t.Errorf("cmid=2001: ожидался заголовок 'Лабораторная работа 1 по ОС', получено %q", itemNew.Title)
	}

	// 3. Старое задание по успешно опрошенному курсу 102 (cmid=1999), которого нет на сайте, не должно остаться
	if _, ok := byCM[1999]; ok {
		t.Errorf("старое удаленное задание cmid=1999 по курсу 102 не должно было остаться при успешном обходе")
	}
}

// Проверка withWakeKicked/isWakeKicked и того, что detectAndHandleOpenedQuizzes
// видит wakeKicked из контекста даже после удаления WakeKickKey из Store,
// а также сохраняет обратную совместимость при наличии WakeKickKey в Store.
func TestWithWakeKickedAndDetectOpenedQuizzes(t *testing.T) {
	baseCtx := context.Background()
	if isWakeKicked(baseCtx) {
		t.Fatal("isWakeKicked(Background) должен быть false")
	}
	wakeCtx := withWakeKicked(withTrigger(baseCtx, "вейкворд на паре (wake_kick)"))
	if !isWakeKicked(wakeCtx) {
		t.Fatal("isWakeKicked(withWakeKicked(...)) должен быть true")
	}
	if got := syncTrigger(wakeCtx, "fallback"); got != "вейкворд на паре (wake_kick)" {
		t.Fatalf("syncTrigger = %q; want %q", got, "вейкворд на паре (wake_kick)")
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	// Помечаем тест решённым, чтобы processQuiz сразу выходил без сетевых вызовов,
	// а мы проверяли именно уведомление от detectAndHandleOpenedQuizzes.
	const tgID int64 = 505
	const cmid int64 = 777
	if err := st.SetSetting(fmt.Sprintf("quiz_solved:%d:%d", tgID, cmid), "1"); err != nil {
		t.Fatalf("SetSetting quiz_solved: %v", err)
	}

	// Устанавливаем и сразу снимаем WakeKickKey, как это делает moodleTick до запуска воркеров.
	if err := WakeKick(st); err != nil {
		t.Fatalf("WakeKick: %v", err)
	}
	clearWakeKick(st)
	if hasWakeKick(st) {
		t.Fatal("WakeKickKey должен быть снят перед вызовом detectAndHandleOpenedQuizzes")
	}

	var notes []string
	sy := &Syncer{
		Store: st,
		Notify: func(_ context.Context, _ int64, text string) {
			notes = append(notes, text)
		},
	}
	cli, err := moodle.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("moodle.New: %v", err)
	}
	acc := model.Account{TelegramID: tgID, Kind: model.AccountMoodle}
	openQuiz := model.MoodleItem{
		CMID:   cmid,
		Course: "Алгоритмы",
		Kind:   model.MoodleQuiz,
		Title:  "Летучка",
		Status: model.MoodleTodo,
	}

	// 1. Без флага в контексте и при пустом prev уведомлений быть не должно.
	sy.detectAndHandleOpenedQuizzes(baseCtx, acc, cli, nil, []model.MoodleItem{openQuiz})
	if len(notes) != 0 {
		t.Fatalf("без wakeKicked и при пустом prev не ждали уведомлений, получили: %v", notes)
	}

	// 2. С withWakeKicked в контексте, даже если WakeKickKey удалён из Store и prev пустой,
	// тест должен быть замечен и отправлено уведомление.
	sy.detectAndHandleOpenedQuizzes(wakeCtx, acc, cli, nil, []model.MoodleItem{openQuiz})
	if len(notes) != 1 || !strings.Contains(notes[0], "Открылся тест в Moodle!") {
		t.Fatalf("с wakeCtx и пустым prev ждали уведомление о тесте, получили: %v", notes)
	}

	// 3. Тест уже был открыт в prev: без wakeKicked уведомления нет, с wakeCtx — есть.
	notes = nil
	sy.detectAndHandleOpenedQuizzes(baseCtx, acc, cli, []model.MoodleItem{openQuiz}, []model.MoodleItem{openQuiz})
	if len(notes) != 0 {
		t.Fatalf("без wakeKicked для уже открытого теста не ждали уведомлений, получили: %v", notes)
	}
	sy.detectAndHandleOpenedQuizzes(wakeCtx, acc, cli, []model.MoodleItem{openQuiz}, []model.MoodleItem{openQuiz})
	if len(notes) != 1 || !strings.Contains(notes[0], "Открылся тест в Moodle!") {
		t.Fatalf("с wakeCtx для открытого теста ждали уведомление, получили: %v", notes)
	}

	// 4. Обратная совместимость: прямой вызов с обычным ctx, когда WakeKickKey есть в Store.
	notes = nil
	if err := WakeKick(st); err != nil {
		t.Fatalf("WakeKick: %v", err)
	}
	sy.detectAndHandleOpenedQuizzes(baseCtx, acc, cli, []model.MoodleItem{openQuiz}, []model.MoodleItem{openQuiz})
	if len(notes) != 1 || !strings.Contains(notes[0], "Открылся тест в Moodle!") {
		t.Fatalf("при WakeKickKey в Store прямой вызов тоже должен видеть wakeKicked, получили: %v", notes)
	}
}

// Проверка того, что занятый аккаунт не теряет wake_kick (записывается отложенный
// персональный кик "wake") и не теряет существующий moodle_kick:<tg>.
func TestWakeKickPreservedForLockedAccount(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	v, err := vault.Load("", filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatalf("vault.Load: %v", err)
	}
	sec, err := v.Seal("pass123")
	if err != nil {
		t.Fatalf("seal pass: %v", err)
	}

	for _, id := range []int64{101, 102} {
		if err := st.PutAccount(model.Account{
			TelegramID: id,
			Kind:       model.AccountMoodle,
			Status:     model.AccountOK,
			Login:      fmt.Sprintf("student%d", id),
			Secret:     sec,
		}); err != nil {
			t.Fatalf("PutAccount %d: %v", id, err)
		}
	}

	sy := &Syncer{
		Store:      st,
		Vault:      v,
		MoodleBase: "http://127.0.0.1:1",
	}

	// Оба аккаунта заняты; у 102 ещё и был персональный kick от пользователя.
	if !sy.tryLockAccount(101) || !sy.tryLockAccount(102) {
		t.Fatal("не удалось заблокировать аккаунты для теста")
	}
	if err := Kick(st, 102); err != nil {
		t.Fatalf("Kick 102: %v", err)
	}

	// Тик без wake_kick: персональный kick для занятого 102 не должен потеряться.
	sy.moodleTick(context.Background())
	if got, ok, _ := st.GetSetting(KickKey(102)); !ok || got != "1" {
		t.Fatalf("KickKey(102) = (%q, %v); want ('1', true)", got, ok)
	}

	// Приходит глобальный wake_kick пока 101 занят: 101 должен получить отложенный "wake".
	if err := WakeKick(st); err != nil {
		t.Fatalf("WakeKick: %v", err)
	}
	sy.moodleTick(context.Background())
	sy.Wait()

	if hasWakeKick(st) {
		t.Fatal("общий WakeKickKey должен быть снят")
	}
	if got, ok, _ := st.GetSetting(KickKey(101)); !ok || got != "wake" {
		t.Fatalf("KickKey(101) = (%q, %v); want ('wake', true)", got, ok)
	}

	// Освобождаем 101 и 102: на следующем тике отложенные кики должны потребиться.
	sy.unlockAccount(101)
	sy.unlockAccount(102)
	sy.moodleTick(context.Background())
	sy.Wait()

	if got, _, _ := st.GetSetting(KickKey(101)); got != "" {
		t.Fatalf("отложенный KickKey(101) должен быть снят после запуска воркера, got %q", got)
	}
	if got, _, _ := st.GetSetting(KickKey(102)); got != "" {
		t.Fatalf("KickKey(102) должен быть снят после запуска воркера, got %q", got)
	}
}

// Проверка отмены ctx во время collectMoodle при ошибке CourseActivities:
// collectMoodle должен вернуть ошибку контекста, а не успешный результат.
func TestCollectMoodleContextCanceledOnCourseError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := http.NewServeMux()
	mux.HandleFunc("/my/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><script>M.cfg = {"sesskey":"TESTKEY123"};</script></head><body></body></html>`))
	})
	mux.HandleFunc("/lib/ajax/service.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("info") {
		case "core_course_get_enrolled_courses_by_timeline_classification":
			_, _ = w.Write([]byte(`[{"data":{"courses":[{"id":101,"fullname":"Архитектура ЭВМ"}]}}]`))
		case "core_calendar_get_action_events_by_timesort":
			_, _ = w.Write([]byte(`[{"data":{"events":[]}}]`))
		default:
			_, _ = w.Write([]byte(`[{"data":{}}]`))
		}
	})
	mux.HandleFunc("/course/view.php", func(w http.ResponseWriter, r *http.Request) {
		// Отменяем контекст в момент обхода курса и ждём обрыва запроса клиентом.
		cancel()
		<-r.Context().Done()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := moodle.New(srv.URL)
	if err != nil {
		t.Fatalf("moodle.New: %v", err)
	}
	sy := &Syncer{MoodleBase: srv.URL}
	prev := []model.MoodleItem{
		{
			CMID:     1001,
			CourseID: 101,
			Course:   "Архитектура ЭВМ",
			Kind:     model.MoodleAssign,
			Title:    "Старое ДЗ",
			Status:   model.MoodleTodo,
		},
	}

	items, err := sy.collectMoodle(ctx, cli, 101, prev, true)
	if err == nil {
		t.Fatalf("ожидалась ошибка отмены контекста, получены items=%v", items)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидалась ошибка context.Canceled, получено: %v", err)
	}
	if items != nil {
		t.Fatalf("при ошибке контекста items должен быть nil, получено: %v", items)
	}
}
