package uni

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

// retryMoodle — фейковый Moodle одной попытки: правила (попытки, метод), число начатых и
// завершённых попыток, оценка «N из M» на странице просмотра. Вопросы — из fakeAttemptHTML.
type retryMoodle struct {
	srv        *httptest.Server
	allowed    string // "0" — без ограничений
	method     string
	grade      string
	inProgress bool // студент уже открыл попытку 555 сам
	showRight  bool // на странице просмотра есть блок «Правильный ответ» у неверного вопроса
	mu         sync.Mutex
	started    int
	finished   int
}

// rightBlock — блок «Правильный ответ» для неверного вопроса (если Moodle его показывает).
func (f *retryMoodle) rightBlock(full bool) string {
	if !f.showRight || full {
		return ""
	}
	return `<div class="outcome"><div class="feedback"><div class="rightanswer">Правильный ответ: Нулю</div></div></div>`
}

// Страница просмотра: вопрос 1 — неверный (класс incorrect при неполной оценке), вопрос 2 — верный.
func (f *retryMoodle) reviewHTML() string {
	full := false
	if n, d, ok := moodle.GradeFraction(f.grade); ok && n >= d {
		full = true
	}
	mark1, state1, grade1 := "incorrect", "Неверно", "Баллов: 0,00 из 1,00"
	if full {
		mark1, state1, grade1 = "correct", "Верно", "Баллов: 1,00 из 1,00"
	}
	return `<!DOCTYPE html><html><head><title>Просмотр</title></head><body>
<table class="generaltable quizreviewsummary"><tbody>
  <tr><th>Оценка</th><td>` + f.grade + `</td></tr>
</tbody></table>
<div class="que multichoice deferredfeedback ` + mark1 + `" id="question-101-1">
  <div class="info"><div class="state">` + state1 + `</div><div class="grade">` + grade1 + `</div></div>
  ` + f.rightBlock(full) + `
  <div class="content">
    <div class="qtext"><p>Чему равна энтропия равновероятных событий?</p></div>
    <div class="answer">
      <div class="r0"><input type="radio" name="q101:1_answer" value="0" checked disabled><label><span data-region="answer-label">Максимуму</span></label></div>
      <div class="r1"><input type="radio" name="q101:1_answer" value="1" disabled><label><span data-region="answer-label">Нулю</span></label></div>
    </div>
  </div>
</div>
<div class="que shortanswer deferredfeedback correct" id="question-102-2">
  <div class="info"><div class="state">Верно</div></div>
  <div class="content">
    <div class="qtext"><p>Назовите единицу измерения информации:</p></div>
    <div class="answer"><input type="text" name="q102:2_answer" value="бит" readonly></div>
  </div>
</div>
</body></html>`
}

func newRetryMoodle(t *testing.T, allowed, method, grade string) *retryMoodle {
	t.Helper()
	f := &retryMoodle{allowed: allowed, method: method, grade: grade}
	mux := http.NewServeMux()
	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		allowedLine := "Разрешено попыток: " + f.allowed
		limit, _ := strconv.Atoi(f.allowed)
		if f.allowed == "0" {
			allowedLine = "Разрешено попыток: Без ограничений"
		}
		var rows strings.Builder
		for i := 1; i <= f.finished; i++ {
			_, _ = fmt.Fprintf(&rows, `<tr><td class="cell c0">%d</td><td class="cell c1 state">Завершены</td><td class="cell c2 grade">%s</td><td class="cell c3"><a href="%s/mod/quiz/review.php?attempt=555&amp;cmid=777">Просмотр</a></td></tr>`, i, f.grade, f.srv.URL)
		}
		if f.inProgress {
			_, _ = fmt.Fprintf(&rows, `<tr><td class="cell c0">%d</td><td class="cell c1 state">В процессе</td><td class="cell c2 grade"></td><td class="cell c3"><a href="%s/mod/quiz/attempt.php?attempt=555&amp;cmid=777">Продолжить</a></td></tr>`, f.finished+1, f.srv.URL)
		}
		start := ""
		if f.allowed == "0" || f.finished < limit {
			start = `<div class="quizstartbuttondiv"><form method="post" action="` + f.srv.URL + `/mod/quiz/startattempt.php">
<input type="hidden" name="cmid" value="777"><input type="hidden" name="sesskey" value="SK_UNI_TEST">
<input type="hidden" name="_qf__mod_quiz_form_preflight_check_form" value="1">
<input type="submit" name="submitbutton" value="Начать попытку"></form></div>`
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><body><script>M.cfg = {"sesskey":"SK_UNI_TEST"};</script>
<div class="quizinfo"><p>%s</p><p>Метод оценивания: %s</p></div>%s
<table class="generaltable quizreviewsummary"><tbody>%s</tbody></table></body></html>`, allowedLine, f.method, start, rows.String())
	})
	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.started++
		f.mu.Unlock()
		http.Redirect(w, r, f.srv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeAttemptHTML, "BASE", f.srv.URL)))
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			http.Redirect(w, r, f.srv.URL+"/mod/quiz/summary.php?attempt=555&cmid=777", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()
		if r.FormValue("finishattempt") == "1" {
			f.mu.Lock()
			f.finished++
			f.mu.Unlock()
			http.Redirect(w, r, f.srv.URL+"/mod/quiz/review.php?attempt=555&cmid=777", http.StatusSeeOther)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
	})
	mux.HandleFunc("/mod/quiz/summary.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeSummaryHTML, "BASE", f.srv.URL)))
	})
	mux.HandleFunc("/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(f.reviewHTML()))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *retryMoodle) counts() (started, finished int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started, f.finished
}

// retryScenario — Syncer с ИИ (или без него), фейковый Moodle и тестовый элемент теста.
type retryScenario struct {
	sy   *Syncer
	st   *store.Store
	sent *[]string
	m    *retryMoodle
	ai   *fakeAI
	acc  model.Account
	it   model.MoodleItem
}

func newRetryScenario(t *testing.T, m *retryMoodle, withKey bool) *retryScenario {
	t.Helper()
	f := newFake(t)
	sy, st, sent := setup(t, f)
	sy.MoodleBase = m.srv.URL
	ai := newFakeAISeq(t, `{"answers":[{"slot":1,"choice":"0"},{"slot":2,"text":"бит"}]}`)
	cfg := &config.Config{AdminID: 10}
	if withKey {
		cfg = &config.Config{LLMAPIKey: "fake_key", LLMAPIURL: ai.srv.URL, LLMModel: "fake_model", AdminID: 10}
	}
	sy.Cfg = cfg
	if !withKey && len(access.QuizChainWithVault(cfg, st, nil, 10)) != 0 {
		t.Fatal("без ключа цепочка ИИ должна быть пуста")
	}
	return &retryScenario{
		sy: sy, st: st, sent: sent, m: m, ai: ai,
		acc: model.Account{TelegramID: 10, Kind: model.AccountMoodle},
		it: model.MoodleItem{
			CMID: 777, Course: "Теория информации (01.03.02)", Title: "Тест: Сжатие LZ77",
			URL: m.srv.URL + "/mod/quiz/view.php?id=777", Kind: model.MoodleQuiz,
		},
	}
}

func (sc *retryScenario) run(t *testing.T) bool {
	t.Helper()
	return sc.runForced(t, false)
}

func (sc *retryScenario) runForced(t *testing.T, forced bool) bool {
	t.Helper()
	cli, err := sc.sy.moodleClient()
	if err != nil {
		t.Fatalf("moodleClient: %v", err)
	}
	solved, err := sc.sy.processQuizErr(context.Background(), sc.acc, cli, sc.it, forced)
	if err != nil {
		t.Fatalf("processQuizErr: %v", err)
	}
	sc.sy.Wait()
	return solved
}

func (sc *retryScenario) solvedMark(t *testing.T) bool {
	t.Helper()
	_, ok, err := sc.st.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", sc.acc.TelegramID, sc.it.CMID))
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func (sc *retryScenario) reports() string {
	return strings.Join(*sc.sent, "\n---\n")
}

// Высшая оценка, попытки есть, оценка 0 — повтор до исчерпания попыток.
func TestPartialGradeRetriesWhileHighestMethodAttemptsLeft(t *testing.T) {
	m := newRetryMoodle(t, "2", "Высшая оценка", "0,00 из 2,00")
	sc := newRetryScenario(t, m, true)
	if solved := sc.run(t); solved {
		t.Fatal("неполная оценка не должна считаться решённой")
	}
	started, finished := m.counts()
	if started != 2 || finished != 2 {
		t.Fatalf("начато=%d завершено=%d, ожидали 2 и 2 (автоповтор до исчерпания)", started, finished)
	}
	if sc.solvedMark(t) {
		t.Error("тест помечен решённым при неполной оценке")
	}
	if v, _, _ := sc.st.GetSetting(quizFailKey(10, 777)); v != "" {
		t.Errorf("счётчик неудач = %q, ожидали сброс после завершённых попыток", v)
	}
	rep := sc.reports()
	if !strings.Contains(rep, "повтор будет автоматически") {
		t.Errorf("в первом отчёте нет «повтор будет автоматически»:\n%s", rep)
	}
	if !strings.Contains(rep, "попыток не осталось") {
		t.Errorf("в последнем отчёте нет «попыток не осталось»:\n%s", rep)
	}
	if !strings.Contains(rep, "Тест Moodle: оценка 0,00 из 2,00") {
		t.Errorf("нет заголовка неполной оценки:\n%s", rep)
	}
	// Неверный ответ первой попытки запомнен и передан модели во второй.
	if w, _ := sc.st.ListWrongAnswers(quizQuestionHash(moodle.QuizQuestion{QType: "multichoice",
		Text:    "Чему равна энтропия равновероятных событий?",
		Choices: []moodle.QuizChoice{{Text: "Максимуму"}, {Text: "Нулю"}}})); len(w) != 1 {
		t.Errorf("неверных ответов в памяти = %d, ожидали 1", len(w))
	}
	if !strings.Contains(sc.ai.prompts(), "УЖЕ ПРОВЕРЕНО") {
		t.Error("во втором решении модели не передан неверный ответ первой попытки")
	}
}

// Последняя попытка с ненулевой оценкой — повтора нет, в отчёте «Пройти» и остаток попыток.
func TestPartialGradeLastAttemptMethodNoRetry(t *testing.T) {
	m := newRetryMoodle(t, "3", "Последняя попытка", "1,00 из 2,00")
	sc := newRetryScenario(t, m, true)
	if solved := sc.run(t); solved {
		t.Fatal("неполная оценка не должна считаться решённой")
	}
	started, finished := m.counts()
	if started != 1 || finished != 1 {
		t.Fatalf("начато=%d завершено=%d, ожидали 1 и 1 (без повтора)", started, finished)
	}
	if sc.solvedMark(t) {
		t.Error("тест помечен решённым при неполной оценке")
	}
	rep := sc.reports()
	if !strings.Contains(rep, "нажми „Пройти“, осталось попыток 2") {
		t.Errorf("нет подсказки про «Пройти» и остаток попыток:\n%s", rep)
	}
	if !strings.Contains(rep, "Последняя попытка") || !strings.Contains(rep, "повтор не запускаю") {
		t.Errorf("нет причины, почему не повторяем:\n%s", rep)
	}
	if v, _, _ := sc.st.GetSetting(quizFailKey(10, 777)); v != "" {
		t.Errorf("счётчик неудач = %q: без повтора он должен быть пуст, иначе синк запустит тест сам", v)
	}
}

// Оценка 0 повторяется даже при методе «Последняя попытка», но только при новом знании:
// на второй попытке те же ответы уже в базе, нового нет — третьей не начинаем.
func TestZeroGradeRetriesEvenOnLastAttemptMethod(t *testing.T) {
	m := newRetryMoodle(t, "3", "Последняя попытка", "0,00 из 2,00")
	sc := newRetryScenario(t, m, true)
	sc.run(t)
	if started, _ := m.counts(); started != 2 {
		t.Fatalf("начато=%d, ожидали 2 (повтор на второй попытке не нужен: нового знания нет)", started)
	}
}

// Правильный ответ из блока «Правильный ответ» попадает в базу: повтор улучшает оценку.
func TestRightAnswerBlockFeedsBase(t *testing.T) {
	m := newRetryMoodle(t, "2", "Высшая оценка", "1,00 из 2,00")
	m.showRight = true
	sc := newRetryScenario(t, m, true)
	sc.run(t)
	page, err := mustClient(t, sc).GetAttemptPage(context.Background(), 777, 555, 0)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := sc.st.PeekAnswer(quizQuestionHash(page.Questions[0]))
	if err != nil || !ok {
		t.Fatalf("правильный ответ по неверному вопросу не записан: ok=%t err=%v", ok, err)
	}
	if !strings.Contains(rec.AnswerJSON, `"choice":"Нулю"`) {
		t.Errorf("answer_json = %s, ожидали «Нулю»", rec.AnswerJSON)
	}
}

func mustClient(t *testing.T, sc *retryScenario) *moodle.Client {
	t.Helper()
	cli, err := sc.sy.moodleClient()
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

// Полная оценка — тест решён, отметка и отчёт «Тест решён: 2,00 из 2,00».
func TestFullGradeMarksSolved(t *testing.T) {
	m := newRetryMoodle(t, "2", "Высшая оценка", "2,00 из 2,00")
	sc := newRetryScenario(t, m, true)
	if solved := sc.run(t); !solved {
		t.Fatal("полная оценка должна давать решённый тест")
	}
	if started, _ := m.counts(); started != 1 {
		t.Errorf("начато=%d, ожидали 1 (повтора при полной оценке нет)", started)
	}
	if !sc.solvedMark(t) {
		t.Error("отметка решённого теста не записана")
	}
	if rep := sc.reports(); !strings.Contains(rep, "✅ Тест решён: 2,00 из 2,00") {
		t.Errorf("нет отчёта о решённом тесте:\n%s", rep)
	}
}

// Без ключа ИИ, но все вопросы в базе — решение из базы, ИИ не вызывается.
func TestNoKeySolvesFromBaseWhenAllKnown(t *testing.T) {
	m := newRetryMoodle(t, "0", "Высшая оценка", "2,00 из 2,00")
	sc := newRetryScenario(t, m, false)
	seedBase(t, sc, map[int]string{0: `{"choice":"Максимуму"}`, 1: `{"text":"бит"}`})
	if solved := sc.runForced(t, true); !solved {
		t.Fatal("все вопросы в базе и «Пройти» — тест должен решиться без ключа ИИ")
	}
	if n := sc.ai.calls.Load(); n != 0 {
		t.Errorf("ИИ вызван %d раз, ожидали 0", n)
	}
	if rep := sc.reports(); !strings.Contains(rep, "нет ключа ИИ, все ответы из базы") {
		t.Errorf("в отчёте нет пометки про базу без ключа:\n%s", rep)
	}
}

// Без ключа и без открытой попытки: новую не начинаем, одно сообщение «нужен ключ ИИ».
func TestNoKeyNoOpenAttemptNoStart(t *testing.T) {
	m := newRetryMoodle(t, "0", "Высшая оценка", "2,00 из 2,00")
	sc := newRetryScenario(t, m, false)
	if solved := sc.run(t); solved {
		t.Fatal("без ключа и без попытки тест не решается")
	}
	if started, finished := m.counts(); started != 0 || finished != 0 {
		t.Errorf("начато=%d завершено=%d, ожидали 0 и 0", started, finished)
	}
	if rep := sc.reports(); !strings.Contains(strings.ToLower(rep), "нужен ключ ии") {
		t.Errorf("нет сообщения про ключ ИИ:\n%s", rep)
	}
}

// Без ключа, студент открыл попытку сам, все вопросы в базе — решаем из неё, попытку завершаем.
func TestNoKeyOpenAttemptSolvesFromBase(t *testing.T) {
	m := newRetryMoodle(t, "0", "Высшая оценка", "2,00 из 2,00")
	m.inProgress = true
	sc := newRetryScenario(t, m, false)
	seedBase(t, sc, map[int]string{0: `{"choice":"Максимуму"}`, 1: `{"text":"бит"}`})
	if solved := sc.run(t); !solved {
		t.Fatal("открытая попытка и полная база — тест решается из базы")
	}
	if started, finished := m.counts(); started != 0 || finished != 1 {
		t.Errorf("начато=%d завершено=%d, ожидали 0 и 1 (продолжаем открытую)", started, finished)
	}
}

// Без ключа, попытка открыта студентом, базы не хватает: текст вопросов, попытку не трогаем.
func TestNoKeyOpenAttemptNotCoveredSendsText(t *testing.T) {
	m := newRetryMoodle(t, "0", "Высшая оценка", "2,00 из 2,00")
	m.inProgress = true
	sc := newRetryScenario(t, m, false)
	if solved := sc.run(t); solved {
		t.Fatal("база пуста — тест не решается без ключа")
	}
	if started, finished := m.counts(); started != 0 || finished != 0 {
		t.Errorf("начато=%d завершено=%d, попытку трогать нельзя", started, finished)
	}
	rep := sc.reports()
	if !strings.Contains(rep, "Открыт тест") || !strings.Contains(rep, "Чему равна энтропия") {
		t.Errorf("нет текста вопросов:\n%s", rep)
	}
	if strings.Contains(rep, "ответы не отправлены") {
		t.Error("для уже открытой студентом попытки не нужно предупреждение о начатой попытке")
	}
}

// «Пройти» без ключа: если в базе есть ответы по тесту — попытку начинаем; не всё покрыто —
// текст вопросов и предупреждение, что попытка открыта.
func TestNoKeyForcedWithRecordsStartsAndWarns(t *testing.T) {
	m := newRetryMoodle(t, "0", "Высшая оценка", "2,00 из 2,00")
	sc := newRetryScenario(t, m, false)
	seedBase(t, sc, map[int]string{0: `{"choice":"Максимуму"}`})
	if solved := sc.runForced(t, true); solved {
		t.Fatal("база покрывает не все вопросы — без ключа тест не решается")
	}
	if started, finished := m.counts(); started != 1 || finished != 0 {
		t.Errorf("начато=%d завершено=%d, ожидали 1 и 0", started, finished)
	}
	if rep := sc.reports(); !strings.Contains(rep, "ответы не отправлены") {
		t.Errorf("нет предупреждения о начатой попытке:\n%s", rep)
	}
}

// seedBase пишет верные ответы по вопросам первой страницы (по слотам) в базу.
func seedBase(t *testing.T, sc *retryScenario, answers map[int]string) {
	t.Helper()
	page, err := mustClient(t, sc).GetAttemptPage(context.Background(), 777, 555, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, q := range page.Questions {
		raw, ok := answers[i]
		if !ok {
			continue
		}
		if err := sc.st.SaveVerifiedAnswer(store.VerifiedAnswer{CMID: 777, QHash: quizQuestionHash(q), AnswerJSON: raw, QType: q.QType}); err != nil {
			t.Fatal(err)
		}
	}
}

// Совпадение с известным неверным ответом — один повтор запроса, только для таких вопросов.
func TestWrongAnswerTriggersOneRepeatRequest(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	page := moodle.QuizQuestion{
		Slot: 1, QType: "multichoice", Text: "Чему равна энтропия равновероятных событий?",
		Choices: []moodle.QuizChoice{
			{Name: "q101:1_answer", Value: "0", Text: "Максимуму"},
			{Name: "q101:1_answer", Value: "1", Text: "Нулю"},
		},
	}
	if err := st.SaveWrongAnswer(quizQuestionHash(page), `{"choice":"Максимуму"}`, 777, 10); err != nil {
		t.Fatal(err)
	}
	ai := newFakeAISeq(t, `{"answers":[{"slot":1,"choice":"0"}]}`, `{"answers":[{"slot":1,"choice":"1"}]}`)
	cfg := &config.Config{LLMAPIKey: "fake_key", LLMAPIURL: ai.srv.URL, LLMModel: "fake_model", AdminID: 10}
	sy := &Syncer{Store: st, Cfg: cfg}
	chain := access.QuizChainWithVault(cfg, st, nil, 10)
	tag := quizTag{TG: 10, CMID: 777, Page: 1}

	answers, _, _, err := sy.solvePageWithOpts(context.Background(), tag, nil, nil, chain, "Курс", "", []moodle.QuizQuestion{page}, solvePageOpts{})
	if err != nil {
		t.Fatalf("solvePageWithOpts: %v", err)
	}
	if n := ai.calls.Load(); n != 2 {
		t.Fatalf("запросов к ИИ = %d, ожидали 2 (основной и один повтор)", n)
	}
	if answers[1].Choice != "1" {
		t.Errorf("итоговый ответ = %q, ожидали вариант «Нулю» (1)", answers[1].Choice)
	}
	if !strings.Contains(ai.prompts(), "НЕВЕРНЫЙ") {
		t.Error("в повторе нет пометки «НЕВЕРНЫЙ»")
	}
}

// Вопросы с разделом «Формат» и «Пример»: раздел в промпте отдельно, в поиск не идёт.
func TestFormatSectionInPromptAndQuery(t *testing.T) {
	text := "Закодируйте строку aabab методом LZ77. Формат ответа: (смещение, длина, символ). Пример: (0,0,a)"
	sec := formatSection(text)
	if !strings.HasPrefix(sec, "Формат ответа") || !strings.Contains(sec, "Пример: (0,0,a)") {
		t.Fatalf("formatSection = %q", sec)
	}
	if formatSection("Например, для кода 1010 найдите длину. Приведите пример кодирования.") != "" {
		t.Error("«например» и «пример кодирования» не должны считаться разделом формата")
	}
	q := moodle.QuizQuestion{QType: "shortanswer", Text: text, InputName: "a"}
	query := quizQuery(q)
	if strings.Contains(query, "Формат") || strings.Contains(query, "Пример") {
		t.Errorf("в поиске остался раздел формата: %q", query)
	}
	if !strings.Contains(query, "Закодируйте строку aabab методом LZ77") {
		t.Errorf("в поиске потеряна формулировка задачи: %q", query)
	}
	pc := &pageCtx{wrong: map[int][]string{1: {"(0,0,a)"}}, feedback: map[int]string{}}
	prompt := buildQuizPrompt("Курс", "", []moodle.QuizQuestion{q}, pc)
	if !strings.Contains(prompt, "ФОРМАТ ОТВЕТА из условия") || !strings.Contains(prompt, "Пример: (0,0,a)") {
		t.Errorf("в промпте нет отдельного блока формата:\n%s", prompt)
	}
}

func TestRetryDecisionRules(t *testing.T) {
	high := func(left int) *moodle.QuizInfo {
		return &moodle.QuizInfo{Allowed: left + 1, Used: 1, GradingMethod: "Высшая оценка"}
	}
	last := &moodle.QuizInfo{Allowed: 3, Used: 1, GradingMethod: "Последняя попытка"}
	first := &moodle.QuizInfo{Allowed: 3, Used: 1, GradingMethod: "Первая попытка"}
	average := &moodle.QuizInfo{Allowed: 3, Used: 1, GradingMethod: "Средняя оценка"}
	unlimited := &moodle.QuizInfo{Allowed: 0, GradingMethod: "Высшая оценка"}
	cases := []struct {
		name    string
		info    *moodle.QuizInfo
		n       float64
		learned bool
		fails   int
		retry   bool
	}{
		{"попытки не прочитаны", nil, 0, true, 0, false},
		{"попыток не осталось", &moodle.QuizInfo{Allowed: 2, Used: 2, GradingMethod: "Высшая оценка"}, 0, true, 0, false},
		{"нет нового знания", high(2), 0, false, 0, false},
		{"высшая, частично", high(2), 1, true, 0, true},
		{"высшая, ноль", high(2), 0, true, 0, true},
		{"последняя, ноль", last, 0, true, 0, true},
		{"последняя, частично", last, 1, true, 0, false},
		{"первая, ноль", first, 0, true, 0, false},
		{"средняя, ноль", average, 0, true, 0, false},
		{"средняя, частично", average, 1, true, 0, false},
		{"без ограничений, есть знание", unlimited, 1, true, 0, true},
		{"без ограничений, уже три автоповтора", unlimited, 1, true, 3, false},
	}
	for _, c := range cases {
		if got, note := retryDecision(c.info, c.n, c.learned, c.fails); got != c.retry {
			t.Errorf("%s: повтор=%t (%s), ожидали %t", c.name, got, note, c.retry)
		}
	}
}

func TestBuildQuizReportPartialEscapesAndLayout(t *testing.T) {
	in := quizReportInput{
		Title: "Тест <b>LZ77</b>", Course: "Курс & Co", URL: "https://moodle.example/quiz?id=1&x=2",
		Grade: "0,00 из 1,00", Method: "Последняя попытка", Note: "повтор не запускаю",
		AttemptsLeft: 1, Models: []string{"openai/gpt"}, Confidence: []float64{1.0},
		Search: "капча", FromBase: 1, ByAI: 1,
		Questions: []quizReportQuestion{
			{Slot: 1, Kind: "короткий ответ", Text: "<script>alert(1)</script> условие", Answer: "a<b&c", Mark: "неверно"},
			{Slot: 2, Kind: "один вариант", Text: "Вопрос два", Answer: "Нулю", Mark: "верно"},
		},
	}
	rep := buildQuizReport(in)
	for _, want := range []string{
		"<b>Тест Moodle: оценка 0,00 из 1,00</b>",
		"нажми „Пройти“, осталось попыток 1. повтор не запускаю.",
		"<b>Веб-поиск:</b> капча",
		"из базы 1, через ИИ 1",
		"<b>Уверенность модели:</b> 1.00",
		"<pre>a&lt;b&amp;c</pre>",
		"<b>Вопрос 1</b> (короткий ответ) — неверно: «&lt;script&gt;alert(1)&lt;/script&gt; условие»",
		"Открыть тест в Moodle",
		"https://moodle.example/quiz?id=1&amp;x=2",
	} {
		if !strings.Contains(rep, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, rep)
		}
	}
	if strings.Contains(rep, "<script>") {
		t.Error("сырой HTML из условия попал в отчёт")
	}
	if strings.Contains(rep, "Тест <b>LZ77</b>") {
		t.Error("название теста не экранировано")
	}
}

func TestBuildQuizReportSolvedAndNoKey(t *testing.T) {
	rep := buildQuizReport(quizReportInput{Solved: true, Grade: "1,00 из 1,00", Title: "T", FromBaseNoKey: true, AttemptsLeft: -2})
	if !strings.Contains(rep, "<b>✅ Тест решён: 1,00 из 1,00</b>") {
		t.Errorf("нет заголовка решённого теста:\n%s", rep)
	}
	if strings.Contains(rep, "Дальше") {
		t.Error("у решённого теста не нужна строка «Дальше»")
	}
	if !strings.Contains(rep, "нет ключа ИИ, все ответы из базы") {
		t.Errorf("нет пометки о решении из базы:\n%s", rep)
	}
}

func TestBuildQuizReportTruncatesLongList(t *testing.T) {
	in := quizReportInput{Title: "Длинный", Grade: "0,00 из 60,00", AttemptsLeft: -1, Note: "x"}
	long := strings.Repeat("ответ ", 300)
	for i := 1; i <= 60; i++ {
		in.Questions = append(in.Questions, quizReportQuestion{Slot: i, Kind: "короткий ответ", Text: "условие " + strings.Repeat("слово ", 40), Answer: long})
	}
	rep := buildQuizReport(in)
	if n := len([]rune(rep)); n > quizReportMaxRunes {
		t.Errorf("длина отчёта %d рун, предел %d", n, quizReportMaxRunes)
	}
	if !strings.Contains(rep, "… и ещё") {
		t.Error("нет пометки «… и ещё N вопросов» при обрезке")
	}
	if strings.Contains(rep, "Вопрос 60") {
		t.Error("последний вопрос должен быть обрезан")
	}
	if !strings.Contains(rep, "Вопрос 1") {
		t.Error("первый вопрос должен остаться в отчёте")
	}
}
