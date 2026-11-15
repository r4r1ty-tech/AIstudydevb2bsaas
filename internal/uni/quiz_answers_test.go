package uni

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

// Страница просмотра: slot1 — верно (чекбоксы), slot2 — неверно (радио), slot3 — частично,
// slot4 — верно, отметка только по тексту состояния.
const answersReviewVisible = `<!DOCTYPE html>
<html><head><title>Просмотр</title></head><body>
<table class="generaltable quizreviewsummary"><tbody>
  <tr><th>Оценка</th><td>3,00 из 4,00 (75%)</td></tr>
</tbody></table>
<div class="que multichoice deferredfeedback correct" id="question-500-1">
  <div class="info"><div class="state">Верно</div><div class="grade">Баллов: 1,00 из 1,00</div></div>
  <div class="content">
    <div class="qtext"><p>Выберите все коды</p></div>
    <div class="answer">
      <div class="r0"><input type="checkbox" name="q500:1_choice0" id="q500:1_choice0" value="1" checked disabled><label for="q500:1_choice0"><span data-region="answer-label">11000100</span></label></div>
      <div class="r1"><input type="checkbox" name="q500:1_choice1" id="q500:1_choice1" value="1" disabled><label for="q500:1_choice1"><span data-region="answer-label">0101100</span></label></div>
      <div class="r0"><input type="checkbox" name="q500:1_choice2" id="q500:1_choice2" value="1" checked disabled><label for="q500:1_choice2"><span data-region="answer-label">1010</span></label></div>
    </div>
  </div>
</div>
<div class="que multichoice deferredfeedback incorrect" id="question-500-2">
  <div class="info"><div class="state">Неверно</div><div class="grade">Баллов: 0,00 из 1,00</div></div>
  <div class="content">
    <div class="qtext"><p>Энтропия равновероятных событий</p></div>
    <div class="answer">
      <div class="r0"><input type="radio" name="q500:2_answer" id="q500:2_answer0" value="0" checked disabled><label for="q500:2_answer0"><span data-region="answer-label">Максимуму</span></label></div>
      <div class="r1"><input type="radio" name="q500:2_answer" id="q500:2_answer1" value="1" disabled><label for="q500:2_answer1"><span data-region="answer-label">Нулю</span></label></div>
    </div>
  </div>
</div>
<div class="que shortanswer deferredfeedback partiallycorrect" id="question-500-3">
  <div class="info"><div class="state">Частично правильно</div><div class="grade">Баллов: 0,50 из 1,00</div></div>
  <div class="content">
    <div class="qtext"><p>Декодируйте сообщение</p></div>
    <div class="answer"><input type="text" name="q500:3_answer" value="0110" readonly></div>
  </div>
</div>
<div class="que shortanswer deferredfeedback correct" id="question-500-4">
  <div class="info"><div class="state">Верно</div></div>
  <div class="content">
    <div class="qtext"><p>Назовите единицу информации</p></div>
    <div class="answer"><input type="text" name="q500:4_answer" value="бит" readonly></div>
  </div>
</div>
</body></html>`

// Отметки скрыты: блоки без классов верно/неверно и без баллов.
func answersReviewHidden(total string) string {
	return `<!DOCTYPE html>
<html><head><title>Просмотр</title></head><body>
<table class="generaltable quizreviewsummary"><tbody>
  <tr><th>Оценка</th><td>` + total + `</td></tr>
</tbody></table>
<div class="que multichoice deferredfeedback complete" id="question-500-1">
  <div class="info"><div class="state">Ответ сохранён</div></div>
  <div class="content">
    <div class="qtext"><p>Выберите все коды</p></div>
    <div class="answer">
      <div class="r0"><input type="checkbox" name="q500:1_choice0" id="q500:1_choice0" value="1" checked disabled><label for="q500:1_choice0"><span data-region="answer-label">11000100</span></label></div>
      <div class="r1"><input type="checkbox" name="q500:1_choice1" id="q500:1_choice1" value="1" disabled><label for="q500:1_choice1"><span data-region="answer-label">0101100</span></label></div>
    </div>
  </div>
</div>
<div class="que shortanswer deferredfeedback complete" id="question-500-4">
  <div class="info"><div class="state">Ответ сохранён</div></div>
  <div class="content">
    <div class="qtext"><p>Назовите единицу информации</p></div>
    <div class="answer"><input type="text" name="q500:4_answer" value="бит" readonly></div>
  </div>
</div>
</body></html>`
}

// reviewServer отдаёт review.php с заданным HTML; пустой html — просмотр закрыт
// (редирект на view.php, как у Moodle при запрете просмотра).
func reviewServer(t *testing.T, html string) *moodle.Client {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		if html == "" {
			http.Redirect(w, r, srv.URL+"/mod/quiz/view.php?id=500", http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div id="feedback">Оценка скрыта</div></body></html>`))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cli, err := moodle.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

// fakeAI — фейковый ИИ: считает запросы и запоминает тела промптов.
type fakeAI struct {
	srv    *httptest.Server
	calls  atomic.Int32
	mu     sync.Mutex
	bodies []string
}

func newFakeAI(t *testing.T, content string) *fakeAI {
	t.Helper()
	return newFakeAISeq(t, content)
}

// newFakeAISeq — фейковый ИИ с ответами по порядку вызовов; после последнего отвечает им же.
func newFakeAISeq(t *testing.T, contents ...string) *fakeAI {
	t.Helper()
	f := &fakeAI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.calls.Add(1))
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(body))
		f.mu.Unlock()
		content := contents[len(contents)-1]
		if n-1 < len(contents) {
			content = contents[n-1]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAI) prompts() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.bodies, "\n")
}

// aiSyncer — Syncer с конфигурацией ИИ на фейковый сервер; цепочка ИИ непустая.
func aiSyncer(t *testing.T, st *store.Store, ai *fakeAI) (*Syncer, []llm.Target) {
	t.Helper()
	cfg := &config.Config{LLMAPIKey: "fake_key", LLMAPIURL: ai.srv.URL, LLMModel: "fake_model", AdminID: 10}
	sy := &Syncer{Store: st, Cfg: cfg}
	chain := access.QuizChainWithVault(cfg, st, nil, 10)
	if len(chain) == 0 {
		t.Fatal("цепочка ИИ пуста: тест бессмысленен")
	}
	return sy, chain
}

// Вопросы второго студента: те же условия, но записаны иначе (регистр, пробелы, ё)
// и варианты перемешаны, номера полей другие.
func secondStudentQuestions() []moodle.QuizQuestion {
	return []moodle.QuizQuestion{
		{
			Slot: 1, QType: "multichoice", Text: "ВЫБЕРИТЕ  все коды",
			Choices: []moodle.QuizChoice{
				{Name: "q900:1_choice0", Value: "1", Text: "0101100", IsMulti: true},
				{Name: "q900:1_choice1", Value: "1", Text: "11000100", IsMulti: true},
				{Name: "q900:1_choice2", Value: "1", Text: "1010", IsMulti: true},
			},
		},
		{
			Slot: 2, QType: "shortanswer", Text: "назовите   единицу информации",
			InputName: "q900:2_answer",
		},
	}
}

func TestQuizQuestionHashNormalization(t *testing.T) {
	base := moodle.QuizQuestion{
		QType: "multichoice", Text: "Всё про ёжика",
		Choices: []moodle.QuizChoice{{Text: "1010"}, {Text: "0101"}},
	}
	h := quizQuestionHash(base)

	same := []moodle.QuizQuestion{
		{QType: "multichoice", Text: "ВСЁ ПРО ЁЖИКА", Choices: []moodle.QuizChoice{{Text: "0101"}, {Text: "1010"}}},
		{QType: "multichoice", Text: "<p>Всё  про <b>ёжика</b></p>", Choices: []moodle.QuizChoice{{Text: " 1010 "}, {Text: "0101"}}},
		{QType: "multichoice", Text: "все про ежика", Choices: []moodle.QuizChoice{{Text: "0101"}, {Text: "1010"}}},
	}
	for i, q := range same {
		if got := quizQuestionHash(q); got != h {
			t.Errorf("вариант %d: хэш отличается (порядок, регистр, ё, HTML не должны менять)", i)
		}
	}

	diff := []moodle.QuizQuestion{
		{QType: "multichoice", Text: "Всё про ёжика", Choices: []moodle.QuizChoice{{Text: "1010"}, {Text: "0110"}}},
		{QType: "shortanswer", Text: "Всё про ёжика", Choices: []moodle.QuizChoice{{Text: "1010"}, {Text: "0101"}}},
		{QType: "multichoice", Text: "Другой вопрос", Choices: []moodle.QuizChoice{{Text: "1010"}, {Text: "0101"}}},
	}
	for i, q := range diff {
		if got := quizQuestionHash(q); got == h {
			t.Errorf("вариант %d: хэш совпал, а вопрос другой", i)
		}
	}
}

func TestAnswerRoundTripByText(t *testing.T) {
	// Текст ответа студента → хранимый вид → сопоставление с перемешанными вариантами.
	cur := moodle.QuizQuestion{
		QType: "multichoice",
		Choices: []moodle.QuizChoice{
			{Name: "qA:1_c0", Value: "7", Text: "1010", IsMulti: true, Checked: true},
			{Name: "qA:1_c1", Value: "8", Text: "0101", IsMulti: true, Checked: false},
			{Name: "qA:1_c2", Value: "9", Text: "1100", IsMulti: true, Checked: true},
		},
	}
	sa, ok := answerFromReview(cur)
	if !ok || len(sa.Choices) != 2 || sa.Choices[0] != "1010" || sa.Choices[1] != "1100" {
		t.Fatalf("answerFromReview multi: %+v ok=%t", sa, ok)
	}
	shuffled := moodle.QuizQuestion{
		QType: "multichoice",
		Choices: []moodle.QuizChoice{
			{Name: "qB:2_c0", Value: "3", Text: "1100", IsMulti: true},
			{Name: "qB:2_c1", Value: "4", Text: "0101", IsMulti: true},
			{Name: "qB:2_c2", Value: "5", Text: "1010", IsMulti: true},
		},
	}
	ans, ok := applyStoredAnswer(shuffled, sa)
	if !ok || len(ans.Choices) != 2 || ans.Choices[0] != "qB:2_c2" || ans.Choices[1] != "qB:2_c0" {
		t.Fatalf("applyStoredAnswer multi: %+v ok=%t", ans, ok)
	}

	// Одиночный выбор: в Moodle уходит value варианта.
	radio := moodle.QuizQuestion{
		QType:   "multichoice",
		Choices: []moodle.QuizChoice{{Name: "r", Value: "0", Text: "Нулю"}, {Name: "r", Value: "1", Text: "Максимуму", Checked: true}},
	}
	sa, ok = answerFromReview(radio)
	if !ok || sa.Choice != "Максимуму" {
		t.Fatalf("answerFromReview radio: %+v ok=%t", sa, ok)
	}
	radioNew := moodle.QuizQuestion{
		QType:   "multichoice",
		Choices: []moodle.QuizChoice{{Name: "r", Value: "3", Text: "Максимуму"}, {Name: "r", Value: "4", Text: "Нулю"}},
	}
	if ans, ok := applyStoredAnswer(radioNew, sa); !ok || ans.Choice != "3" {
		t.Fatalf("applyStoredAnswer radio: %+v ok=%t", ans, ok)
	}

	// Соответствие: подвопрос → текст варианта; в Moodle уходит value варианта по тексту.
	match := moodle.QuizQuestion{
		QType: "match",
		MatchOptions: []moodle.MatchOption{{
			Name: "qM:1_sub0", Question: "Код А", SelectedValue: "2",
			Choices: []moodle.SelectOption{{Value: "1", Text: "0"}, {Value: "2", Text: "10"}},
		}},
	}
	sa, ok = answerFromReview(match)
	if !ok || sa.Matches["Код А"] != "10" {
		t.Fatalf("answerFromReview match: %+v ok=%t", sa, ok)
	}
	matchNew := moodle.QuizQuestion{
		QType: "match",
		MatchOptions: []moodle.MatchOption{{
			Name: "qN:2_sub0", Question: "код а",
			Choices: []moodle.SelectOption{{Value: "5", Text: "0"}, {Value: "6", Text: "10"}},
		}},
	}
	if ans, ok := applyStoredAnswer(matchNew, sa); !ok || ans.Matches["qN:2_sub0"] != "6" {
		t.Fatalf("applyStoredAnswer match: %+v ok=%t", ans, ok)
	}

	// Ввод с клавиатуры.
	text := moodle.QuizQuestion{QType: "shortanswer", InputName: "t", InputValue: " бит "}
	sa, ok = answerFromReview(text)
	if !ok || sa.Text != "бит" {
		t.Fatalf("answerFromReview text: %+v ok=%t", sa, ok)
	}
}

func TestRecordVerifiedAnswersWritesOnlyConfirmed(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	cli := reviewServer(t, answersReviewVisible)

	recordVerifiedAnswers(context.Background(), st, cli, 500, 42, 10, "Теория информации")

	// Записаны только слот 1 (верно, чекбоксы) и слот 4 (верно по тексту состояния).
	rp, err := cli.GetReview(context.Background(), 500, 42)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range rp.Questions {
		_, found, err := st.LookupAnswer(quizQuestionHash(q.QuizQuestion))
		if err != nil {
			t.Fatal(err)
		}
		want := q.Slot == 1 || q.Slot == 4
		if found != want {
			t.Errorf("слот %d: в базе=%t, ожидали %t (отметка %q)", q.Slot, found, want, q.Verdict)
		}
	}

	// Ответ слота 1 — тексты выбранных вариантов.
	rec, ok, err := st.LookupAnswer(quizQuestionHash(rp.Questions[0].QuizQuestion))
	if err != nil || !ok {
		t.Fatalf("слот 1 не найден: ok=%t err=%v", ok, err)
	}
	if !strings.Contains(rec.AnswerJSON, `"choices":["1010","11000100"]`) {
		t.Errorf("answer_json слота 1 = %s", rec.AnswerJSON)
	}
	if rec.Course != "Теория информации" || rec.CMID != 500 || rec.SourceTG != 10 {
		t.Errorf("метаданные записи: %+v", rec)
	}
}

func TestRecordVerifiedAnswersHiddenMarks(t *testing.T) {
	// Два вопроса страницы в том виде, как их собирает студент: им соответствуют хэши в базе.
	choiceQ := moodle.QuizQuestion{QType: "multichoice", Text: "Выберите все коды",
		Choices: []moodle.QuizChoice{{Text: "11000100"}, {Text: "0101100"}}}
	textQ := moodle.QuizQuestion{QType: "shortanswer", Text: "Назовите единицу информации", InputName: "x"}

	cases := []struct {
		name string
		html string
		want bool // должны ли вопросы попасть в базу
	}{
		{"отметки скрыты, итог полный", answersReviewHidden("10,00 из 10,00 (100%)"), true},
		{"отметки скрыты, итог неполный", answersReviewHidden("7,00 из 10,00 (70%)"), false},
		{"просмотр закрыт настройками", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, err := setupStore(t)
			if err != nil {
				t.Fatal(err)
			}
			recordVerifiedAnswers(context.Background(), st, reviewServer(t, c.html), 500, 42, 10, "Курс")
			for _, q := range []moodle.QuizQuestion{choiceQ, textQ} {
				_, found, err := st.LookupAnswer(quizQuestionHash(q))
				if err != nil {
					t.Fatal(err)
				}
				if found != c.want {
					t.Errorf("вопрос %q: в базе=%t, ожидали %t", q.Text, found, c.want)
				}
			}
		})
	}
}

// Второй студент с перемешанными вариантами получает ответы из базы; ИИ не вызывается.
func TestSecondStudentGetsAnswersFromBaseWithoutAI(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	recordVerifiedAnswers(context.Background(), st, reviewServer(t, answersReviewVisible), 500, 42, 10, "Курс")

	ai := newFakeAI(t, `{"answers":[]}`)
	sy, chain := aiSyncer(t, st, ai)
	tag := quizTag{TG: 11, CMID: 500, Page: 1}

	answers, _, pl, err := sy.solvePageWithOpts(context.Background(), tag, nil, nil, chain, "Курс", "", secondStudentQuestions(), solvePageOpts{})
	if err != nil {
		t.Fatalf("solvePageWithOpts: %v", err)
	}
	if n := ai.calls.Load(); n != 0 {
		t.Fatalf("ИИ вызван %d раз, ожидали 0", n)
	}
	// Ответы в базе отсортированы по тексту: 1010 (choice2), затем 11000100 (choice1).
	if got := answers[1].Choices; len(got) != 2 || got[0] != "q900:1_choice2" || got[1] != "q900:1_choice1" {
		t.Errorf("ответ слота 1 = %v, ожидали [q900:1_choice2 q900:1_choice1]", got)
	}
	if answers[2].Text != "бит" {
		t.Errorf("ответ слота 2 = %q, ожидали «бит»", answers[2].Text)
	}
	if len(pl.Notes) == 0 || !strings.Contains(pl.Notes[0], "из базы") {
		t.Errorf("в отчёте страницы нет пометки про базу: %v", pl.Notes)
	}
}

// Частично найденная страница: ИИ получает только ненайденные вопросы.
func TestPartialPageAsksAIOnlyForUnknown(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	recordVerifiedAnswers(context.Background(), st, reviewServer(t, answersReviewVisible), 500, 42, 10, "Курс")

	ai := newFakeAI(t, `{"answers":[{"slot":3,"text":"1101"}]}`)
	sy, chain := aiSyncer(t, st, ai)
	questions := append(secondStudentQuestions()[:1], moodle.QuizQuestion{
		Slot: 3, QType: "shortanswer", Text: "Декодируйте новое сообщение", InputName: "q900:3_answer",
	})
	tag := quizTag{TG: 11, CMID: 500, Page: 1}

	answers, _, pl, err := sy.solvePageWithOpts(context.Background(), tag, nil, nil, chain, "Курс", "", questions, solvePageOpts{})
	if err != nil {
		t.Fatalf("solvePageWithOpts: %v", err)
	}
	if n := ai.calls.Load(); n != 1 {
		t.Fatalf("ИИ вызван %d раз, ожидали 1", n)
	}
	prompt := ai.prompts()
	if !strings.Contains(prompt, "Декодируйте новое сообщение") {
		t.Errorf("в промпте нет ненайденного вопроса")
	}
	if strings.Contains(prompt, "11000100") {
		t.Errorf("в промпт ИИ попал вопрос, найденный в базе")
	}
	if len(answers[1].Choices) != 2 || answers[3].Text != "1101" {
		t.Errorf("ответы: слот1=%v слот3=%q", answers[1].Choices, answers[3].Text)
	}
	if len(pl.Notes) == 0 || !strings.Contains(pl.Notes[0], "слоты [1]") {
		t.Errorf("в отчёте должны быть слоты из базы: %v", pl.Notes)
	}
}

// Ответ в базе есть, но его текст не совпадает ни с одним вариантом страницы: вопрос
// считается ненайденным и уходит ИИ.
func TestUnmatchedVariantGoesToAI(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	page := moodle.QuizQuestion{
		Slot: 1, QType: "multichoice", Text: "Выберите все коды",
		Choices: []moodle.QuizChoice{
			{Name: "q900:1_choice0", Value: "1", Text: "11000100", IsMulti: true},
			{Name: "q900:1_choice1", Value: "1", Text: "0101100", IsMulti: true},
		},
	}
	// Запись под хэшем этого вопроса, но с текстом варианта, которого на странице нет.
	if err := st.SaveVerifiedAnswer(store.VerifiedAnswer{
		QHash: quizQuestionHash(page), QType: "multichoice", AnswerJSON: `{"choices":["9999"]}`,
	}); err != nil {
		t.Fatal(err)
	}

	ai := newFakeAI(t, `{"answers":[{"slot":1,"choices":["11000100"]}]}`)
	sy, chain := aiSyncer(t, st, ai)
	tag := quizTag{TG: 11, CMID: 500, Page: 1}

	answers, _, pl, err := sy.solvePageWithOpts(context.Background(), tag, nil, nil, chain, "Курс", "", []moodle.QuizQuestion{page}, solvePageOpts{})
	if err != nil {
		t.Fatalf("solvePageWithOpts: %v", err)
	}
	if n := ai.calls.Load(); n != 1 {
		t.Fatalf("ИИ вызван %d раз, ожидали 1", n)
	}
	if got := answers[1].Choices; len(got) != 1 || got[0] != "q900:1_choice0" {
		t.Errorf("ответ от ИИ: %v, ожидали выбор 11000100 (q900:1_choice0)", got)
	}
	for _, n := range pl.Notes {
		if strings.Contains(n, "из базы") {
			t.Errorf("несопоставленный ответ помечен как из базы: %q", n)
		}
	}
}

// Вопрос 2 (неверный в answersReviewVisible): ответ студента «Максимуму».
func answersQ2() moodle.QuizQuestion {
	return moodle.QuizQuestion{QType: "multichoice", Text: "Энтропия равновероятных событий",
		Choices: []moodle.QuizChoice{{Name: "a", Value: "0", Text: "Максимуму"}, {Name: "b", Value: "1", Text: "Нулю"}}}
}

// Вопрос 4 (верный в answersReviewVisible): короткий ответ «бит».
func answersQ4() moodle.QuizQuestion {
	return moodle.QuizQuestion{QType: "shortanswer", Text: "Назовите единицу информации", InputName: "x"}
}

// Отравленная база: верная запись совпадает с ответом, который Moodle отметил неверным — удаляется.
func TestPoisonedVerifiedAnswerDeleted(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	hash := quizQuestionHash(answersQ2())
	if err := st.SaveVerifiedAnswer(store.VerifiedAnswer{QHash: hash, AnswerJSON: `{"choice":"Максимуму"}`}); err != nil {
		t.Fatal(err)
	}
	newRight, newWrong := recordVerifiedAnswers(context.Background(), st, reviewServer(t, answersReviewVisible), 500, 42, 10, "Курс")
	if _, ok, _ := st.PeekAnswer(hash); ok {
		t.Fatal("отравленная верная запись осталась в базе")
	}
	if list, _ := st.ListWrongAnswers(hash); len(list) != 1 || list[0] != `{"choice":"Максимуму"}` {
		t.Errorf("неверный ответ не запомнен: %v", list)
	}
	if newWrong < 1 || newRight < 2 {
		t.Errorf("новых знаний: верных %d, неверных %d", newRight, newWrong)
	}
}

// Верный ответ удаляет такой же ответ из списка неверных.
func TestVerifiedDropsEquivalentWrongAnswer(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	hash := quizQuestionHash(answersQ4())
	if err := st.SaveWrongAnswer(hash, `{"text":"бит"}`, 500, 10); err != nil {
		t.Fatal(err)
	}
	recordVerifiedAnswers(context.Background(), st, reviewServer(t, answersReviewVisible), 500, 42, 10, "Курс")
	if list, _ := st.ListWrongAnswers(hash); len(list) != 0 {
		t.Errorf("неверный ответ «бит» остался после верного: %v", list)
	}
	if _, ok, _ := st.PeekAnswer(hash); !ok {
		t.Error("верный ответ «бит» не записан")
	}
}

// Страховка при поиске: запись есть, но тот же ответ известен как неверный — вопрос ненайденный.
func TestLookupSkipsAnswerKnownWrong(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	q := answersQ4()
	hash := quizQuestionHash(q)
	if err := st.SaveVerifiedAnswer(store.VerifiedAnswer{QHash: hash, AnswerJSON: `{"text":"бит"}`}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWrongAnswer(hash, `{"text":"бит"}`, 500, 10); err != nil {
		t.Fatal(err)
	}
	if _, ok := lookupQuestionAnswer(st, quizTag{TG: 10, CMID: 500, Page: 1}, q); ok {
		t.Error("ответ, известный как неверный, не должен отдаваться из базы")
	}
	if p := (&Syncer{Store: st}).baseCoversPages([]*moodle.AttemptPage{{Questions: []moodle.QuizQuestion{q}}}); p {
		t.Error("база не должна считаться покрывающей при известном неверном ответе")
	}
}

// Вопросы с рисунком не пишем в базу и не ищем по ней.
func TestImageQuestionsNotStoredNorLooked(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	q := answersQ4()
	hash := quizQuestionHash(q)
	if err := st.SaveVerifiedAnswer(store.VerifiedAnswer{QHash: hash, AnswerJSON: `{"text":"бит"}`}); err != nil {
		t.Fatal(err)
	}
	q.Images = []string{"https://moodle.example/pluginfile.php/7/x.png"}
	if _, ok := lookupQuestionAnswer(st, quizTag{TG: 10, CMID: 500, Page: 1}, q); ok {
		t.Error("вопрос с рисунком не ищем в базе")
	}
	if baseOK := (&Syncer{Store: st}).baseCoversPages([]*moodle.AttemptPage{{Questions: []moodle.QuizQuestion{q}}}); baseOK {
		t.Error("страница с рисунком не покрывается базой")
	}

	imgReview := `<!DOCTYPE html><html><body>
<table class="generaltable quizreviewsummary"><tbody><tr><th>Оценка</th><td>1,00 из 1,00</td></tr></tbody></table>
<div class="que shortanswer deferredfeedback correct" id="question-500-5">
  <div class="info"><div class="state">Верно</div></div>
  <div class="content">
    <div class="qtext"><p>Назовите единицу</p><img src="https://moodle.example/pluginfile.php/7/x.png" alt="image"></div>
    <div class="answer"><input type="text" name="q500:5_answer" value="бит" readonly></div>
  </div>
</div></body></html>`
	newRight, newWrong := recordVerifiedAnswers(context.Background(), st, reviewServer(t, imgReview), 500, 42, 10, "Курс")
	if newRight != 0 || newWrong != 0 {
		t.Errorf("вопрос с рисунком записан: верных %d, неверных %d", newRight, newWrong)
	}
}

// Два одинаковых текста варианта — ответ неоднозначен: не пишем и не сопоставляем.
func TestDuplicateChoiceTextsAreAmbiguous(t *testing.T) {
	dup := moodle.QuizQuestion{QType: "multichoice", Text: "Выберите", Choices: []moodle.QuizChoice{
		{Name: "a", Value: "0", Text: "Нулю", Checked: true}, {Name: "b", Value: "1", Text: "нулю "}}}
	if _, ok := answerFromReview(dup); ok {
		t.Error("ответ с повторяющимся текстом варианта не должен записываться")
	}
	if _, ok := findChoiceByText(dup.Choices, "Нулю"); ok {
		t.Error("findChoiceByText вернул вариант при повторе текста")
	}
	if _, ok := applyStoredAnswer(dup, quizStoredAnswer{Choice: "Нулю"}); ok {
		t.Error("applyStoredAnswer сопоставил неоднозначный текст")
	}

	match := moodle.QuizQuestion{QType: "match", MatchOptions: []moodle.MatchOption{{
		Name: "m", Question: "Код", SelectedValue: "1",
		Choices: []moodle.SelectOption{{Value: "1", Text: "10"}, {Value: "2", Text: "10"}},
	}}}
	if _, ok := answerFromReview(match); ok {
		t.Error("соответствие с повторяющимся текстом варианта не должно записываться")
	}
	if _, ok := optionValueByText(match.MatchOptions[0].Choices, "10"); ok {
		t.Error("optionValueByText вернул значение при повторе текста")
	}
}

// Возвращаемые значения: первый обход — новые знания, повтор того же — ни одного, закрытый просмотр — ни одного.
func TestRecordReturnsNewKnowledgeOnly(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	cli := reviewServer(t, answersReviewVisible)
	r1, w1 := recordVerifiedAnswers(context.Background(), st, cli, 500, 42, 10, "Курс")
	if r1 != 2 || w1 != 1 {
		t.Fatalf("первый обход: верных %d, неверных %d; ожидали 2 и 1", r1, w1)
	}
	r2, w2 := recordVerifiedAnswers(context.Background(), st, cli, 500, 42, 10, "Курс")
	if r2 != 0 || w2 != 0 {
		t.Errorf("повторный обход тех же ответов: верных %d, неверных %d; ожидали 0 и 0", r2, w2)
	}
	closed := reviewServer(t, "")
	if r, w := recordVerifiedAnswers(context.Background(), st, closed, 500, 42, 10, "Курс"); r != 0 || w != 0 {
		t.Errorf("закрытый просмотр дал знание: %d, %d", r, w)
	}
}

// Правильный ответ из блока «Правильный ответ» однозначен только для одного варианта и короткого ответа.
func TestRightAnswerBlockParsing(t *testing.T) {
	single := moodle.QuizQuestion{QType: "multichoice", Choices: []moodle.QuizChoice{
		{Name: "a", Value: "0", Text: "Максимуму"}, {Name: "b", Value: "1", Text: "Нулю"}}}
	if sa, ok := rightAnswerSA(single, "Правильный ответ: Нулю"); !ok || sa.Choice != "Нулю" {
		t.Errorf("один вариант: %+v ok=%t", sa, ok)
	}
	if _, ok := rightAnswerSA(single, "Правильный ответ: Что-то другое"); ok {
		t.Error("ответ, которого нет среди вариантов, не должен браться")
	}
	multi := moodle.QuizQuestion{QType: "multichoice", Choices: []moodle.QuizChoice{
		{Name: "a", Value: "1", Text: "x", IsMulti: true}, {Name: "b", Value: "1", Text: "y", IsMulti: true}}}
	if _, ok := rightAnswerSA(multi, "Правильный ответ: x, y"); ok {
		t.Error("несколько вариантов не разбираем")
	}
	short := answersQ4()
	if sa, ok := rightAnswerSA(short, "Правильный ответ: бит"); !ok || sa.Text != "бит" {
		t.Errorf("короткий ответ: %+v ok=%t", sa, ok)
	}
}
