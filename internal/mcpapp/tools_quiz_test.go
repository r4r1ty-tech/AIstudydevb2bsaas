package mcpapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

// quizTestImage — маленький PNG-подобный набор байт: содержимое важно только
// для сверки, клиенты картинку не декодируют.
var quizTestImage = []byte("\x89PNG\r\n\x1a\nquiz-image-bytes")

// quizCounters считает POST-запросы к фейковому Moodle и запоминает поля
// последней отправки. Тесты проверяют, что чтение не делает POST, а write-
// инструменты отправляют ровно один запрос.
type quizCounters struct {
	mu         sync.Mutex
	posts      int
	startPOST  int
	submitPOST int
	finishPOST int
	started    bool
	fields     map[string][]string
	gate       chan struct{}
	arrived    chan struct{}
}

func (c *quizCounters) note(r *http.Request) {
	if r.Method != http.MethodPost {
		return
	}
	c.mu.Lock()
	c.posts++
	c.mu.Unlock()
}

func (c *quizCounters) setStarted(v bool) {
	c.mu.Lock()
	c.started = v
	c.mu.Unlock()
}

func (c *quizCounters) isStarted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

func (c *quizCounters) setGate(gate, arrived chan struct{}) {
	c.mu.Lock()
	c.gate, c.arrived = gate, arrived
	c.mu.Unlock()
}

func (c *quizCounters) waitGate() {
	c.mu.Lock()
	gate, arrived := c.gate, c.arrived
	c.mu.Unlock()
	if arrived != nil {
		select {
		case arrived <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		<-gate
	}
}

func (c *quizCounters) snapshot() (posts, start, submit, finish int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.posts, c.startPOST, c.submitPOST, c.finishPOST
}

func (c *quizCounters) reset() {
	c.mu.Lock()
	c.posts, c.startPOST, c.submitPOST, c.finishPOST = 0, 0, 0, 0
	c.fields = nil
	c.mu.Unlock()
}

func (c *quizCounters) setFields(fields map[string][]string) {
	c.mu.Lock()
	c.fields = fields
	c.mu.Unlock()
}

func (c *quizCounters) lastFields() map[string][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fields
}

// newQuizServer поднимает фейковый Moodle: логин и курсы — из fakeMoodle,
// страницы теста — из фикстур ниже.
func newQuizServer(t *testing.T) (*httptest.Server, *quizCounters) {
	t.Helper()
	fake := newFakeMoodle()
	counters := &quizCounters{}
	fallback := fake.handler()
	var srv *httptest.Server

	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters.note(r)
		switch r.URL.Path {
		case "/moodle/mod/quiz/view.php":
			html := quizViewHTML(counters.isStarted())
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(html))

		case "/moodle/mod/quiz/startattempt.php":
			counters.mu.Lock()
			counters.startPOST++
			counters.mu.Unlock()
			counters.waitGate()
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if r.FormValue("cmid") != "61422" || r.FormValue("sesskey") != testSesskey {
				http.Error(w, "bad start form", http.StatusBadRequest)
				return
			}
			counters.setStarted(true)
			http.Redirect(w, r, srv.URL+"/moodle/mod/quiz/attempt.php?attempt=999&cmid=61422", http.StatusSeeOther)

		case "/moodle/mod/quiz/attempt.php":
			html := quizAttemptPage0(srv.URL)
			switch r.URL.Query().Get("page") {
			case "1":
				html = quizAttemptPage1(srv.URL)
			case "5":
				html = quizAttemptEssay()
			case "6":
				html = quizAttemptFinished()
			case "7":
				html = quizAttemptEmpty()
			case "8":
				html = quizAttemptPage1(srv.URL)
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(html))

		case "/moodle/mod/quiz/processattempt.php":
			fields := readQuizForm(t, r)
			counters.setFields(fields)
			counters.mu.Lock()
			counters.submitPOST++
			finish := firstValue(fields, "finishattempt") == "1"
			if finish {
				counters.finishPOST++
			}
			counters.mu.Unlock()
			switch {
			case finish:
				http.Redirect(w, r, srv.URL+"/moodle/mod/quiz/review.php?attempt=999&cmid=61422", http.StatusSeeOther)
			case hasCheckButton(fields):
				http.Redirect(w, r, srv.URL+"/moodle/mod/quiz/attempt.php?attempt=999&cmid=61422", http.StatusSeeOther)
			case firstValue(fields, "nextpage") == "1":
				http.Redirect(w, r, srv.URL+"/moodle/mod/quiz/attempt.php?attempt=999&cmid=61422&page=1", http.StatusSeeOther)
			default:
				http.Redirect(w, r, srv.URL+"/moodle/mod/quiz/summary.php?attempt=999&cmid=61422", http.StatusSeeOther)
			}

		case "/moodle/mod/quiz/summary.php":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(quizSummaryHTML()))

		case "/moodle/mod/quiz/review.php":
			if r.URL.Query().Get("attempt") == "111" {
				http.Redirect(w, r, srv.URL+"/moodle/mod/quiz/view.php?id=61422", http.StatusSeeOther)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(quizReviewHTML()))

		case "/moodle/pluginfile.php/1/q1.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(quizTestImage)

		case "/moodle/pluginfile.php/1/q4.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(quizTestImage)

		case "/moodle/pluginfile.php/1/big.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(bytes.Repeat([]byte{0x89}, maxInlineImageBytes+1))

		case "/moodle/pluginfile.php/1/huge.png":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", strconv.FormatInt(moodle.FileLimitDefault+1, 10))
			_, _ = w.Write([]byte("too large"))

		default:
			fallback.ServeHTTP(w, r)
		}
	})
	srv = httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv, counters
}

// readQuizForm разбирает multipart или urlencoded тело отправки.
func readQuizForm(t *testing.T, r *http.Request) map[string][]string {
	t.Helper()
	fields := map[string][]string{}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content-type: %v", err)
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, perr := mr.NextPart()
			if perr == io.EOF {
				break
			}
			if perr != nil {
				t.Fatalf("multipart: %v", perr)
			}
			data, _ := io.ReadAll(part)
			fields[part.FormName()] = append(fields[part.FormName()], string(data))
		}
		return fields
	}
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	for key, values := range r.PostForm {
		fields[key] = append([]string(nil), values...)
	}
	return fields
}

func firstValue(fields map[string][]string, key string) string {
	if values := fields[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}

func hasCheckButton(fields map[string][]string) bool {
	for key := range fields {
		if strings.HasSuffix(key, "_-submit") {
			return true
		}
	}
	return false
}

func newQuizSession(t *testing.T, srvURL string, allowWrites bool) *Session {
	t.Helper()
	cfg := testConfig(t, srvURL)
	cfg.Login, cfg.Password = testLogin, testPassword
	cfg.AllowWrites = allowWrites
	sess, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestQuizWritesDisabledByDefault(t *testing.T) {
	srv, counters := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, false)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	var info QuizInfoOutput
	callMaterialTool(t, cs, ctx, "moodle_quiz_info", map[string]any{"cmid": 61422}, &info)
	if !info.CanStart || info.Allowed != 2 || info.AttemptsLeft != 2 {
		t.Fatalf("info = %+v", info)
	}

	cases := []struct {
		name string
		args map[string]any
	}{
		{"moodle_quiz_start", map[string]any{"cmid": 61422, "confirm": true}},
		{"moodle_quiz_submit", map[string]any{"cmid": 61422, "attempt_id": 999, "page": 0, "confirm": true, "answers": map[string]any{"1": map[string]any{"choice": "111"}}}},
		{"moodle_quiz_finish", map[string]any{"cmid": 61422, "attempt_id": 999, "confirm": true}},
	}
	for _, tc := range cases {
		msg := callToolError(t, cs, ctx, tc.name, tc.args)
		if !strings.Contains(msg, "writes_disabled") {
			t.Errorf("%s: ошибка = %q, ожидался writes_disabled", tc.name, msg)
		}
	}
	if posts, start, submit, finish := counters.snapshot(); posts != 0 || start != 0 || submit != 0 || finish != 0 {
		t.Fatalf("write-запросы при выключенной записи: posts=%d start=%d submit=%d finish=%d", posts, start, submit, finish)
	}
}

func TestQuizConfirmationRequired(t *testing.T) {
	srv, counters := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, true)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	cases := []struct {
		name string
		args map[string]any
	}{
		{"moodle_quiz_start", map[string]any{"cmid": 61422}},
		{"moodle_quiz_submit", map[string]any{"cmid": 61422, "attempt_id": 999, "page": 0, "answers": map[string]any{}}},
		{"moodle_quiz_finish", map[string]any{"cmid": 61422, "attempt_id": 999}},
	}
	for _, tc := range cases {
		msg := callToolError(t, cs, ctx, tc.name, tc.args)
		if !strings.Contains(msg, "confirmation_required") {
			t.Errorf("%s: ошибка = %q, ожидался confirmation_required", tc.name, msg)
		}
	}
	if posts, start, _, _ := counters.snapshot(); posts != 0 || start != 0 {
		t.Fatalf("без confirm ушло запросов: posts=%d start=%d", posts, start)
	}
}

func TestQuizReadOnlyToolsDoNotPost(t *testing.T) {
	srv, counters := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, true)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	var info QuizInfoOutput
	callMaterialTool(t, cs, ctx, "moodle_quiz_info", map[string]any{"cmid": 61422}, &info)
	var page QuizPageOutput
	callMaterialTool(t, cs, ctx, "moodle_quiz_page", map[string]any{"cmid": 61422, "attempt_id": 999, "page": 0}, &page)
	var review moodle.ReviewPage
	callMaterialTool(t, cs, ctx, "moodle_quiz_review", map[string]any{"cmid": 61422, "attempt_id": 999}, &review)

	if posts, _, _, _ := counters.snapshot(); posts != 0 {
		t.Fatalf("read-only инструменты сделали %d POST", posts)
	}
	if len(page.Questions) != 4 || len(page.Slots) != 4 {
		t.Fatalf("страница: вопросов %d, слотов %d", len(page.Questions), len(page.Slots))
	}
	if !strings.Contains(review.Grade, "9,00") {
		t.Fatalf("review grade = %q", review.Grade)
	}
}

func TestQuizLifecycle(t *testing.T) {
	srv, counters := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, true)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	var start QuizPageOutput
	callMaterialTool(t, cs, ctx, "moodle_quiz_start", map[string]any{"cmid": 61422, "confirm": true}, &start)
	if start.AttemptID != 999 || start.Page != 0 || start.NextPage != 1 || start.NeedsFinish {
		t.Fatalf("старт: %+v", start)
	}
	if start.TimeLeft != "44:12" || len(start.Questions) != 4 {
		t.Fatalf("старт: time_left=%q вопросов=%d", start.TimeLeft, len(start.Questions))
	}
	if _, startPOST, _, _ := counters.snapshot(); startPOST != 1 {
		t.Fatalf("startattempt POST = %d, want 1", startPOST)
	}

	// Повторный старт должен продолжить попытку, а не начинать новую.
	counters.reset()
	var resumed QuizPageOutput
	callMaterialTool(t, cs, ctx, "moodle_quiz_start", map[string]any{"cmid": 61422, "confirm": true}, &resumed)
	if resumed.AttemptID != 999 {
		t.Fatalf("продолжение: %+v", resumed)
	}
	if posts, startPOST, _, _ := counters.snapshot(); posts != 0 || startPOST != 0 {
		t.Fatalf("продолжение сделало POST: posts=%d start=%d", posts, startPOST)
	}

	// Отправка страницы 0: radio, shortanswer и match.
	counters.reset()
	var after QuizPageOutput
	callMaterialTool(t, cs, ctx, "moodle_quiz_submit", map[string]any{
		"cmid":       61422,
		"attempt_id": 999,
		"page":       0,
		"confirm":    true,
		"answers": map[string]any{
			"1": map[string]any{"choice": "111"},
			"2": map[string]any{"text": "0101"},
			"3": map[string]any{"matches": map[string]string{"альфа": "первый", "q603:3_sub1": "второй"}},
		},
	}, &after)
	if after.Page != 1 || after.NextPage != -1 || !after.NeedsFinish {
		t.Fatalf("после страницы 0: %+v", after)
	}
	fields := counters.lastFields()
	for key, want := range map[string]string{
		"q601:1_answer": "1",
		"q602:2_answer": "0101",
		"q603:3_sub0":   "1",
		"q603:3_sub1":   "2",
		"nextpage":      "1",
	} {
		if got := firstValue(fields, key); got != want {
			t.Errorf("поле %s = %q, want %q", key, got, want)
		}
	}

	// check_slot: проверка adaptive-вопроса без перехода на следующую страницу.
	counters.reset()
	var checked QuizPageOutput
	callMaterialTool(t, cs, ctx, "moodle_quiz_submit", map[string]any{
		"cmid":       61422,
		"attempt_id": 999,
		"page":       0,
		"check_slot": 6,
		"confirm":    true,
		"answers":    map[string]any{"6": map[string]any{"text": "42"}},
	}, &checked)
	if checked.Page != 0 || checked.NextPage != 1 {
		t.Fatalf("check_slot: %+v", checked)
	}
	if got := firstValue(counters.lastFields(), "q606:6_-submit"); got == "" {
		t.Fatalf("check_slot не отправил кнопку проверки: %v", counters.lastFields())
	}

	// Последняя страница: truefalse и numerical.
	counters.reset()
	var summary QuizPageOutput
	callMaterialTool(t, cs, ctx, "moodle_quiz_submit", map[string]any{
		"cmid":       61422,
		"attempt_id": 999,
		"page":       1,
		"confirm":    true,
		"answers": map[string]any{
			"4": map[string]any{"choice": "Верно"},
			"5": map[string]any{"text": "17"},
		},
	}, &summary)
	if summary.Page != -1 || !summary.NeedsFinish {
		t.Fatalf("сводка: %+v", summary)
	}
	fields = counters.lastFields()
	if firstValue(fields, "q604:4_answer") != "1" || firstValue(fields, "q605:5_answer") != "17" {
		t.Fatalf("ответы последней страницы: %v", fields)
	}

	// Завершение попытки.
	counters.reset()
	var result moodle.QuizResult
	callMaterialTool(t, cs, ctx, "moodle_quiz_finish", map[string]any{"cmid": 61422, "attempt_id": 999, "confirm": true}, &result)
	if !result.ReviewAllowed || !strings.Contains(result.Grade, "9,00") || len(result.Questions) != 2 {
		t.Fatalf("результат: %+v", result)
	}
	if _, _, _, finishPOST := counters.snapshot(); finishPOST != 1 {
		t.Fatalf("finishattempt POST = %d, want 1", finishPOST)
	}

	// Просмотр завершённой попытки — GET, без POST.
	counters.reset()
	var review moodle.ReviewPage
	callMaterialTool(t, cs, ctx, "moodle_quiz_review", map[string]any{"cmid": 61422, "attempt_id": 999}, &review)
	if len(review.Questions) != 2 || !strings.Contains(review.Grade, "9,00") {
		t.Fatalf("review: %+v", review)
	}
	if posts, _, _, _ := counters.snapshot(); posts != 0 {
		t.Fatalf("review сделал %d POST", posts)
	}
}

func TestQuizSubmitValidation(t *testing.T) {
	srv, counters := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, true)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	// Неизвестный слот.
	msg := callToolError(t, cs, ctx, "moodle_quiz_submit", map[string]any{
		"cmid": 61422, "attempt_id": 999, "page": 0, "confirm": true,
		"answers": map[string]any{"99": map[string]any{"text": "x"}},
	})
	if !strings.Contains(msg, "bad_input") {
		t.Fatalf("неизвестный слот: %q", msg)
	}
	// Несуществующий вариант radio.
	msg = callToolError(t, cs, ctx, "moodle_quiz_submit", map[string]any{
		"cmid": 61422, "attempt_id": 999, "page": 0, "confirm": true,
		"answers": map[string]any{"1": map[string]any{"choice": "нет такого"}},
	})
	if !strings.Contains(msg, "bad_input") {
		t.Fatalf("неизвестный вариант: %q", msg)
	}
	// check_slot без кнопки проверки.
	msg = callToolError(t, cs, ctx, "moodle_quiz_submit", map[string]any{
		"cmid": 61422, "attempt_id": 999, "page": 0, "check_slot": 1, "confirm": true,
		"answers": map[string]any{},
	})
	if !strings.Contains(msg, "bad_input") {
		t.Fatalf("check_slot без кнопки: %q", msg)
	}
	// Неизвестный тип вопроса: essay на странице 5.
	msg = callToolError(t, cs, ctx, "moodle_quiz_submit", map[string]any{
		"cmid": 61422, "attempt_id": 999, "page": 5, "confirm": true,
		"answers": map[string]any{"9": map[string]any{"text": "эссе"}},
	})
	if !strings.Contains(msg, "unsupported_question") {
		t.Fatalf("essay: %q", msg)
	}
	if _, _, submitPOST, _ := counters.snapshot(); submitPOST != 0 {
		t.Fatalf("при ошибках валидации ушло %d processattempt POST", submitPOST)
	}
}

// TestMultiChoiceIgnoresValue: у чекбоксов Moodle value="1" у всех, поэтому
// multi-ответ ищется только по name и тексту.
func TestMultiChoiceIgnoresValue(t *testing.T) {
	q := moodle.QuizQuestion{Slot: 1, QType: "multichoice", Choices: []moodle.QuizChoice{
		{Name: "q601:1_choice0", Value: "1", Text: "000", IsMulti: true},
		{Name: "q601:1_choice1", Value: "1", Text: "111", IsMulti: true},
	}}
	if _, err := multiChoiceValues(q, []string{"1"}); !errors.Is(err, ErrQuizBadInput) {
		t.Fatalf("choices=[\"1\"]: %v, ожидался bad_input", err)
	}
	got, err := multiChoiceValues(q, []string{"111"})
	if err != nil || len(got) != 1 || got[0] != "q601:1_choice1" {
		t.Fatalf("по тексту: %v, %v", got, err)
	}
	got, err = multiChoiceValues(q, []string{"q601:1_choice0"})
	if err != nil || len(got) != 1 || got[0] != "q601:1_choice0" {
		t.Fatalf("по name: %v, %v", got, err)
	}

	// Вариант с текстом «1» выбирается по тексту.
	qText := moodle.QuizQuestion{Slot: 2, QType: "multichoice", Choices: []moodle.QuizChoice{
		{Name: "q602:2_choice0", Value: "1", Text: "0", IsMulti: true},
		{Name: "q602:2_choice1", Value: "1", Text: "1", IsMulti: true},
	}}
	got, err = multiChoiceValues(qText, []string{"1"})
	if err != nil || len(got) != 1 || got[0] != "q602:2_choice1" {
		t.Fatalf("текст «1»: %v, %v", got, err)
	}
	ans, err := mapQuizAnswer(qText, QuizAnswerInput{Choices: []string{"1"}})
	if err != nil || len(ans.Choices) != 1 || ans.Choices[0] != "q602:2_choice1" {
		t.Fatalf("mapQuizAnswer: %+v, %v", ans, err)
	}

	// У radio value остаётся основным ключом.
	radio := moodle.QuizQuestion{Slot: 3, QType: "multichoice", Choices: []moodle.QuizChoice{
		{Name: "q603:3_answer", Value: "0", Text: "нет"},
		{Name: "q603:3_answer", Value: "1", Text: "да"},
	}}
	if val, err := singleChoiceValue(radio, QuizAnswerInput{Choice: "1"}); err != nil || val != "1" {
		t.Fatalf("radio по value: %q, %v", val, err)
	}
}

// TestQuizRejectsNonAttemptPage: редирект на review/view или чужая страница
// не должны приводить к отправке.
func TestQuizRejectsNonAttemptPage(t *testing.T) {
	srv, counters := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, true)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	for _, pageNo := range []int{6, 7} {
		msg := callToolError(t, cs, ctx, "moodle_quiz_page", map[string]any{
			"cmid": 61422, "attempt_id": 999, "page": pageNo,
		})
		if !strings.Contains(msg, "bad_input") {
			t.Errorf("page=%d: %q, ожидался bad_input", pageNo, msg)
		}
	}

	msg := callToolError(t, cs, ctx, "moodle_quiz_submit", map[string]any{
		"cmid": 61422, "attempt_id": 999, "page": 6, "confirm": true, "answers": map[string]any{},
	})
	if !strings.Contains(msg, "bad_input") {
		t.Fatalf("submit на review-страницу: %q", msg)
	}

	// Moodle вернул страницу 1 вместо запрошенной 8.
	msg = callToolError(t, cs, ctx, "moodle_quiz_submit", map[string]any{
		"cmid": 61422, "attempt_id": 999, "page": 8, "confirm": true, "answers": map[string]any{},
	})
	if !strings.Contains(msg, "bad_input") || !strings.Contains(msg, "запрошена страница 8") {
		t.Fatalf("несовпадение страницы: %q", msg)
	}

	if _, _, submitPOST, _ := counters.snapshot(); submitPOST != 0 {
		t.Fatalf("при неверной странице ушло %d processattempt POST", submitPOST)
	}
}

func TestQuizReviewClosed(t *testing.T) {
	srv, _ := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, true)
	cs, ctx := connectMCP(t, sess)

	msg := callToolError(t, cs, ctx, "moodle_quiz_review", map[string]any{"cmid": 61422, "attempt_id": 111})
	if !strings.Contains(msg, "review_closed") {
		t.Fatalf("review закрыт: %q", msg)
	}
}

func TestQuizImage(t *testing.T) {
	srv, counters := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, true)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "moodle_quiz_image",
		Arguments: map[string]any{
			"cmid": 61422, "attempt_id": 999, "slot": 1, "index": 0,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out QuizImageOutput
	decodeStructured(t, res, &out)
	if out.MIMEType != "image/png" || !out.Inline || out.Size != int64(len(quizTestImage)) {
		t.Fatalf("image = %+v", out)
	}
	saved, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatalf("сохранённый рисунок: %v", err)
	}
	if string(saved) != string(quizTestImage) {
		t.Fatalf("содержимое файла отличается: %d байт", len(saved))
	}
	if filepath.Dir(out.Path) != filepath.Join(sess.cfg.StateDir, quizImagesDir) {
		t.Fatalf("путь рисунка вне StateDir/images: %s", out.Path)
	}
	info, err := os.Stat(out.Path)
	if err != nil {
		t.Fatalf("stat рисунка: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("права рисунка = %o, want 0600", perm)
	}
	var imageContent *mcp.ImageContent
	for _, content := range res.Content {
		if img, ok := content.(*mcp.ImageContent); ok {
			imageContent = img
		}
	}
	if imageContent == nil || imageContent.MIMEType != "image/png" || string(imageContent.Data) != string(quizTestImage) {
		t.Fatalf("image content: %+v", imageContent)
	}
	if posts, _, _, _ := counters.snapshot(); posts != 0 {
		t.Fatalf("image сделал %d POST", posts)
	}

	// Рисунок на второй странице: инструмент обходит страницы попытки.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "moodle_quiz_image",
		Arguments: map[string]any{
			"cmid": 61422, "attempt_id": 999, "slot": 4, "index": 0,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var page1 QuizImageOutput
	decodeStructured(t, res, &page1)
	if page1.Slot != 4 || page1.MIMEType != "image/png" || page1.Path == "" {
		t.Fatalf("рисунок второй страницы: %+v", page1)
	}

	// Чужой хост отклоняется до запроса.
	msg := callToolError(t, cs, ctx, "moodle_quiz_image", map[string]any{
		"cmid": 61422, "attempt_id": 999, "slot": 1, "index": 1,
	})
	if !strings.Contains(msg, "forbidden_host") {
		t.Fatalf("чужой хост: %q", msg)
	}

	// Больше 8 МБ: без inline, но файл сохранён.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "moodle_quiz_image",
		Arguments: map[string]any{
			"cmid": 61422, "attempt_id": 999, "slot": 1, "index": 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var big QuizImageOutput
	decodeStructured(t, res, &big)
	if big.Inline || big.Path == "" || big.Size != int64(maxInlineImageBytes+1) {
		t.Fatalf("большой рисунок: %+v", big)
	}
	if info, err := os.Stat(big.Path); err != nil || info.Size() != int64(maxInlineImageBytes+1) {
		t.Fatalf("файл большого рисунка: %v, %+v", err, info)
	}

	// Потолок 25 МБ: too_large, без чтения тела.
	msg = callToolError(t, cs, ctx, "moodle_quiz_image", map[string]any{
		"cmid": 61422, "attempt_id": 999, "slot": 1, "index": 3,
	})
	if !strings.Contains(msg, "too_large") {
		t.Fatalf("рисунок больше 25 МБ: %q", msg)
	}

	msg = callToolError(t, cs, ctx, "moodle_quiz_image", map[string]any{
		"cmid": 61422, "attempt_id": 999, "slot": 1, "index": 7,
	})
	if !strings.Contains(msg, "bad_input") {
		t.Fatalf("несуществующий рисунок: %q", msg)
	}
}

func TestQuizResourceAndPrompt(t *testing.T) {
	srv, _ := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, true)
	cs, ctx := connectMCP(t, sess)

	resource, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "moodle://quiz/61422/info"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resource.Contents) != 1 {
		t.Fatalf("ресурс: %+v", resource)
	}
	var info QuizInfoOutput
	if err := json.Unmarshal([]byte(resource.Contents[0].Text), &info); err != nil {
		t.Fatalf("ресурс JSON: %v", err)
	}
	if info.CMID != 61422 || !info.CanStart || info.GradingMethod != "Высшая оценка" {
		t.Fatalf("ресурс: %+v", info)
	}

	prompt, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      PromptSolveQuiz,
		Arguments: map[string]string{"cmid": "61422"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompt.Messages) != 1 {
		t.Fatalf("промпт: %+v", prompt)
	}
	text, ok := prompt.Messages[0].Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("промпт не текст: %T", prompt.Messages[0].Content)
	}
	for _, want := range []string{"moodle_quiz_info", "moodle_quiz_start", "confirm=true", "unsupported_question", "moodle_quiz_review", "MOODLE_MCP_ALLOW_WRITES"} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("в промпте нет %q", want)
		}
	}
}

func TestQuizAnnotations(t *testing.T) {
	srv, _ := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, false)
	cs, ctx := connectMCP(t, sess)

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	writes := map[string]bool{
		"moodle_quiz_start":  true,
		"moodle_quiz_submit": true,
		"moodle_quiz_finish": true,
	}
	found := map[string]bool{}
	for _, tool := range list.Tools {
		switch tool.Name {
		case "moodle_quiz_info", "moodle_quiz_page", "moodle_quiz_review", "moodle_quiz_image",
			"moodle_quiz_start", "moodle_quiz_submit", "moodle_quiz_finish":
			found[tool.Name] = true
			if tool.Annotations == nil {
				t.Errorf("%s: нет аннотаций", tool.Name)
				continue
			}
			if writes[tool.Name] == tool.Annotations.ReadOnlyHint {
				t.Errorf("%s: readOnlyHint=%t, не соответствует write-инструменту", tool.Name, tool.Annotations.ReadOnlyHint)
			}
		}
	}
	for _, name := range []string{
		"moodle_quiz_info", "moodle_quiz_page", "moodle_quiz_review", "moodle_quiz_image",
		"moodle_quiz_start", "moodle_quiz_submit", "moodle_quiz_finish",
	} {
		if !found[name] {
			t.Errorf("в tools/list нет %s", name)
		}
	}
}

// TestQuizCMIDLock проверяет сериализацию write-действий по cmid.
func TestQuizCMIDLock(t *testing.T) {
	guard := newQuizGuard()
	release, err := guard.acquire(61422, "start")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.acquire(61422, "submit"); !errors.Is(err, ErrQuizBusy) {
		t.Fatalf("параллельный submit: %v, ожидался ErrQuizBusy", err)
	}
	release()
	release2, err := guard.acquire(61422, "finish")
	if err != nil {
		t.Fatalf("после освобождения: %v", err)
	}
	release2()
}

// TestQuizConcurrentStartBusy проверяет блокировку на уровне инструмента:
// второй start по тому же cmid получает quiz_busy, пока первый ещё в Moodle.
func TestQuizConcurrentStartBusy(t *testing.T) {
	srv, counters := newQuizServer(t)
	sess := newQuizSession(t, srv.URL, true)
	cs, ctx := connectMCP(t, sess)
	counters.reset()

	gate := make(chan struct{})
	arrived := make(chan struct{}, 1)
	counters.setGate(gate, arrived)
	var once sync.Once
	release := func() {
		once.Do(func() { close(gate) })
		counters.setGate(nil, nil)
	}
	defer release()

	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "moodle_quiz_start",
			Arguments: map[string]any{"cmid": 61422, "confirm": true},
		})
		done <- err
	}()

	select {
	case <-arrived:
	case <-ctx.Done():
		t.Fatal("первый start не дошёл до startattempt")
	}

	msg := callToolError(t, cs, ctx, "moodle_quiz_start", map[string]any{"cmid": 61422, "confirm": true})
	if !strings.Contains(msg, "quiz_busy") {
		t.Fatalf("второй start: %q, ожидался quiz_busy", msg)
	}

	release()
	if err := <-done; err != nil {
		t.Fatalf("первый start: %v", err)
	}
}

func TestAllowWritesConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOODLE_MCP_ALLOW_WRITES", "1")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowWrites {
		t.Fatal("MOODLE_MCP_ALLOW_WRITES=1 не включил запись")
	}

	// JSON-файл может явно выключить запись после env=true.
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{"allow_writes":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowWrites {
		t.Fatal("allow_writes=false в JSON не выключил запись")
	}

	t.Setenv("MOODLE_MCP_ALLOW_WRITES", "maybe")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("опечатка в MOODLE_MCP_ALLOW_WRITES должна быть ошибкой")
	}
}

func quizViewHTML(started bool) string {
	start := `<div class="quizstartbuttondiv">
<form method="post" action="/moodle/mod/quiz/startattempt.php">
<input type="hidden" name="cmid" value="61422">
<input type="submit" name="submitbutton" value="Начать попытку">
</form>
</div>`
	if started {
		start = `<table class="quizattemptsummary"><tbody>
<tr><td class="state">В процессе</td><td><a href="/moodle/mod/quiz/attempt.php?attempt=999&amp;cmid=61422">Продолжить</a></td></tr>
</tbody></table>`
	}
	return `<!DOCTYPE html><html><body>
<div class="quizinfo">
<p>Разрешено попыток: 2</p>
<p>Метод оценивания: Высшая оценка</p>
</div>
` + start + `
</body></html>`
}

func quizAttemptPage0(base string) string {
	return `<!DOCTYPE html><html><body>
<script>M.cfg = {"sesskey":"` + testSesskey + `"};</script>
<span id="quiz-time-left">44:12</span>
<form id="responseform" method="post" action="` + base + `/moodle/mod/quiz/processattempt.php?cmid=61422" enctype="multipart/form-data">
<input type="hidden" name="attempt" value="999">
<input type="hidden" name="thispage" value="0">
<input type="hidden" name="nextpage" value="1">
<input type="hidden" name="slots" value="1,2,3,6">

<div class="que multichoice deferredfeedback notyetanswered" id="question-601-1">
  <div class="info"><input type="hidden" name="q601:1_:sequencecheck" value="1"></div>
  <div class="content">
    <div class="qtext"><p>Выберите код:</p><img src="` + base + `/moodle/pluginfile.php/1/q1.png" alt="код"><img src="https://evil.example/q.png" alt="чужой"><img src="` + base + `/moodle/pluginfile.php/1/big.png" alt="большой"><img src="` + base + `/moodle/pluginfile.php/1/huge.png" alt="огромный"></div>
    <div class="prompt">Выберите один ответ:</div>
    <div class="answer">
      <div class="r0"><input type="radio" name="q601:1_answer" id="q601:1_answer0" value="0"><label for="q601:1_answer0"><span data-region="answer-label">000</span></label></div>
      <div class="r1"><input type="radio" name="q601:1_answer" id="q601:1_answer1" value="1"><label for="q601:1_answer1"><span data-region="answer-label">111</span></label></div>
    </div>
  </div>
</div>

<div class="que shortanswer deferredfeedback notyetanswered" id="question-602-2">
  <div class="info"><input type="hidden" name="q602:2_:sequencecheck" value="1"></div>
  <div class="content">
    <div class="qtext"><p>Запишите последовательность:</p></div>
    <div class="answer"><input type="text" name="q602:2_answer" id="q602:2_answer" value=""></div>
  </div>
</div>

<div class="que match deferredfeedback notyetanswered" id="question-603-3">
  <div class="info"><input type="hidden" name="q603:3_:sequencecheck" value="1"></div>
  <div class="content">
    <div class="qtext"><p>Сопоставьте понятия:</p></div>
    <div class="answer"><table>
      <tr><td class="text">альфа</td><td><select name="q603:3_sub0"><option value="">Выберите...</option><option value="1">первый</option><option value="2">второй</option></select></td></tr>
      <tr><td class="text">бета</td><td><select name="q603:3_sub1"><option value="">Выберите...</option><option value="1">первый</option><option value="2">второй</option></select></td></tr>
    </table></div>
  </div>
</div>

<div class="que shortanswer adaptive notyetanswered" id="question-606-6">
  <div class="info"><input type="hidden" name="q606:6_:sequencecheck" value="1"></div>
  <div class="content">
    <div class="qtext"><p>Проверьте себя:</p></div>
    <div class="answer"><input type="text" name="q606:6_answer" id="q606:6_answer" value=""></div>
    <div class="im-controls"><input type="submit" name="q606:6_-submit" value="Проверить"></div>
  </div>
</div>

</form></body></html>`
}

func quizAttemptPage1(base string) string {
	return `<!DOCTYPE html><html><body>
<script>M.cfg = {"sesskey":"` + testSesskey + `"};</script>
<form id="responseform" method="post" action="/moodle/mod/quiz/processattempt.php?cmid=61422" enctype="multipart/form-data">
<input type="hidden" name="attempt" value="999">
<input type="hidden" name="thispage" value="1">
<input type="hidden" name="nextpage" value="-1">
<input type="hidden" name="slots" value="4,5">

<div class="que truefalse deferredfeedback notyetanswered" id="question-604-4">
  <div class="info"><input type="hidden" name="q604:4_:sequencecheck" value="1"></div>
  <div class="content">
    <div class="qtext"><p>Энтропия максимальна при равномерном распределении.</p><img src="` + base + `/moodle/pluginfile.php/1/q4.png" alt="график"></div>
    <div class="answer">
      <div class="r0"><input type="radio" name="q604:4_answer" id="q604:4_answer1" value="1"><label for="q604:4_answer1"><span data-region="answer-label">Верно</span></label></div>
      <div class="r1"><input type="radio" name="q604:4_answer" id="q604:4_answer0" value="0"><label for="q604:4_answer0"><span data-region="answer-label">Неверно</span></label></div>
    </div>
  </div>
</div>

<div class="que numerical deferredfeedback notyetanswered" id="question-605-5">
  <div class="info"><input type="hidden" name="q605:5_:sequencecheck" value="1"></div>
  <div class="content">
    <div class="qtext"><p>Сколько бит нужно для 17 символов?</p></div>
    <div class="answer"><input type="text" name="q605:5_answer" value=""></div>
  </div>
</div>

</form></body></html>`
}

func quizAttemptFinished() string {
	return `<!DOCTYPE html><html><body>
<script>M.cfg = {"sesskey":"` + testSesskey + `"};</script>
<table class="generaltable quizreviewsummary"><tbody>
<tr><th>Состояние</th><td>Завершены</td></tr>
<tr><th>Оценка</th><td>9,00 из 10,00 (90%)</td></tr>
</tbody></table>
<div class="que multichoice deferredfeedback correct" id="question-601-1">
  <div class="info"><div class="state">Верно</div><div class="grade">Баллов: 1,00/1,00</div></div>
</div>
<div class="que shortanswer deferredfeedback correct" id="question-602-2">
  <div class="info"><div class="state">Верно</div><div class="grade">Баллов: 1,00/1,00</div></div>
</div>
</body></html>`
}

func quizAttemptEmpty() string {
	return `<!DOCTYPE html><html><body><p>Попытка завершена, страница недоступна.</p></body></html>`
}

func quizAttemptEssay() string {
	return `<!DOCTYPE html><html><body>
<script>M.cfg = {"sesskey":"` + testSesskey + `"};</script>
<form id="responseform" method="post" action="/moodle/mod/quiz/processattempt.php?cmid=61422" enctype="multipart/form-data">
<input type="hidden" name="attempt" value="999">
<input type="hidden" name="thispage" value="5">
<input type="hidden" name="nextpage" value="-1">
<input type="hidden" name="slots" value="9">
<div class="que essay deferredfeedback notyetanswered" id="question-609-9">
  <div class="info"><input type="hidden" name="q609:9_:sequencecheck" value="1"></div>
  <div class="content">
    <div class="qtext"><p>Опишите своими словами.</p></div>
    <div class="answer"><textarea name="q609:9_answer"></textarea></div>
  </div>
</div>
</form></body></html>`
}

func quizSummaryHTML() string {
	return `<!DOCTYPE html><html><body>
<script>M.cfg = {"sesskey":"` + testSesskey + `"};</script>
<form method="post" action="/moodle/mod/quiz/processattempt.php">
<input type="hidden" name="attempt" value="999">
<input type="hidden" name="finishattempt" value="1">
<input type="hidden" name="timeup" value="0">
<input type="hidden" name="slots" value="">
<input type="hidden" name="cmid" value="61422">
<input type="submit" value="Отправить всё и завершить тест">
</form>
</body></html>`
}

func quizReviewHTML() string {
	return `<!DOCTYPE html><html><body>
<script>M.cfg = {"sesskey":"` + testSesskey + `"};</script>
<table class="generaltable quizreviewsummary"><tbody>
<tr><th>Состояние</th><td>Завершены</td></tr>
<tr><th>Оценка</th><td>9,00 из 10,00 (90%)</td></tr>
</tbody></table>
<div id="feedback">Хорошая работа!</div>
<div class="que multichoice deferredfeedback correct" id="question-601-1">
  <div class="info"><div class="state">Верно</div><div class="grade">Баллов: 1,00/1,00</div></div>
</div>
<div class="que shortanswer deferredfeedback correct" id="question-602-2">
  <div class="info"><div class="state">Верно</div><div class="grade">Баллов: 1,00/1,00</div></div>
</div>
</body></html>`
}
