package uni

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

const watchClosedTodo = "Тест сейчас не открыт для попыток"

// watchQuiz — тест в базе наблюдателя: открытие opens, срок due, состояние страницы todo.
func watchQuiz(cmid int64, opens, due time.Time, todo string) model.MoodleItem {
	o, d := opens, due
	return model.MoodleItem{
		CMID: cmid, CourseID: 5, Course: "ТФЯГ", Kind: model.MoodleQuiz, Title: "Тест 2",
		URL:   fmt.Sprintf("https://moodle.test/mod/quiz/view.php?id=%d", cmid),
		Opens: &o, Due: &d, Status: model.MoodleTodo, Todo: todo,
	}
}

func seedMoodle(t *testing.T, st *store.Store, tg int64, items ...model.MoodleItem) {
	t.Helper()
	if err := st.ReplaceMoodleItems(tg, items); err != nil {
		t.Fatal(err)
	}
}

// watchTick делает один проход наблюдателя и ждёт синки, которые тот запустил.
func watchTick(s *Syncer) {
	var wg sync.WaitGroup
	s.quizWatchTick(context.Background(), &wg)
	wg.Wait()
}

func at(h, m, sec int) time.Time {
	return time.Date(2026, 11, 14, h, m, sec, 0, samara)
}

// Стаб синка: считает вызовы, проверяет, что тест перечитывается. Расписание: синк за минуту
// до открытия, затем один синк на каждый слот по 40 с от открытия до 10 минут (но не позже срока).
func TestQuizWatchScheduleTriggersOncePerKey(t *testing.T) {
	t.Setenv("QUIZ_MOODLE_RPS", "100000")
	f := newFake(t)
	s, st, _ := setup(t, f)
	addAccount(t, s, 1, model.AccountMoodle, "admin", "pw")
	seedMoodle(t, st, 1, watchQuiz(900, at(14, 45, 0), at(15, 10, 0), watchClosedTodo))

	var calls int
	var mu sync.Mutex
	s.watchSync = func(ctx context.Context, _ model.Account) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if !rereadSet(ctx)[900] {
			t.Errorf("синк наблюдателя не перечитывает тест 900")
		}
		return nil
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}

	steps := []struct {
		at   time.Time
		want int
	}{
		{at(14, 43, 30), 0}, // до окна за минуту до открытия
		{at(14, 44, 10), 1}, // за минуту до открытия: синк перед открытием
		{at(14, 44, 40), 1}, // то же действие не повторяется
		{at(14, 45, 0), 2},  // открытие: слот 0
		{at(14, 45, 0), 2},  // второй тик в ту же секунду: без повтора
		{at(14, 45, 30), 2}, // слот 0 выполнен, слот 1 ещё не начался
		{at(14, 45, 50), 3}, // слот 1 (+40 с)
		{at(14, 46, 10), 3}, // слот 1 выполнен
		{at(14, 46, 30), 4}, // слот 2 (+80 с)
		{at(14, 47, 5), 5},  // слот 3 (+120 с)
		{at(14, 50, 0), 6},  // слот 7 (+300 с): пропущенные слоты не догоняются
		{at(14, 54, 50), 7}, // слот 14 (+590 с)
		{at(14, 55, 30), 7}, // окно в 10 минут закрыто
	}
	for _, sp := range steps {
		nowFn = func() time.Time { return sp.at }
		watchTick(s)
		if got := count(); got != sp.want {
			t.Fatalf("в %s: синков %d, want %d", sp.at.Format("15:04:05"), got, sp.want)
		}
	}
	s.Wait()
}

// Сбой синка не сжигает слот: повтор на следующем тике. Ошибочные вызовы: №1 (синк перед открытием)
// и №3 (слот 0). Метка выставляется только после успешного синка.
func TestQuizWatchRetriesFailedSyncOnNextTick(t *testing.T) {
	t.Setenv("QUIZ_MOODLE_RPS", "100000")
	f := newFake(t)
	s, st, _ := setup(t, f)
	addAccount(t, s, 1, model.AccountMoodle, "admin", "pw")
	seedMoodle(t, st, 1, watchQuiz(900, at(14, 45, 0), at(15, 10, 0), watchClosedTodo))
	var calls int
	s.watchSync = func(_ context.Context, _ model.Account) error {
		calls++
		if calls == 1 || calls == 3 {
			return errors.New("429 Too Many Requests")
		}
		return nil
	}
	steps := []struct {
		at   time.Time
		want int
	}{
		{at(14, 44, 10), 1}, // синк перед открытием падает
		{at(14, 44, 30), 2}, // повтор на следующем тике — успех
		{at(14, 45, 0), 3},  // слот 0 падает
		{at(14, 45, 20), 4}, // повтор в том же слоте — успех
		{at(14, 45, 30), 4}, // слот 0 выполнен
	}
	for _, sp := range steps {
		nowFn = func() time.Time { return sp.at }
		watchTick(s)
		if calls != sp.want {
			t.Fatalf("в %s: синков %d, want %d", sp.at.Format("15:04:05"), calls, sp.want)
		}
	}
	s.Wait()
}

// Moodle показал тест открытым на второй проверке: дальше синков наблюдателя нет.
func TestQuizWatchStopsOnceMoodleShowsOpen(t *testing.T) {
	t.Setenv("QUIZ_MOODLE_RPS", "100000")
	f := newFake(t)
	s, st, _ := setup(t, f)
	addAccount(t, s, 1, model.AccountMoodle, "admin", "pw")
	seedMoodle(t, st, 1, watchQuiz(900, at(14, 45, 0), at(15, 10, 0), watchClosedTodo))

	openAt := at(14, 45, 45)
	var calls int
	s.watchSync = func(_ context.Context, _ model.Account) error {
		calls++
		if !nowFn().Before(openAt) {
			// Так выглядит обычный синк, который прочитал открытый тест.
			seedMoodle(t, st, 1, watchQuiz(900, at(14, 45, 0), at(15, 10, 0), ""))
		}
		return nil
	}
	for _, now := range []time.Time{at(14, 45, 0), at(14, 45, 50), at(14, 47, 5), at(14, 47, 50)} {
		nowFn = func() time.Time { return now }
		watchTick(s)
	}
	s.Wait()
	if calls != 2 {
		t.Fatalf("синков %d, want 2 (проверка закрыта, затем открыта)", calls)
	}
}

// Синк за минуту до открытия пропускается, если аккаунт занят (обычный обход); тест не
// помечается выполненным и уходит на следующий тик.
func TestQuizWatchSkipsBusyAccountAndRetriesLater(t *testing.T) {
	t.Setenv("QUIZ_MOODLE_RPS", "100000")
	f := newFake(t)
	s, st, _ := setup(t, f)
	addAccount(t, s, 1, model.AccountMoodle, "admin", "pw")
	seedMoodle(t, st, 1, watchQuiz(900, at(14, 45, 0), at(15, 10, 0), watchClosedTodo))
	var calls int
	s.watchSync = func(_ context.Context, _ model.Account) error { calls++; return nil }

	if !s.tryLockAccount(1) {
		t.Fatal("аккаунт должен быть свободен")
	}
	nowFn = func() time.Time { return at(14, 44, 10) }
	watchTick(s)
	if calls != 0 {
		t.Fatalf("занятый аккаунт синхронизирован: %d", calls)
	}
	s.unlockAccount(1)
	nowFn = func() time.Time { return at(14, 44, 30) }
	watchTick(s)
	if calls != 1 {
		t.Fatalf("после освобождения синков %d, want 1", calls)
	}
	s.Wait()
}

// Завершённые тесты, тесты без срока и без времени открытия наблюдатель не трогает.
func TestQuizWatchPlanIgnoresFinishedAndIncomplete(t *testing.T) {
	s := &Syncer{} // без хранилища: отбор идёт до обращения к settings
	now := at(14, 50, 0)
	done := watchQuiz(1, at(14, 45, 0), at(15, 10, 0), watchClosedTodo)
	done.Status = model.MoodleDone
	noDue := watchQuiz(2, at(14, 45, 0), at(15, 10, 0), watchClosedTodo)
	noDue.Due = nil
	noOpen := watchQuiz(3, at(14, 45, 0), at(15, 10, 0), watchClosedTodo)
	noOpen.Opens = nil
	past := watchQuiz(4, at(14, 45, 0), at(14, 46, 0), watchClosedTodo) // срок уже прошёл
	assign := watchQuiz(5, at(14, 45, 0), at(15, 10, 0), watchClosedTodo)
	assign.Kind = model.MoodleAssign
	if plan := s.planQuizWatch(1, []model.MoodleItem{done, noDue, noOpen, past, assign}, now); len(plan) != 0 {
		t.Fatalf("план для неподходящих элементов: %+v", plan)
	}
}

// Реальный синк: Moodle сначала показывает тест закрытым, затем открытым. Наблюдатель делает
// синк за минуту до открытия и две проверки; «Открылся тест» уходит ровно один раз.
// Напоминание за 15 минут тоже ровно одно.
func TestQuizWatchRealSyncNotifiesOnceAtOpen(t *testing.T) {
	t.Setenv("QUIZ_MOODLE_RPS", "100000")
	f := newFake(t)
	var open atomic.Bool
	var views atomic.Int32
	f.mu.Lock()
	f.quiz = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		views.Add(1)
		page := quizPageClosed
		if open.Load() {
			page = quizPageOpen
		}
		_, _ = w.Write([]byte(page))
	})
	f.mu.Unlock()
	s, st, sent := setup(t, f)
	addAccount(t, s, 1, model.AccountMoodle, "admin", "pw")
	quiz := watchQuiz(900, at(14, 45, 0), at(15, 10, 0), watchClosedTodo)
	quiz.URL = f.srv.URL + "/moodle/mod/quiz/view.php?id=900" // страница теста — на фейковом Moodle
	seedMoodle(t, st, 1, quiz)
	// Решение не запускаем: тест помечен решённым, пройдёт только детекция и уведомление.
	if err := st.SetSetting("quiz_solved:1:900", "1"); err != nil {
		t.Fatal(err)
	}

	count := func(sub string) int {
		n := 0
		for _, m := range *sent {
			if strings.Contains(m, sub) {
				n++
			}
		}
		return n
	}
	steps := []struct {
		at      time.Time
		openNow bool // Moodle открывает тест к этому тику
	}{
		{at(14, 31, 0), false}, // напоминание за 15 минут
		{at(14, 32, 0), false}, // напоминание не повторяется
		{at(14, 44, 10), false},
		{at(14, 45, 0), false}, // проверка №1 — Moodle ещё закрыт
		{at(14, 45, 50), true}, // проверка №2 — Moodle уже открыт
		{at(14, 47, 5), true},  // проверок больше нет
		{at(14, 49, 0), true},
	}
	for _, sp := range steps {
		open.Store(sp.openNow)
		nowFn = func() time.Time { return sp.at }
		watchTick(s)
	}
	s.Wait()

	if n := count("Открылся тест в Moodle!"); n != 1 {
		t.Fatalf("«Открылся тест» %d раз, want 1: %v", n, *sent)
	}
	if n := count("Через 15 минут откроется тест"); n != 1 {
		t.Fatalf("напоминание %d раз, want 1", n)
	}
	// Синки: за минуту до открытия, затем проверки на 14:45:00 и 14:45:50.
	if n := views.Load(); n != 3 {
		t.Fatalf("запросов страницы теста %d, want 3", n)
	}
}

// Страницы теста на фейковом Moodle: закрыт (нет кнопки старта) и открыт (кнопка есть).
const quizPageClosed = `<div data-region="activity-dates"><div><strong>Открывается:</strong> суббота, 14 ноября 2026, 14:45</div><div><strong>Закрывается:</strong> суббота, 14 ноября 2026, 15:10</div></div><div class="quizinfo"><p>Разрешено попыток: 1</p></div>`

const quizPageOpen = quizPageClosed + `<div class="quizstartbuttondiv"></div>`
