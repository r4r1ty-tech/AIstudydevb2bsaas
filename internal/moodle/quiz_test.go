package moodle

import (
	"context"
	"github.com/PuerkitoBio/goquery"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleQuizViewHTML = `<!DOCTYPE html>
<html>
<head><title>Тест: Неравномерное кодирование</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_QUIZ_123"};</script>
<div class="quizinfo">
  <p>Разрешено попыток: 2</p>
  <p>Ограничение по времени: 45 мин.</p>
  <p>Метод оценивания: Высшая оценка</p>
</div>
<div class="quizstartbuttondiv">
  <form method="post" action="BASE/mod/quiz/startattempt.php">
    <input type="hidden" name="cmid" value="61422">
    <input type="hidden" name="sesskey" value="SK_QUIZ_123">
    <input type="hidden" name="_qf__mod_quiz_form_preflight_check_form" value="1">
    <input type="submit" name="submitbutton" value="Начать попытку">
  </form>
</div>
<div id="feedback">
  <h3>Ваша итоговая оценка за этот тест: 10,00/10,00</h3>
</div>
<table class="generaltable quizreviewsummary">
  <thead><tr><th>Попытка</th><th>Состояние</th><th>Оценка</th></tr></thead>
  <tbody>
    <tr>
      <td class="cell c0">1</td>
      <td class="cell c1 state">Завершены</td>
      <td class="cell c2 grade">10,00 из 10,00 (100%)</td>
      <td class="cell c3"><a href="BASE/mod/quiz/review.php?attempt=888&cmid=61422">Просмотр</a></td>
    </tr>
  </tbody>
</table>
</body>
</html>`

const sampleAttemptHTML = `<!DOCTYPE html>
<html>
<head><title>Попытка 1</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_QUIZ_123"};</script>
<span id="quiz-time-left">44:12</span>
<form id="responseform" method="post" action="BASE/mod/quiz/processattempt.php?cmid=61422" enctype="multipart/form-data">
  <input type="hidden" name="attempt" value="999">
  <input type="hidden" name="thispage" value="0">
  <input type="hidden" name="nextpage" value="-1">
  <input type="hidden" name="timeup" value="0">
  <input type="hidden" name="sesskey" value="SK_QUIZ_123">
  <input type="hidden" name="slots" value="1,2,3">

  <!-- Вопрос 1: multichoice checkbox (несколько вариантов) -->
  <div class="que multichoice deferredfeedback notyetanswered" id="question-607183-1">
    <div class="info">
      <input type="hidden" name="q607183:1_:sequencecheck" value="1">
      <input type="hidden" name="q607183:1_:flagged" value="0">
    </div>
    <div class="content">
      <div class="qtext"><p>Выберите из списка ВСЕ коды:</p></div>
      <div class="prompt">Выберите один или несколько ответов:</div>
      <div class="answer">
        <div class="r0">
          <input type="hidden" name="q607183:1_choice0" value="0">
          <input type="checkbox" name="q607183:1_choice0" id="q607183:1_choice0" value="1">
          <label for="q607183:1_choice0"><span data-region="answer-label">11000100</span></label>
        </div>
        <div class="r1">
          <input type="hidden" name="q607183:1_choice1" value="0">
          <input type="checkbox" name="q607183:1_choice1" id="q607183:1_choice1" value="1" checked="checked">
          <label for="q607183:1_choice1"><span data-region="answer-label">0101100</span></label>
        </div>
      </div>
    </div>
  </div>

  <!-- Вопрос 2: shortanswer со склейкой битовых строк -->
  <div class="que shortanswer deferredfeedback notyetanswered" id="question-607206-2">
    <div class="info">
      <input type="hidden" name="q607206:2_:sequencecheck" value="2">
      <input type="hidden" name="q607206:2_:flagged" value="0">
    </div>
    <div class="content">
      <div class="qtext">
        <p>Декодируйте сообщение по Шеннону-Фано:</p>
        <p>0101011100010101010</p>
        <p>1010101110001010101</p>
      </div>
      <div class="answer">
        <input type="text" name="q607206:2_answer" id="q607206:2_answer" value="предыдущий ответ">
      </div>
    </div>
  </div>

  <!-- Вопрос 3: adaptive с кнопкой "Проверить" -->
  <div class="que shortanswer adaptive notyetanswered" id="question-607209-3">
    <div class="info">
      <input type="hidden" name="q607209:3_:sequencecheck" value="3">
      <input type="hidden" name="q607209:3_:flagged" value="0">
    </div>
    <div class="content">
      <div class="qtext"><p>Постройте код Элиаса для числа 17:</p></div>
      <div class="answer">
        <input type="text" name="q607209:3_answer" id="q607209:3_answer" value="">
      </div>
      <div class="im-controls">
        <input type="submit" name="q607209:3_-submit" value="Проверить">
      </div>
    </div>
  </div>
</form>
</body>
</html>`

const sampleSummaryHTML = `<!DOCTYPE html>
<html>
<head><title>Сводка попытки</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_QUIZ_456"};</script>
<table class="generaltable quizsummarytable">
  <tbody>
    <tr><td>1</td><td>Ответ сохранен</td></tr>
    <tr><td>2</td><td>Ответ сохранен</td></tr>
    <tr><td>3</td><td>Верно</td></tr>
  </tbody>
</table>
<form method="post" action="BASE/mod/quiz/processattempt.php">
  <input type="hidden" name="attempt" value="999">
  <input type="hidden" name="finishattempt" value="1">
  <input type="hidden" name="timeup" value="0">
  <input type="hidden" name="slots" value="">
  <input type="hidden" name="sesskey" value="SK_QUIZ_456">
  <input type="submit" value="Отправить всё и завершить тест">
</form>
</body>
</html>`

const sampleReviewHTML = `<!DOCTYPE html>
<html>
<head><title>Просмотр попытки</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_QUIZ_456"};</script>
<table class="generaltable quizreviewsummary">
  <tbody>
    <tr><th>Состояние</th><td>Завершены</td></tr>
    <tr><th>Оценка</th><td>9,00 из 10,00 (90%)</td></tr>
  </tbody>
</table>
<div id="feedback">Хорошая работа!</div>
<div class="que multichoice deferredfeedback correct" id="question-607183-1">
  <div class="info">
    <div class="state">Верно</div>
    <div class="grade">Баллов: 1,00/1,00</div>
  </div>
</div>
<div class="que shortanswer deferredfeedback correct" id="question-607206-2">
  <div class="info">
    <div class="state">Верно</div>
    <div class="grade">Баллов: 1,00/1,00</div>
  </div>
</div>
</body>
</html>`

func TestInspectAndAttemptLifecycle(t *testing.T) {
	var srv *httptest.Server
	mux := http.NewServeMux()

	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(sampleQuizViewHTML, "BASE", srv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.FormValue("cmid") != "61422" {
			t.Errorf("expected cmid=61422, got %s", r.FormValue("cmid"))
		}
		if r.FormValue("sesskey") != "SK_QUIZ_123" {
			t.Errorf("expected sesskey=SK_QUIZ_123, got %s", r.FormValue("sesskey"))
		}
		if r.FormValue("_qf__mod_quiz_form_preflight_check_form") != "1" {
			t.Errorf("expected preflight=1, got %s", r.FormValue("_qf__mod_quiz_form_preflight_check_form"))
		}
		http.Redirect(w, r, srv.URL+"/mod/quiz/attempt.php?attempt=999&cmid=61422", http.StatusSeeOther)
	})

	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(sampleAttemptHTML, "BASE", srv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	var lastMultipartFields map[string][]string
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if strings.HasPrefix(mediaType, "multipart/") {
			mr := multipart.NewReader(r.Body, params["boundary"])
			lastMultipartFields = make(map[string][]string)
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("next part: %v", err)
				}
				slurp, _ := io.ReadAll(p)
				lastMultipartFields[p.FormName()] = append(lastMultipartFields[p.FormName()], string(slurp))
			}

			// Если была нажата кнопка проверки слота
			if len(lastMultipartFields["q607209:3_-submit"]) > 0 {
				http.Redirect(w, r, srv.URL+"/mod/quiz/attempt.php?attempt=999&cmid=61422", http.StatusSeeOther)
				return
			}

			http.Redirect(w, r, srv.URL+"/mod/quiz/summary.php?attempt=999&cmid=61422", http.StatusSeeOther)
			return
		}

		// urlencoded (завершение попытки finishattempt=1)
		r.ParseForm()
		if r.FormValue("finishattempt") == "1" {
			http.Redirect(w, r, srv.URL+"/mod/quiz/review.php?attempt=999&cmid=61422", http.StatusSeeOther)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
	})

	mux.HandleFunc("/mod/quiz/summary.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(sampleSummaryHTML, "BASE", srv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	mux.HandleFunc("/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(sampleReviewHTML, "BASE", srv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	srv = httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	cli, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 1. InspectQuiz
	info, err := cli.InspectQuiz(ctx, 61422)
	if err != nil {
		t.Fatalf("InspectQuiz: %v", err)
	}
	if !info.CanStart {
		t.Errorf("expected CanStart=true")
	}
	if len(info.Rules) != 3 {
		t.Errorf("expected 3 rules, got %d", len(info.Rules))
	}
	if !strings.Contains(info.Feedback, "10,00/10,00") {
		t.Errorf("expected feedback with grade, got %q", info.Feedback)
	}
	if len(info.Attempts) != 1 || info.Attempts[0].State != "Завершены" {
		t.Errorf("unexpected attempts: %+v", info.Attempts)
	}

	// 2. StartOrResumeAttempt
	page, err := cli.StartOrResumeAttempt(ctx, 61422)
	if err != nil {
		t.Fatalf("StartOrResumeAttempt: %v", err)
	}
	if page.AttemptID != 999 {
		t.Errorf("expected AttemptID=999, got %d", page.AttemptID)
	}
	if page.TimeLeft != "44:12" {
		t.Errorf("expected TimeLeft=44:12, got %q", page.TimeLeft)
	}
	if len(page.Questions) != 3 {
		t.Fatalf("expected 3 questions, got %d", len(page.Questions))
	}

	// Проверяем Вопрос 1: multichoice checkbox
	q1 := page.Questions[0]
	if q1.Slot != 1 || q1.QType != "multichoice" || !q1.Choices[0].IsMulti {
		t.Errorf("unexpected q1: %+v", q1)
	}
	if len(q1.Choices) != 2 || q1.Choices[0].Text != "11000100" {
		t.Errorf("unexpected choices for q1: %+v", q1.Choices)
	}

	// Проверяем Вопрос 2: shortanswer со склейкой битовых строк
	q2 := page.Questions[1]
	if q2.Slot != 2 || q2.QType != "shortanswer" {
		t.Errorf("unexpected q2: %+v", q2)
	}
	expectedBits := "01010111000101010101010101110001010101"
	if !strings.Contains(q2.Text, expectedBits) {
		t.Errorf("expected bit string merged without space %q, got q2.Text=%q", expectedBits, q2.Text)
	}

	// Проверяем Вопрос 3: adaptive с кнопкой
	q3 := page.Questions[2]
	if q3.Slot != 3 || !q3.HasSubmitButton {
		t.Errorf("unexpected q3: %+v", q3)
	}

	// 3. SubmitAnswers с проверкой конкретного слота (adaptive)
	ans := map[int]QuizAnswer{
		1: {Choices: []string{"q607183:1_choice0"}},
		2: {Text: "декодированный текст"},
		3: {Text: "000010001"},
	}
	updatedPage, err := cli.SubmitAnswers(ctx, page, ans, 3)
	if err != nil {
		t.Fatalf("SubmitAnswers checkSlot: %v", err)
	}
	if updatedPage.AttemptID != 999 {
		t.Errorf("expected updatedPage.AttemptID=999, got %d", updatedPage.AttemptID)
	}
	if len(lastMultipartFields["q607209:3_-submit"]) == 0 {
		t.Errorf("expected submit button field q607209:3_-submit sent")
	}

	// 4. SubmitAnswers переход к сводке (checkSlot=0)
	summaryPage, err := cli.SubmitAnswers(ctx, page, ans, 0)
	if err != nil {
		t.Fatalf("SubmitAnswers next: %v", err)
	}
	if summaryPage.Page != -1 {
		t.Errorf("expected summary page with Page=-1, got %d", summaryPage.Page)
	}

	// 5. FinishAttempt
	result, err := cli.FinishAttempt(ctx, 61422, 999)
	if err != nil {
		t.Fatalf("FinishAttempt: %v", err)
	}
	if !result.ReviewAllowed {
		t.Errorf("expected ReviewAllowed=true")
	}
	if !strings.Contains(result.Grade, "9,00") {
		t.Errorf("expected Grade containing 9,00, got %q", result.Grade)
	}
	if len(result.Questions) != 2 {
		t.Errorf("expected 2 reviewed questions, got %d", len(result.Questions))
	}
}

func TestFinishAttemptReviewForbiddenRedirectToView(t *testing.T) {
	var srv *httptest.Server
	mux := http.NewServeMux()

	mux.HandleFunc("/mod/quiz/summary.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<script>M.cfg = {"sesskey":"SK_QUIZ_777"};</script>`))
	})

	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		// Редирект на review.php
		http.Redirect(w, r, srv.URL+"/mod/quiz/review.php?attempt=111&cmid=61422", http.StatusSeeOther)
	})

	mux.HandleFunc("/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		// Просмотр запрещён настройками теста -> редирект на view.php
		http.Redirect(w, r, srv.URL+"/mod/quiz/view.php?id=61422", http.StatusSeeOther)
	})

	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(sampleQuizViewHTML, "BASE", srv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	srv = httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	cli, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := cli.FinishAttempt(ctx, 61422, 111)
	if err != nil {
		t.Fatalf("FinishAttempt: %v", err)
	}
	if res.ReviewAllowed {
		t.Errorf("expected ReviewAllowed=false due to redirect to view.php")
	}
	if !strings.Contains(res.Grade, "10,00") {
		t.Errorf("expected Grade extracted from view.php, got %q", res.Grade)
	}
}

// Таблица кода и рисунок в условии не теряются (решатель отвечал «нет таблицы»).
func TestExtractQTextTableAndImage(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<div class="qtext">
<p>Декодируйте последовательность, используя неравномерный код:</p>
<table><tr><th>Символ</th><th>Код</th></tr>
<tr><td>а</td><td>0</td></tr><tr><td>б</td><td>10</td></tr>
<tr><td>в</td><td><img src="https://do.ssau.ru/moodle/pluginfile.php/1/v.png" alt="image"></td></tr></table>
<p>Последовательность:</p><p>0101</p><p>1000</p>
Текст без абзаца <img src="https://do.ssau.ru/moodle/pluginfile.php/1/tree.png" alt="дерево Шеннона-Фано">
</div>`))
	if err != nil {
		t.Fatal(err)
	}
	text, imgs := extractQTextImages(doc.Find(".qtext"), "")
	for _, want := range []string{"Символ | Код", "а | 0", "б | 10", "в | [рисунок 1]", "01011000", "Текст без абзаца [рисунок 2: дерево Шеннона-Фано]"} {
		if !strings.Contains(text, want) {
			t.Errorf("нет %q в\n%s", want, text)
		}
	}
	if len(imgs) != 2 || !strings.HasSuffix(imgs[1], "tree.png") {
		t.Fatalf("images=%v", imgs)
	}
}

// Со страницы review достаём правильный ответ и отзыв; текст правильного
// ответа не дублируется в отзыве.
func TestParseReviewRightAnswerAndFeedback(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<div id="region-main">
<div class="que" id="question-5-1"><div class="info"><div class="state">Неверно</div><div class="grade">Баллов: 0,00 из 1,00</div></div><div class="outcome"><div class="feedback"><div class="rightanswer">Правильный ответ: 101</div></div></div></div>
<div class="que" id="question-5-2"><div class="info"><div class="state">Частично правильно</div><div class="grade">Баллов: 0,50 из 1,00</div></div><div class="outcome"><div class="feedback"><div class="rightanswer">Правильный ответ: 0110</div><div class="specificfeedback">Ошибка на втором шаге</div></div></div></div>
</div>`))
	if err != nil {
		t.Fatal(err)
	}
	qs := parseReview(doc)
	if len(qs) != 2 {
		t.Fatalf("вопросов = %d, ожидали 2: %+v", len(qs), qs)
	}

	q := qs[0]
	if q.Slot != 1 || q.Status != "Неверно" || q.Grade != "Баллов: 0,00 из 1,00" {
		t.Errorf("вопрос 1: %+v", q)
	}
	if q.RightAnswer != "Правильный ответ: 101" {
		t.Errorf("RightAnswer = %q", q.RightAnswer)
	}
	if q.Feedback != "" {
		t.Errorf("Feedback = %q, ожидали пусто (только правильный ответ)", q.Feedback)
	}

	q = qs[1]
	if q.Slot != 2 || q.RightAnswer != "Правильный ответ: 0110" {
		t.Errorf("вопрос 2: %+v", q)
	}
	if q.Feedback != "Ошибка на втором шаге" {
		t.Errorf("Feedback = %q, ожидали отзыв без правильного ответа", q.Feedback)
	}
}
