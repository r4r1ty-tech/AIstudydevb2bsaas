package uni

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

// poolQuizCMID — cmid открытого теста в фейковой Moodle (он же из ленты календаря).
const poolQuizCMID = 900

// poolQuizViewHTML — открытый тест без блока результата: fakeQuizViewHTML для синка выглядит решённым.
const poolQuizViewHTML = `<!DOCTYPE html>
<html>
<head><title>Тест 2</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_UNI_TEST"};</script>
<div class="quizinfo"><p>Разрешено попыток: 1</p></div>
<div class="quizstartbuttondiv">
  <form method="post" action="BASE/mod/quiz/startattempt.php">
    <input type="hidden" name="cmid" value="900">
    <input type="hidden" name="sesskey" value="SK_UNI_TEST">
    <input type="hidden" name="_qf__mod_quiz_form_preflight_check_form" value="1">
    <input type="submit" name="submitbutton" value="Начать попытку">
  </form>
</div>
</body>
</html>`

type poolOpts struct {
	pages        int  // страниц в попытке
	failPage     int  // страница (с 0), на которую ИИ отвечает ошибкой; -1 — без ошибок
	gated        bool // ИИ держит ответ до release()
	barrier      int  // первые barrier запросов view.php (сбор синка) ждут друг друга
	expireOnView int  // номер запроса view.php, на который Moodle отвечает «войдите» (протухшая сессия)
}

// poolQuizResumeHTML — тест с незавершённой попыткой: кнопка «Продолжить попытку».
const poolQuizResumeHTML = `<!DOCTYPE html>
<html>
<head><title>Тест 2</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_UNI_TEST"};</script>
<div class="quizinfo"><p>Разрешено попыток: 1</p></div>
<div class="quizstartbuttondiv">
  <a class="btn btn-primary" href="BASE/mod/quiz/attempt.php?attempt=555&cmid=900">Продолжить попытку</a>
</div>
</body>
</html>`

// poolFixture — Moodle из newFake с открытым тестом и ИИ-фейк. Тест в базе лежит «закрытым»,
// поэтому синк видит его открытие и ставит решение в пул, как на боевом пути.
type poolFixture struct {
	t    *testing.T
	f    *fakeUni
	sy   *Syncer
	st   *store.Store
	sent *[]string
	acc  model.Account
	tg   int64
	opts poolOpts

	gate       chan struct{} // ИИ отвечает только после close(gate)
	releaseOne sync.Once
	llmEntered chan struct{} // сигнал: ИИ получил запрос

	llmCalls      atomic.Int32
	attemptStarts atomic.Int32
	finished      atomic.Bool

	mu        sync.Mutex
	submitted []int // thispage сабмитов страниц, в порядке прихода

	viewCalls   atomic.Int32
	arrived     atomic.Int32
	barrierOpen chan struct{}

	failPage atomic.Int32 // страница, на которую ИИ отвечает ошибкой; меняется в тестах
}

// setFailPage меняет страницу, которую ИИ «ломает» (-1 — ИИ отвечает всегда).
func (fx *poolFixture) setFailPage(p int32) { fx.failPage.Store(p) }

func newPoolFixture(t *testing.T, o poolOpts) *poolFixture {
	t.Helper()
	fx := &poolFixture{
		t: t, tg: 1, opts: o,
		gate:        make(chan struct{}),
		llmEntered:  make(chan struct{}, 64),
		barrierOpen: make(chan struct{}),
	}
	fx.failPage.Store(int32(o.failPage))
	if !o.gated {
		fx.release()
	}
	f := newFake(t)
	fx.f = f
	sy, st, sent := setup(t, f)
	fx.sy, fx.st, fx.sent = sy, st, sent

	llmSrv := httptest.NewServer(http.HandlerFunc(fx.serveLLM))
	t.Cleanup(llmSrv.Close)
	// release регистрируем позже сервера: при выходе из теста он выполнится раньше и не даст повиснуть ИИ.
	t.Cleanup(fx.release)
	// Уникальный ключ на тест: состояние ключа (cooldown, семафор) общее для процесса и не должно
	// переходить между тестами.
	sy.Cfg = &config.Config{LLMAPIKey: t.Name(), LLMAPIURL: llmSrv.URL, LLMModel: "fake_model", AdminID: fx.tg}

	base := f.srv.URL + "/moodle"
	mux := http.NewServeMux()
	mux.HandleFunc("/moodle/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		n := fx.viewCalls.Add(1)
		if o.barrier > 0 && n <= int32(o.barrier) {
			if fx.arrived.Add(1) == int32(o.barrier) {
				close(fx.barrierOpen)
			}
			select {
			case <-fx.barrierOpen:
			case <-time.After(5 * time.Second):
			}
		}
		if o.expireOnView > 0 && n == int32(o.expireOnView) {
			// Сессия протухла: Moodle отправляет на страницу входа.
			http.Redirect(w, r, base+"/login/index.php", http.StatusSeeOther)
			return
		}
		page := poolQuizViewHTML
		if fx.attemptStarts.Load() > 0 && !fx.finished.Load() {
			page = poolQuizResumeHTML
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(page, "BASE", base)))
	})
	mux.HandleFunc("/moodle/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		fx.attemptStarts.Add(1)
		http.Redirect(w, r, base+"/mod/quiz/attempt.php?attempt=555&cmid=900&page=0", http.StatusSeeOther)
	})
	mux.HandleFunc("/moodle/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		next := p + 1
		if p == o.pages-1 {
			next = -1
		}
		html := renderAttemptPageHTML(base, poolQuizCMID, 555, p, next, p+1, fmt.Sprintf("Вопрос МаркерСтраницы%d", p+1), "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/moodle/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			_ = r.ParseMultipartForm(1 << 20)
			thisPage, _ := strconv.Atoi(r.FormValue("thispage"))
			fx.mu.Lock()
			fx.submitted = append(fx.submitted, thisPage)
			fx.mu.Unlock()
			if thisPage >= o.pages-1 {
				http.Redirect(w, r, base+"/mod/quiz/summary.php?attempt=555&cmid=900", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("%s/mod/quiz/attempt.php?attempt=555&cmid=900&page=%d", base, thisPage+1), http.StatusSeeOther)
			return
		}
		// Завершение попытки (FinishAttempt).
		fx.finished.Store(true)
		http.Redirect(w, r, base+"/mod/quiz/review.php?attempt=555&cmid=900", http.StatusSeeOther)
	})
	mux.HandleFunc("/moodle/mod/quiz/summary.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeSummaryHTML, "BASE", base)))
	})
	mux.HandleFunc("/moodle/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeReviewHTML, "BASE", base)))
	})
	f.mu.Lock()
	f.quiz = mux
	f.mu.Unlock()

	addAccount(t, sy, fx.tg, model.AccountMoodle, "student", "pw")
	acc, err := st.GetAccount(fx.tg, model.AccountMoodle)
	if err != nil || acc == nil {
		t.Fatalf("get account: %v", err)
	}
	fx.acc = *acc
	// Тест в базе «закрыт»: синк увидит, что он открылся.
	if err := st.ReplaceMoodleItems(fx.tg, []model.MoodleItem{{
		CMID: poolQuizCMID, CourseID: 5, Course: "ТФЯГ", Kind: model.MoodleQuiz,
		Title: "Тест 2", Status: model.MoodleTodo, Todo: "Тест сейчас не открыт для попыток",
	}}); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	return fx
}

// release открывает ИИ (один раз).
func (fx *poolFixture) release() {
	fx.releaseOne.Do(func() { close(fx.gate) })
}

func (fx *poolFixture) serveLLM(w http.ResponseWriter, r *http.Request) {
	fx.llmCalls.Add(1)
	select {
	case fx.llmEntered <- struct{}{}:
	default:
	}
	body, _ := io.ReadAll(r.Body)
	select {
	case <-fx.gate:
	case <-r.Context().Done():
		return
	}
	page := -1
	for p := 0; p < fx.opts.pages; p++ {
		if strings.Contains(string(body), fmt.Sprintf("МаркерСтраницы%d", p+1)) {
			page = p
			break
		}
	}
	if page < 0 {
		http.Error(w, "нет маркера страницы в промпте", http.StatusBadRequest)
		return
	}
	if page == int(fx.failPage.Load()) {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	content, _ := json.Marshal(map[string]any{"answers": []map[string]any{{"slot": page + 1, "text": "ans"}}})
	resp, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(content)}}}})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}

// waitLLM ждёт, пока ИИ получит запрос: значит, решение теста реально запущено.
func (fx *poolFixture) waitLLM() {
	fx.t.Helper()
	select {
	case <-fx.llmEntered:
	case <-time.After(5 * time.Second):
		fx.t.Fatal("ИИ не получил запрос: решение теста не запустилось")
	}
}

// checkSolvedAll дожидается фонового решения и проверяет полный успешный путь:
// все страницы отправлены по порядку, попытка завершена, тест помечен решённым,
// аккаунту поставлен пинок на синк.
func (fx *poolFixture) checkSolvedAll() {
	fx.t.Helper()
	fx.sy.Wait()
	want := make([]int, fx.opts.pages)
	for i := range want {
		want[i] = i
	}
	fx.mu.Lock()
	got := append([]int(nil), fx.submitted...)
	fx.mu.Unlock()
	if !reflect.DeepEqual(got, want) {
		fx.t.Fatalf("отправлены страницы %v, want %v", got, want)
	}
	if n := fx.attemptStarts.Load(); n != 1 {
		fx.t.Fatalf("попыток начато %d, want 1", n)
	}
	if !fx.finished.Load() {
		fx.t.Fatal("FinishAttempt не вызван при полном решении")
	}
	if v, ok, _ := fx.st.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", fx.tg, poolQuizCMID)); !ok || v == "" {
		fx.t.Fatal("тест не отмечен решённым")
	}
	if v, ok, _ := fx.st.GetSetting(KickKey(fx.tg)); !ok || v != "1" {
		fx.t.Fatalf("после решения аккаунту не поставлен пинок на синк: %q, ok=%v", v, ok)
	}
	if !strings.Contains(strings.Join(*fx.sent, "\n"), "автоматически решён") {
		fx.t.Fatalf("нет отчёта о решении: %v", *fx.sent)
	}
}

// withTestMoodleSession кладёт в аккаунт зашифрованную сессию Moodle. Фейковые серверы тестов
// входа не отдают, а решение теста в фоне (solveQuiz) сначала входит по сохранённой сессии.
func withTestMoodleSession(t *testing.T, sy *Syncer, acc *model.Account) {
	t.Helper()
	if acc == nil {
		t.Fatal("withTestMoodleSession: аккаунт не найден")
	}
	sealed, err := sy.Vault.Seal(`[{"n":"MoodleSession","v":"ok"}]`)
	if err != nil {
		t.Fatalf("seal session: %v", err)
	}
	acc.Session = sealed
}

// poolWaitUntil ждёт выполнения условия, иначе падает по таймауту.
func poolWaitUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Синк возвращается сразу, а тест решается в фоне: ИИ держит ответ, SyncMoodle уже вышел.
func TestSyncMoodleSchedulesQuizInPool(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: -1, gated: true})
	done := make(chan error, 1)
	go func() { done <- fx.sy.SyncMoodle(context.Background(), fx.acc, false) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SyncMoodle: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SyncMoodle не вернулся, пока ИИ держит ответ: решение теста идёт прямо в синке")
	}
	fx.waitLLM()
	if v, ok, _ := fx.st.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", fx.tg, poolQuizCMID)); ok && v != "" {
		t.Fatal("тест решён до ответа ИИ")
	}
	fx.release()
	fx.checkSolvedAll()
}

// Слот аккаунта освобождается, пока решение теста ещё идёт (обход через moodleTick).
func TestQuizPoolFreesAccountSlotBeforeSolveEnds(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: -1, gated: true})
	fx.sy.moodleTick(context.Background())
	fx.waitLLM()
	poolWaitUntil(t, 10*time.Second, "слот аккаунта освободился", func() bool {
		if !fx.sy.tryLockAccount(fx.tg) {
			return false
		}
		fx.sy.unlockAccount(fx.tg)
		return true
	})
	if n := len(fx.sy.getSem()); n != 0 {
		t.Fatalf("слот обхода Moodle занят после синка: %d", n)
	}
	if fx.finished.Load() {
		t.Fatal("попытка завершена, пока ИИ держит ответ")
	}
	fx.release()
	fx.checkSolvedAll()
}

// Два одновременных SyncMoodle одного аккаунта не запускают решение одного теста дважды.
// Барьер на запросах к теcту держит оба синка до сбора прежнего состояния (тест «закрыт» в обоих),
// так что оба дойдут до постановки в пул.
func TestConcurrentSyncMoodleSolvesQuizOnce(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: -1, gated: true, barrier: 2})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- fx.sy.SyncMoodle(context.Background(), fx.acc, false)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SyncMoodle: %v", err)
		}
	}
	fx.waitLLM()
	fx.release()
	fx.checkSolvedAll()
	if n := fx.llmCalls.Load(); n != 1 {
		t.Fatalf("запросов к ИИ %d, want 1: тест решён дважды", n)
	}
}

// Ошибка одной страницы не блокирует отправку остальных: страница 2 уходит в Moodle,
// попытка не завершается, тест не помечается решённым и получает повтор.
func TestQuizFailedPageIsSkippedOthersSubmitted(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 3, failPage: 1, gated: false})
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	fx.sy.Wait()

	fx.mu.Lock()
	got := append([]int(nil), fx.submitted...)
	fx.mu.Unlock()
	if !reflect.DeepEqual(got, []int{0, 2}) {
		t.Fatalf("отправлены страницы %v, want [0 2]: страница 2 не должна зависеть от ошибки страницы 1", got)
	}
	if fx.finished.Load() {
		t.Fatal("попытка завершена при пропущенной странице")
	}
	if _, ok, _ := fx.st.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", fx.tg, poolQuizCMID)); ok {
		t.Fatal("тест помечен решённым при пропущенной странице")
	}
	if v, ok, _ := fx.st.GetSetting(quizFailKey(fx.tg, poolQuizCMID)); !ok || v != "1" {
		t.Fatalf("quiz_fail = %q (ok=%v), want 1: тест должен повториться", v, ok)
	}
	if !strings.Contains(strings.Join(*fx.sent, "\n"), "Проблема с тестом") {
		t.Fatalf("нет сообщения о проблеме с тестом: %v", *fx.sent)
	}
}

func TestQuizWorkersLimitEnv(t *testing.T) {
	cases := map[string]int{"": 4, "3": 3, " 2 ": 2, "0": 4, "-1": 4, "abc": 4}
	for v, want := range cases {
		t.Setenv("MOODLE_QUIZ_WORKERS", v)
		if got := QuizWorkersLimit(); got != want {
			t.Errorf("MOODLE_QUIZ_WORKERS=%q: got %d, want %d", v, got, want)
		}
	}
}

func TestQuizParallelismBounds(t *testing.T) {
	if got := quizParallelism(nil, 1); got != 1 {
		t.Errorf("одна страница: %d, want 1", got)
	}
	// Без распознанных ключей считаем один ключ: не больше llm.MaxConcurrentPerKey одновременно.
	if got := quizParallelism(nil, 10); got != min(quizPageWorkers, llm.MaxConcurrentPerKey) {
		t.Errorf("один ключ, 10 страниц: %d, want %d", got, min(quizPageWorkers, llm.MaxConcurrentPerKey))
	}
}

// «Пройти» при занятой блокировке теста не молчит: пользователь получает уведомление, флаг снят.
func TestForcedQuizWhileSolvingNotifies(t *testing.T) {
	f := newFake(t)
	sy, st, sent := setup(t, f)
	const tg, cmid = int64(7), int64(poolQuizCMID)
	acc := model.Account{TelegramID: tg, Kind: model.AccountMoodle}
	item := model.MoodleItem{CMID: cmid, Kind: model.MoodleQuiz, Course: "ТФЯГ", Title: "Тест 2", Status: model.MoodleTodo}

	if !sy.tryLockQuiz(tg, "900") {
		t.Fatal("блокировка не взята")
	}
	defer sy.unlockQuiz(tg, "900")
	if err := st.SetSetting(QuizForceKey(tg, cmid), "1"); err != nil {
		t.Fatal(err)
	}
	cli, err := sy.moodleClient()
	if err != nil {
		t.Fatal(err)
	}
	sy.detectAndHandleOpenedQuizzes(context.Background(), acc, cli, []model.MoodleItem{item}, []model.MoodleItem{item})
	sy.Wait()

	if !strings.Contains(strings.Join(*sent, "\n"), "Тест уже решается или стоит в очереди") {
		t.Fatalf("«Пройти» при занятой блокировке должно давать уведомление: %v", *sent)
	}
	if v, _, _ := st.GetSetting(QuizForceKey(tg, cmid)); v != "" {
		t.Fatal("флаг «Пройти» не снят")
	}
}

// Остановка в очереди (ctx отменён до старта) — не неудача решения: счётчик не растёт,
// но повтор на следующем синке остаётся (quiz_fail выставлен).
func TestQuizCancelledInQueueKeepsRetry(t *testing.T) {
	f := newFake(t)
	sy, st, _ := setup(t, f)
	sy.QuizWorkers = 1
	sem := sy.getQuizSem()
	sem <- struct{}{} // пул занят: решение встанет в очередь
	const tg, cmid = int64(8), int64(901)
	acc := model.Account{TelegramID: tg, Kind: model.AccountMoodle}
	item := model.MoodleItem{CMID: cmid, Kind: model.MoodleQuiz, Title: "Тест", Status: model.MoodleTodo}
	cli, err := sy.moodleClient()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if !sy.scheduleQuiz(ctx, acc, cli, item, false) {
		t.Fatal("в очередь не поставлено")
	}
	cancel()
	sy.Wait()
	<-sem

	v, ok, _ := st.GetSetting(quizFailKey(tg, cmid))
	if !ok || v != "0" {
		t.Fatalf("quiz_fail = %q (ok=%v), want \"0\": повтор на следующем синке", v, ok)
	}
	if n := sy.quizFailCount(tg, cmid); n != 0 {
		t.Fatalf("остановка засчитана как неудача решения: счётчик %d", n)
	}
}

// Повтор после пропущенной страницы продолжает ту же попытку: startattempt один раз за два прохода,
// страницы решаются заново и попытка завершается.
func TestQuizRetryResumesSameAttempt(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 3, failPage: 1})
	ctx := context.Background()
	if err := fx.sy.SyncMoodle(ctx, fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle (проход 1): %v", err)
	}
	fx.sy.Wait()
	if fx.finished.Load() {
		t.Fatal("проход 1: попытка завершена при пропущенной странице")
	}

	fx.setFailPage(-1)
	fx.mu.Lock()
	fx.submitted = nil
	fx.mu.Unlock()
	if err := fx.sy.SyncMoodle(ctx, fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle (проход 2): %v", err)
	}
	fx.checkSolvedAll() // страницы 0..2, одна попытка, FinishAttempt, решено, пинок
}

// После quizMaxAttempts неудач подряд авто-постановки нет; ручной «Пройти» решает тест и сбрасывает счётчик.
func TestQuizSixFailuresStopAutoAndForcedResets(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: 0})
	ctx := context.Background()
	for i := 0; i < quizMaxAttempts; i++ {
		if err := fx.sy.SyncMoodle(ctx, fx.acc, false); err != nil {
			t.Fatalf("SyncMoodle (проход %d): %v", i+1, err)
		}
		fx.sy.Wait()
	}
	if n := fx.llmCalls.Load(); n != quizMaxAttempts {
		t.Fatalf("ИИ вызван %d раз за %d неудачных проходов", n, quizMaxAttempts)
	}
	if n := fx.sy.quizFailCount(fx.tg, poolQuizCMID); n != quizMaxAttempts {
		t.Fatalf("счётчик неудач %d, want %d", n, quizMaxAttempts)
	}
	if !strings.Contains(strings.Join(*fx.sent, "\n"), "Не смог решить тест за 6 попыток") {
		t.Fatalf("нет сообщения о лимите попыток: %v", *fx.sent)
	}

	// Седьмой синк: авто-постановки нет, ИИ не зовётся.
	if err := fx.sy.SyncMoodle(ctx, fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle (после лимита): %v", err)
	}
	fx.sy.Wait()
	if n := fx.llmCalls.Load(); n != quizMaxAttempts {
		t.Fatalf("авто-решение после лимита: ИИ вызван %d раз, want %d", n, quizMaxAttempts)
	}

	// «Пройти» сбрасывает счётчик, ИИ отвечает — тест решён.
	fx.setFailPage(-1)
	if err := fx.st.SetSetting(QuizForceKey(fx.tg, poolQuizCMID), "1"); err != nil {
		t.Fatal(err)
	}
	if err := fx.sy.SyncMoodle(ctx, fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle (Пройти): %v", err)
	}
	fx.checkSolvedAll()
	if n := fx.sy.quizFailCount(fx.tg, poolQuizCMID); n != 0 {
		t.Fatalf("после решения счётчик неудач не сброшен: %d", n)
	}
}

// Протухшая сессия посреди решения: решение перелогинивается и доходит до конца.
// Вход 1 — синк, вход 2 — перелогин решения.
func TestQuizUnauthorizedMidSolveRelogins(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: -1, expireOnView: 2})
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	fx.checkSolvedAll()
	if n := fx.f.mdLogins.Load(); n != 2 {
		t.Fatalf("входов в Moodle %d, want 2 (синк и перелогин решения)", n)
	}
}

// Паника решения одной страницы (горутина страницы) не роняет процесс: остальные страницы отправлены,
// попытка не завершена, счётчик неудач вырос.
func TestQuizPagePanicIsContained(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 3, failPage: -1})
	fx.sy.solveHook = func(idx int) {
		if idx == 1 {
			panic("тестовая паника страницы")
		}
	}
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	fx.sy.Wait()

	fx.mu.Lock()
	got := append([]int(nil), fx.submitted...)
	fx.mu.Unlock()
	if !reflect.DeepEqual(got, []int{0, 2}) {
		t.Fatalf("отправлены страницы %v, want [0 2]: паника страницы 1 не должна останавливать остальные", got)
	}
	if fx.finished.Load() {
		t.Fatal("попытка завершена при панике страницы")
	}
	if n := fx.sy.quizFailCount(fx.tg, poolQuizCMID); n != 1 {
		t.Fatalf("счётчик неудач %d, want 1", n)
	}
	if !strings.Contains(strings.Join(*fx.sent, "\n"), "Проблема с тестом") {
		t.Fatalf("нет сообщения о проблеме с тестом: %v", *fx.sent)
	}
}

// Паника в горутине решения теста (перехват в scheduleQuiz): блокировка снята, счётчик вырос.
// Паника возникает в уведомлении о неудаче, уже после первой отметки счётчика (1), перехват добавляет вторую (2).
func TestQuizPanicInSolverGoroutineIsContained(t *testing.T) {
	fx := newPoolFixture(t, poolOpts{pages: 1, failPage: 0})
	orig := fx.sy.Notify
	fx.sy.Notify = func(ctx context.Context, id int64, text string) {
		if strings.Contains(text, "Проблема с тестом") {
			panic("тестовая паника уведомления")
		}
		orig(ctx, id, text)
	}
	if err := fx.sy.SyncMoodle(context.Background(), fx.acc, false); err != nil {
		t.Fatalf("SyncMoodle: %v", err)
	}
	fx.sy.Wait()

	if !fx.sy.tryLockQuiz(fx.tg, "900") {
		t.Fatal("блокировка теста не снята после паники")
	}
	fx.sy.unlockQuiz(fx.tg, "900")
	if n := fx.sy.quizFailCount(fx.tg, poolQuizCMID); n != 2 {
		t.Fatalf("счётчик неудач %d, want 2 (отметка notify-неудачи и перехват паники)", n)
	}
	if fx.finished.Load() {
		t.Fatal("попытка завершена после паники")
	}
}
