package uni

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizlog"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

const fakeQuizViewHTML = `<!DOCTYPE html>
<html>
<head><title>Тест: Информационная энтропия</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_UNI_TEST"};</script>
<div class="quizinfo"><p>Разрешено попыток: 1</p></div>
<div class="quizstartbuttondiv">
  <form method="post" action="BASE/mod/quiz/startattempt.php">
    <input type="hidden" name="cmid" value="777">
    <input type="hidden" name="sesskey" value="SK_UNI_TEST">
    <input type="hidden" name="_qf__mod_quiz_form_preflight_check_form" value="1">
    <input type="submit" name="submitbutton" value="Начать попытку">
  </form>
</div>
<div id="feedback"><h3>Ваша итоговая оценка за этот тест: 5,00/5,00</h3></div>
<table class="generaltable quizreviewsummary">
  <tbody>
    <tr><td class="state">Завершены</td><td class="grade">5,00 из 5,00 (100%)</td></tr>
  </tbody>
</table>
</body>
</html>`

const fakeAttemptHTML = `<!DOCTYPE html>
<html>
<head><title>Попытка</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_UNI_TEST"};</script>
<form id="responseform" method="post" action="BASE/mod/quiz/processattempt.php?cmid=777" enctype="multipart/form-data">
  <input type="hidden" name="attempt" value="555">
  <input type="hidden" name="thispage" value="0">
  <input type="hidden" name="nextpage" value="-1">
  <input type="hidden" name="timeup" value="0">
  <input type="hidden" name="sesskey" value="SK_UNI_TEST">
  <input type="hidden" name="slots" value="1,2">

  <!-- Слот 1: multichoice radio -->
  <div class="que multichoice deferredfeedback notyetanswered" id="question-101-1">
    <div class="info">
      <input type="hidden" name="q101:1_:sequencecheck" value="1">
      <input type="hidden" name="q101:1_:flagged" value="0">
    </div>
    <div class="content">
      <div class="qtext"><p>Чему равна энтропия равновероятных событий?</p></div>
      <div class="answer">
        <div class="r0">
          <input type="radio" name="q101:1_answer" id="q101:1_answer0" value="0">
          <label for="q101:1_answer0"><span data-region="answer-label">Максимуму</span></label>
        </div>
        <div class="r1">
          <input type="radio" name="q101:1_answer" id="q101:1_answer1" value="1">
          <label for="q101:1_answer1"><span data-region="answer-label">Нулю</span></label>
        </div>
      </div>
    </div>
  </div>

  <!-- Слот 2: shortanswer -->
  <div class="que shortanswer deferredfeedback notyetanswered" id="question-102-2">
    <div class="info">
      <input type="hidden" name="q102:2_:sequencecheck" value="1">
      <input type="hidden" name="q102:2_:flagged" value="0">
    </div>
    <div class="content">
      <div class="qtext"><p>Назовите единицу измерения информации:</p></div>
      <div class="answer">
        <input type="text" name="q102:2_answer" id="q102:2_answer" value="">
      </div>
    </div>
  </div>
</form>
</body>
</html>`

const fakeSummaryHTML = `<!DOCTYPE html>
<html>
<head><title>Сводка</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_UNI_TEST"};</script>
<form method="post" action="BASE/mod/quiz/processattempt.php">
  <input type="hidden" name="attempt" value="555">
  <input type="hidden" name="finishattempt" value="1">
  <input type="hidden" name="timeup" value="0">
  <input type="hidden" name="slots" value="">
  <input type="hidden" name="sesskey" value="SK_UNI_TEST">
</form>
</body>
</html>`

const fakeReviewHTML = `<!DOCTYPE html>
<html>
<head><title>Обзор</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_UNI_TEST"};</script>
<table class="generaltable quizreviewsummary">
  <tbody>
    <tr><th>Состояние</th><td>Завершены</td></tr>
    <tr><th>Оценка</th><td>5,00 из 5,00 (100%)</td></tr>
  </tbody>
</table>
<div id="feedback">Отлично!</div>
</body>
</html>`

func TestQuizSolveWithLLM(t *testing.T) {
	// 1. Фейковый сервер LLM
	var llmCalls int
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCalls++
		body, _ := io.ReadAll(r.Body)
		bodyStr := string(body)
		if !strings.Contains(bodyStr, "энтропия") {
			t.Errorf("expected course context/question in prompt: %s", bodyStr)
		}

		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"content": `{
							"answers": [
								{"slot": 1, "choice": "0"},
								{"slot": 2, "text": "бит"}
							]
						}`,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer llmSrv.Close()

	// 2. Фейковый сервер Moodle
	var moodleSrv *httptest.Server
	mux := http.NewServeMux()

	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(fakeQuizViewHTML, "BASE", moodleSrv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777", http.StatusSeeOther)
	})

	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(fakeAttemptHTML, "BASE", moodleSrv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "" && strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			// Сабмит страницы попытки
			http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/summary.php?attempt=555&cmid=777", http.StatusSeeOther)
			return
		}
		// Завершение попытки finishattempt=1
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/review.php?attempt=555&cmid=777", http.StatusSeeOther)
	})

	mux.HandleFunc("/mod/quiz/summary.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(fakeSummaryHTML, "BASE", moodleSrv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	mux.HandleFunc("/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(fakeReviewHTML, "BASE", moodleSrv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	// 3. Настройка Syncer
	f := newFake(t)
	sy, st, sent := setup(t, f)
	sy.MoodleBase = moodleSrv.URL

	// Создаем тестовый аккаунт Moodle
	addAccount(t, sy, 10, model.AccountMoodle, "student", "pass")
	acc, err := st.GetAccount(10, model.AccountMoodle)
	if err != nil || acc == nil {
		t.Fatalf("get account: %v", err)
	}

	// Добавляем лекционный пак по курсу "Теория информации" с конспектом и транскриптом
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "notes.md"), []byte("Энтропия Шеннона максимальна при равновероятных состояниях."), 0644)
	os.WriteFile(filepath.Join(tmpDir, "transcript.txt"), []byte("Единицей измерения информации является бит."), 0644)

	pack, err := st.EnsurePack(model.Lesson{ID: 1, Discipline: "Теория информации", Date: "2026-11-14"}, "", tmpDir)
	if err != nil {
		t.Fatalf("ensure pack: %v", err)
	}
	pack.Dir = tmpDir
	pack.Status = model.PackDone
	if err := st.SavePack(pack); err != nil {
		t.Fatalf("save pack: %v", err)
	}

	// Задаем конфигурацию с LLM
	sy.Cfg = &config.Config{
		LLMAPIKey: "fake_key",
		LLMAPIURL: llmSrv.URL,
		LLMModel:  "fake_model",
	}

	// Элемент теста, который открылся (ранее не было в базе)
	quizItem := model.MoodleItem{
		CMID:     777,
		CourseID: 1862,
		Course:   "Теория информации (01.03.02)",
		Kind:     model.MoodleQuiz,
		Title:    "Тест: Информационная энтропия",
		URL:      moodleSrv.URL + "/mod/quiz/view.php?id=777",
		Status:   model.MoodleTodo,
		Todo:     "Разрешено попыток: 1",
	}

	// Запускаем синк
	cli, err := sy.moodleClient()
	if err != nil {
		t.Fatalf("moodleClient: %v", err)
	}
	ctx := context.Background()

	// В prev тест был закрыт ("Тест сейчас не открыт для попыток") -> теперь открылся
	prevItem := quizItem
	prevItem.Todo = "Тест сейчас не открыт для попыток"
	sy.detectAndHandleOpenedQuizzes(ctx, *acc, cli, []model.MoodleItem{prevItem}, []model.MoodleItem{quizItem})

	// Проверяем вызов LLM
	if llmCalls != 1 {
		t.Errorf("expected 1 LLM call, got %d", llmCalls)
	}

	// Проверяем отметку в базе
	solvedVal, ok, err := st.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", acc.TelegramID, quizItem.CMID))
	if err != nil || !ok || solvedVal == "" {
		t.Fatalf("expected quiz_solved setting set, ok=%v, val=%q, err=%v", ok, solvedVal, err)
	}

	// Проверяем уведомления пользователю
	if len(*sent) == 0 {
		t.Fatalf("expected notifications sent to user")
	}
	allNotifs := strings.Join(*sent, "\n---\n")
	if !strings.Contains(allNotifs, "автоматически решён") {
		t.Errorf("expected report notification about solved quiz, got: %s", allNotifs)
	}
	if !strings.Contains(allNotifs, "5,00") {
		t.Errorf("expected grade in notification, got: %s", allNotifs)
	}
}

func TestQuizParseWithoutLLMKey(t *testing.T) {
	var moodleSrv *httptest.Server
	mux := http.NewServeMux()

	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(fakeQuizViewHTML, "BASE", moodleSrv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777", http.StatusSeeOther)
	})

	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		html := strings.ReplaceAll(fakeAttemptHTML, "BASE", moodleSrv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	f := newFake(t)
	sy, st, sent := setup(t, f)
	sy.MoodleBase = moodleSrv.URL

	addAccount(t, sy, 20, model.AccountMoodle, "student", "pass")
	acc, _ := st.GetAccount(20, model.AccountMoodle)

	// Конфигурация БЕЗ LLM ключа
	sy.Cfg = &config.Config{
		LLMAPIKey: "",
	}

	quizItem := model.MoodleItem{
		CMID:     777,
		CourseID: 1862,
		Course:   "Теория информации",
		Kind:     model.MoodleQuiz,
		Title:    "Тест: Информационная энтропия",
		URL:      moodleSrv.URL + "/mod/quiz/view.php?id=777",
		Status:   model.MoodleTodo,
		Todo:     "Разрешено попыток: 1",
	}

	cli, _ := sy.moodleClient()
	ctx := context.Background()

	// Тест был ранее закрыт ("не открыт для попыток")
	prevItem := quizItem
	prevItem.Todo = "Тест сейчас не открыт для попыток"

	sy.detectAndHandleOpenedQuizzes(ctx, *acc, cli, []model.MoodleItem{prevItem}, []model.MoodleItem{quizItem})

	// Проверяем отметку quiz_parsed в базе
	parsedVal, ok, err := st.GetSetting(fmt.Sprintf("quiz_parsed:%d:%d", acc.TelegramID, quizItem.CMID))
	if err != nil || !ok || parsedVal == "" {
		t.Fatalf("expected quiz_parsed setting set, ok=%v, val=%q, err=%v", ok, parsedVal, err)
	}

	// Проверяем, что отправлен полный текст вопросов
	allNotifs := strings.Join(*sent, "\n---\n")
	if !strings.Contains(allNotifs, "Чему равна энтропия") || !strings.Contains(allNotifs, "Максимуму") {
		t.Errorf("expected parsed questions and choices in notification, got: %s", allNotifs)
	}

	// Повторный синк — не должен повторно слать то же самое!
	sentCountBefore := len(*sent)
	sy.detectAndHandleOpenedQuizzes(ctx, *acc, cli, []model.MoodleItem{quizItem}, []model.MoodleItem{quizItem})
	if len(*sent) != sentCountBefore {
		t.Errorf("expected no duplicate notification on repeated sync, before=%d, after=%d", sentCountBefore, len(*sent))
	}
}

func TestWakeKickReaction(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatalf("setupStore: %v", err)
	}

	if consumeWakeKick(st) {
		t.Errorf("expected no wake kick initially")
	}

	if err := WakeKick(st); err != nil {
		t.Fatalf("WakeKick: %v", err)
	}

	if !consumeWakeKick(st) {
		t.Errorf("expected wake kick consumed")
	}

	// Повторный вызов — флаг должен быть сброшен
	if consumeWakeKick(st) {
		t.Errorf("expected wake kick consumed and reset")
	}
}

func setupStore(t *testing.T) (*store.Store, error) {
	t.Helper()
	dir := t.TempDir()
	return store.Open(filepath.Join(dir, "bot.db"))
}

// ИИ не ответил: в Moodle ничего не уходит, попытка не завершается,
// человеку — «проблема с тестом», в журнале отладки — failed.
func TestQuizSolveLLMDownKeepsAttemptOpen(t *testing.T) {
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"overloaded"}`, http.StatusServiceUnavailable)
	}))
	defer llmSrv.Close()

	var moodleSrv *httptest.Server
	var processCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.ReplaceAll(fakeQuizViewHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.ReplaceAll(fakeAttemptHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		processCalls++
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/review.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	f := newFake(t)
	sy, st, sent := setup(t, f)
	sy.MoodleBase = moodleSrv.URL
	addAccount(t, sy, 10, model.AccountMoodle, "student", "pass")
	acc, _ := st.GetAccount(10, model.AccountMoodle)
	dbPath := filepath.Join(t.TempDir(), "bot.db")
	// без бесплатных ключей цепочка — один вариант LLM_* (фейк, всегда 503)
	sy.Cfg = &config.Config{LLMAPIKey: "k", LLMAPIURL: llmSrv.URL, LLMModel: "m", DBPath: dbPath}

	item := model.MoodleItem{CMID: 777, Course: "Теория информации", Kind: model.MoodleQuiz, Title: "Тест", Status: model.MoodleTodo,
		URL: moodleSrv.URL + "/mod/quiz/view.php?id=777"}
	prev := item
	prev.Todo = "Тест сейчас не открыт для попыток"
	cli, err := sy.moodleClient()
	if err != nil {
		t.Fatal(err)
	}
	sy.detectAndHandleOpenedQuizzes(context.Background(), *acc, cli, []model.MoodleItem{prev}, []model.MoodleItem{item})

	if processCalls != 0 {
		t.Fatalf("в Moodle ушло %d запросов processattempt, ждали 0", processCalls)
	}
	if v, ok, _ := st.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", acc.TelegramID, item.CMID)); ok && v != "" {
		t.Fatal("тест отмечен решённым")
	}
	if all := strings.Join(*sent, "\n"); !strings.Contains(all, "Проблема с тестом") {
		t.Fatalf("нет сообщения о проблеме: %s", all)
	}
	// следующий обычный обход (тест уже был открыт): пробуем снова, молча
	notes := len(*sent)
	sy.detectAndHandleOpenedQuizzes(context.Background(), *acc, cli, []model.MoodleItem{item}, []model.MoodleItem{item})
	if processCalls != 0 || len(*sent) != notes {
		t.Fatalf("повтор: processattempt=%d, новых сообщений %d", processCalls, len(*sent)-notes)
	}
	if v, _, _ := st.GetSetting(fmt.Sprintf("quiz_fail:%d:%d", acc.TelegramID, item.CMID)); v != "2" {
		t.Fatalf("счётчик неудач = %q, ждали 2", v)
	}
	runs, err := quizlog.List(quizlog.Dir(dbPath), 1)
	if err != nil || len(runs) != 1 || runs[0].Status != quizlog.Failed || len(runs[0].Pages) == 0 || len(runs[0].Pages[0].Attempts) != 1 {
		t.Fatalf("журнал: %+v %v", runs, err)
	}
}

// «Пройти» из панели: просроченный тест, который синк сам не трогает, решается.
func TestQuizForceOverdue(t *testing.T) {
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"answers\":[{\"slot\":1,\"choice\":\"0\"},{\"slot\":2,\"text\":\"бит\"}]}"}}]}`))
	}))
	defer llmSrv.Close()
	var moodleSrv *httptest.Server
	var finished bool
	mux := http.NewServeMux()
	page := func(tpl string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(strings.ReplaceAll(tpl, "BASE", moodleSrv.URL)))
		}
	}
	mux.HandleFunc("/mod/quiz/view.php", page(fakeQuizViewHTML))
	mux.HandleFunc("/mod/quiz/attempt.php", page(fakeAttemptHTML))
	mux.HandleFunc("/mod/quiz/summary.php", page(fakeSummaryHTML))
	mux.HandleFunc("/mod/quiz/review.php", page(fakeReviewHTML))
	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/summary.php?attempt=555&cmid=777", http.StatusSeeOther)
			return
		}
		finished = true
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/review.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	f := newFake(t)
	sy, st, sent := setup(t, f)
	sy.MoodleBase = moodleSrv.URL
	sy.Cfg = &config.Config{LLMAPIKey: "k", LLMAPIURL: llmSrv.URL, LLMModel: "m"}
	addAccount(t, sy, 10, model.AccountMoodle, "student", "pass")
	acc, _ := st.GetAccount(10, model.AccountMoodle)
	past := time.Now().Add(-48 * time.Hour)
	item := model.MoodleItem{CMID: 777, Course: "Теория информации", Kind: model.MoodleQuiz, Title: "Просроченный", Status: model.MoodleTodo, Due: &past}
	cli, _ := sy.moodleClient()

	// без флага — синк просроченный тест не трогает
	sy.detectAndHandleOpenedQuizzes(context.Background(), *acc, cli, []model.MoodleItem{item}, []model.MoodleItem{item})
	if finished {
		t.Fatal("просроченный тест решён без кнопки")
	}
	_ = st.SetSetting(QuizForceKey(10, 777), "1")
	sy.detectAndHandleOpenedQuizzes(context.Background(), *acc, cli, []model.MoodleItem{item}, []model.MoodleItem{item})
	if !finished {
		t.Fatalf("«Пройти» не довело до конца; сообщения: %v", *sent)
	}
	if v, _, _ := st.GetSetting(QuizForceKey(10, 777)); v != "" {
		t.Fatal("флаг «пройти» не снят")
	}
	if !strings.Contains(strings.Join(*sent, "\n"), "автоматически решён") {
		t.Fatalf("нет отчёта: %v", *sent)
	}
}

// Ответы не сохранились в Moodle — попытка не завершается, тест не отмечен решённым.
func TestQuizSubmitErrorKeepsAttemptOpen(t *testing.T) {
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"answers\":[{\"slot\":1,\"choice\":\"0\"},{\"slot\":2,\"text\":\"бит\"}]}"}}]}`))
	}))
	defer llmSrv.Close()
	var moodleSrv *httptest.Server
	var finished bool
	mux := http.NewServeMux()
	page := func(tpl string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(strings.ReplaceAll(tpl, "BASE", moodleSrv.URL)))
		}
	}
	mux.HandleFunc("/mod/quiz/view.php", page(fakeQuizViewHTML))
	mux.HandleFunc("/mod/quiz/attempt.php", page(fakeAttemptHTML))
	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		finished = true
	})
	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	f := newFake(t)
	sy, st, sent := setup(t, f)
	sy.MoodleBase = moodleSrv.URL
	sy.Cfg = &config.Config{LLMAPIKey: "k", LLMAPIURL: llmSrv.URL, LLMModel: "m"}
	addAccount(t, sy, 10, model.AccountMoodle, "student", "pass")
	acc, _ := st.GetAccount(10, model.AccountMoodle)
	item := model.MoodleItem{CMID: 777, Course: "Теория информации", Kind: model.MoodleQuiz, Title: "Тест", Status: model.MoodleTodo}
	prev := item
	prev.Todo = "Тест сейчас не открыт для попыток"
	cli, _ := sy.moodleClient()
	sy.detectAndHandleOpenedQuizzes(context.Background(), *acc, cli, []model.MoodleItem{prev}, []model.MoodleItem{item})

	if finished {
		t.Fatal("попытка завершена, хотя ответы не сохранились")
	}
	if v, ok, _ := st.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", acc.TelegramID, item.CMID)); ok && v != "" {
		t.Fatal("тест отмечен решённым")
	}
	all := strings.Join(*sent, "\n")
	if !strings.Contains(all, "Проблема с тестом") || !strings.Contains(all, "не сохранились") {
		t.Fatalf("нет внятного сообщения: %s", all)
	}
}
