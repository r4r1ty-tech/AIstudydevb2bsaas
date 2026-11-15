package moodle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

// Страница просмотра с отметками: верно (класс), неверно (класс + «Ваш ответ неверный»),
// частично (класс + 0,5 балла), верно только по тексту блока состояния.
const reviewVisibleHTML = `<!DOCTYPE html>
<html><head><title>Просмотр попытки</title></head>
<body>
<table class="generaltable quizreviewsummary">
  <tbody>
    <tr><th>Состояние</th><td>Завершены</td></tr>
    <tr><th>Оценка</th><td>3,00 из 4,00 (75%)</td></tr>
  </tbody>
</table>
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
  <div class="outcome"><div class="feedback">Ваш ответ неверный.</div><div class="rightanswer">Правильный ответ: Нулю</div></div>
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
    <div class="qtext"><p>Декодируйте</p></div>
    <div class="answer"><input type="text" name="q500:3_answer" value="0110" readonly></div>
  </div>
</div>
<div class="que shortanswer deferredfeedback notyetanswered" id="question-500-4">
  <div class="info"><div class="state">Верно</div></div>
  <div class="content">
    <div class="qtext"><p>Назовите единицу информации</p></div>
    <div class="answer"><input type="text" name="q500:4_answer" value="бит" readonly></div>
  </div>
</div>
</body></html>`

// Отметки скрыты: в блоках нет ни классов верно/неверно, ни баллов, только состояние complete.
func reviewHiddenHTML(total string) string {
	return `<!DOCTYPE html>
<html><head><title>Просмотр</title></head><body>
<table class="generaltable quizreviewsummary">
  <tbody>
    <tr><th>Состояние</th><td>Завершены</td></tr>
    <tr><th>Оценка</th><td>` + total + `</td></tr>
  </tbody>
</table>
<div class="que multichoice deferredfeedback complete" id="question-600-1">
  <div class="info"><div class="state">Ответ сохранён</div></div>
  <div class="content">
    <div class="qtext"><p>Выберите все коды</p></div>
    <div class="answer">
      <div class="r0"><input type="checkbox" name="q600:1_choice0" id="q600:1_choice0" value="1" checked disabled><label for="q600:1_choice0"><span data-region="answer-label">11000100</span></label></div>
      <div class="r1"><input type="checkbox" name="q600:1_choice1" id="q600:1_choice1" value="1" disabled><label for="q600:1_choice1"><span data-region="answer-label">0101100</span></label></div>
    </div>
  </div>
</div>
<div class="que shortanswer deferredfeedback complete" id="question-600-2">
  <div class="info"><div class="state">Ответ сохранён</div></div>
  <div class="content">
    <div class="qtext"><p>Назовите единицу информации</p></div>
    <div class="answer"><input type="text" name="q600:2_answer" value="бит" readonly></div>
  </div>
</div>
</body></html>`
}

func parseReviewFixture(t *testing.T, src string) *ReviewPage {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return parseReviewPage(doc, 500, 42)
}

func TestParseReviewVisibleMarks(t *testing.T) {
	rp := parseReviewFixture(t, reviewVisibleHTML)
	if rp.TotalMax {
		t.Fatalf("итог 3 из 4 не максимальный, TotalMax=true")
	}
	if len(rp.Questions) != 4 {
		t.Fatalf("вопросов = %d, ожидали 4", len(rp.Questions))
	}
	want := []Verdict{VerdictCorrect, VerdictIncorrect, VerdictPartial, VerdictCorrect}
	wantCorrect := []bool{true, false, false, true}
	for i, q := range rp.Questions {
		if q.Verdict != want[i] {
			t.Errorf("слот %d: отметка %q, ожидали %q", q.Slot, q.Verdict, want[i])
		}
		if q.Correct() != wantCorrect[i] {
			t.Errorf("слот %d: Correct()=%t, ожидали %t", q.Slot, q.Correct(), wantCorrect[i])
		}
		if q.ByTotal {
			t.Errorf("слот %d: ByTotal=true при видимых отметках", q.Slot)
		}
	}
	// Ответ студента: выбранные варианты, текст ввода — из разобранного .que.
	q1 := rp.Questions[0]
	if q1.QType != "multichoice" || len(q1.Choices) != 3 || !q1.Choices[0].Checked || q1.Choices[1].Checked || !q1.Choices[2].Checked {
		t.Errorf("слот 1: варианты/выбор разобраны неверно: %+v", q1.Choices)
	}
	if q1.Grade != "Баллов: 1,00 из 1,00" {
		t.Errorf("слот 1: балл %q", q1.Grade)
	}
	if rp.Questions[3].InputValue != "бит" {
		t.Errorf("слот 4: ответ %q, ожидали «бит»", rp.Questions[3].InputValue)
	}
}

func TestParseReviewHiddenMarksFullTotal(t *testing.T) {
	rp := parseReviewFixture(t, reviewHiddenHTML("10,00 из 10,00 (100%)"))
	if !rp.TotalMax {
		t.Fatalf("итог 10 из 10 должен быть максимальным")
	}
	if len(rp.Questions) != 2 {
		t.Fatalf("вопросов = %d, ожидали 2", len(rp.Questions))
	}
	for _, q := range rp.Questions {
		if q.Verdict != VerdictUnknown || !q.ByTotal || !q.Correct() {
			t.Errorf("слот %d: verdict=%q by_total=%t correct=%t, ожидали отметку по итогу",
				q.Slot, q.Verdict, q.ByTotal, q.Correct())
		}
	}
}

func TestParseReviewHiddenMarksPartialTotal(t *testing.T) {
	rp := parseReviewFixture(t, reviewHiddenHTML("7,00 из 10,00 (70%)"))
	if rp.TotalMax {
		t.Fatalf("итог 7 из 10 не максимальный")
	}
	for _, q := range rp.Questions {
		if q.Verdict != VerdictUnknown || q.ByTotal || q.Correct() {
			t.Errorf("слот %d: при скрытых отметках и неполном итоге ничего не подтверждаем: verdict=%q by_total=%t",
				q.Slot, q.Verdict, q.ByTotal)
		}
	}
}

func TestParseReviewHiddenMarksNoTotal(t *testing.T) {
	// Итог не показан вовсе: ни отметок, ни максимума — ничего не подтверждаем.
	src := strings.Replace(reviewHiddenHTML("x"), `<tr><th>Оценка</th><td>x</td></tr>`, "", 1)
	rp := parseReviewFixture(t, src)
	if rp.TotalMax || rp.Grade != "" {
		t.Fatalf("итога нет, а TotalMax=%t Grade=%q", rp.TotalMax, rp.Grade)
	}
	for _, q := range rp.Questions {
		if q.Correct() {
			t.Errorf("слот %d подтверждён без отметки и итога", q.Slot)
		}
	}
}

func TestReviewVerdictConflictIsUnknown(t *testing.T) {
	// Класс говорит «верно», а баллы 0 из 1: расхождение — отметку не используем.
	src := `<div class="que shortanswer correct" id="question-7-1">
	  <div class="info"><div class="state">Верно</div><div class="grade">Баллов: 0,00 из 1,00</div></div>
	  <div class="answer"><input type="text" name="q7:1_answer" value="x" readonly></div></div>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	que := doc.Find(".que").First()
	if v := reviewVerdict(que, 1, "Баллов: 0,00 из 1,00"); v != VerdictUnknown {
		t.Fatalf("расхождение отметки и баллов: verdict=%q, ожидали пусто", v)
	}
}

func TestGradeFractionHelpers(t *testing.T) {
	cases := []struct {
		in      string
		max     bool
		verdict Verdict
	}{
		{"Баллов: 1,00 из 1,00", true, VerdictCorrect},
		{"Баллов: 1,00/1,00", true, VerdictCorrect},
		{"Баллов: 0,00 из 1,00", false, VerdictIncorrect},
		{"Баллов: 0,50 из 1,00", false, VerdictPartial},
		{"10,00 из 10,00 (100%)", true, VerdictCorrect},
		{"7,00 из 10,00 (70%)", false, VerdictPartial},
		{"", false, VerdictUnknown},
	}
	for _, c := range cases {
		if got := gradeIsMax(c.in); got != c.max {
			t.Errorf("gradeIsMax(%q)=%t, ожидали %t", c.in, got, c.max)
		}
		if got := gradeVerdict(c.in); got != c.verdict {
			t.Errorf("gradeVerdict(%q)=%q, ожидали %q", c.in, got, c.verdict)
		}
	}
}

func TestGetReviewClosedRedirectsToView(t *testing.T) {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/mod/quiz/view.php?id=500", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div id="feedback">Ваша итоговая оценка: 10,00/10,00</div></body></html>`))
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetReview(context.Background(), 500, 42)
	if !errors.Is(err, ErrReviewClosed) {
		t.Fatalf("err = %v, ожидали ErrReviewClosed", err)
	}
}

func TestGetReviewParsesPageWithShowAll(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mod/quiz/review.php" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(reviewVisibleHTML))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rp, err := c.GetReview(context.Background(), 500, 42)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if !strings.Contains(gotQuery, "attempt=42") || !strings.Contains(gotQuery, "showall=1") {
		t.Errorf("запрос review: %q", gotQuery)
	}
	if rp.CMID != 500 || rp.AttemptID != 42 || len(rp.Questions) != 4 {
		t.Fatalf("страница: cmid=%d attempt=%d вопросов=%d", rp.CMID, rp.AttemptID, len(rp.Questions))
	}
}

func TestParseReviewRightAnswerBlock(t *testing.T) {
	src := `<div class="que multichoice deferredfeedback incorrect" id="question-9-1">
	  <div class="info"><div class="state">Неверно</div><div class="grade">Баллов: 0,00 из 1,00</div></div>
	  <div class="outcome"><div class="feedback"><div class="rightanswer">Правильный ответ: Нулю</div></div></div>
	  <div class="answer"><input type="radio" name="q9:1_answer" value="0" checked disabled></div></div>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	rp := parseReviewPage(doc, 9, 1)
	if len(rp.Questions) != 1 || rp.Questions[0].RightAnswer != "Правильный ответ: Нулю" {
		t.Fatalf("RightAnswer = %+v", rp.Questions)
	}
}
