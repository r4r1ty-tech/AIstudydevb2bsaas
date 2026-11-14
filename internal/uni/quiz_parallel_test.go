package uni

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizlog"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websearch"
)

func renderAttemptPageHTML(baseURL string, cmid, attemptID int64, page, nextPage, slot int, questionText, timeLeft string) string {
	timeHTML := ""
	if timeLeft != "" {
		timeHTML = fmt.Sprintf(`<div id="quiz-time-left">%s</div>`, timeLeft)
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Попытка</title></head>
<body>
<script>M.cfg = {"sesskey":"SK_TEST"};</script>
%s
<form id="responseform" method="post" action="%s/mod/quiz/processattempt.php?cmid=%d" enctype="multipart/form-data">
  <input type="hidden" name="attempt" value="%d">
  <input type="hidden" name="thispage" value="%d">
  <input type="hidden" name="nextpage" value="%d">
  <input type="hidden" name="timeup" value="0">
  <input type="hidden" name="sesskey" value="SK_TEST">
  <input type="hidden" name="slots" value="%d">

  <div class="que shortanswer deferredfeedback notyetanswered" id="question-10%d-%d">
    <div class="info">
      <input type="hidden" name="q10%d:%d_:sequencecheck" value="1">
      <input type="hidden" name="q10%d:%d_:flagged" value="0">
    </div>
    <div class="content">
      <div class="qtext"><p>%s</p></div>
      <div class="answer">
        <input type="text" name="q10%d:%d_answer" id="q10%d:%d_answer" value="">
      </div>
    </div>
  </div>
</form>
</body>
</html>`,
		timeHTML, baseURL, cmid, attemptID, page, nextPage, slot,
		slot, slot, slot, slot, slot, slot, questionText, slot, slot, slot, slot)
}

func TestParseTimeLeft(t *testing.T) {
	cases := []struct {
		raw    string
		want   time.Duration
		wantOK bool
	}{
		{"0:14:32", 14*time.Minute + 32*time.Second, true},
		{"1:05:09", time.Hour + 5*time.Minute + 9*time.Second, true},
		{"14:32", 14*time.Minute + 32*time.Second, true},
		{"Осталось времени 0:14:32", 14*time.Minute + 32*time.Second, true},
		{"Time left: 14:32", 14*time.Minute + 32*time.Second, true},
		{"12 мин 30 сек", 12*time.Minute + 30*time.Second, true},
		{"14 мин. 32 сек.", 14*time.Minute + 32*time.Second, true},
		{"1 ч 05 мин", time.Hour + 5*time.Minute, true},
		{"1 hour 5 mins", time.Hour + 5*time.Minute, true},
		{"2 HOURS 10 MINUTES 5 SECONDS", 2*time.Hour + 10*time.Minute + 5*time.Second, true},
		{"45 сек", 45 * time.Second, true},
		{"1 дн 2 ч", 26 * time.Hour, true},
		{"0:00", 0, true},
		{"", 0, false},
		{"   ", 0, false},
		{"Без ограничения времени", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseTimeLeft(tc.raw)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("parseTimeLeft(%q) = (%v, %v), want (%v, %v)", tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestParallelPagesAndIncrementalSubmit(t *testing.T) {
	releaseSlow := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSlow) }) }
	t.Cleanup(release)

	var activeLLM atomic.Int32
	var maxActiveLLM atomic.Int32

	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := activeLLM.Add(1)
		for {
			prev := maxActiveLLM.Load()
			if cur <= prev || maxActiveLLM.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer activeLLM.Add(-1)

		body, _ := io.ReadAll(r.Body)
		prompt := string(body)

		// Определяем номер слота по маркеру вопроса в промпте.
		slot := 0
		for s := 1; s <= 5; s++ {
			if strings.Contains(prompt, fmt.Sprintf("МаркерСтраницы%d", s)) {
				slot = s
				break
			}
		}
		if slot == 0 {
			http.Error(w, "unknown slot", http.StatusBadRequest)
			return
		}
		if slot == 3 {
			// Страница 2 ждёт явного сигнала, занимая только 1 слот KeyPool,
			// чтобы страницы 0 и 1 гарантированно могли пройти через 2-й слот KeyPool.
			select {
			case <-releaseSlow:
			case <-r.Context().Done():
				return
			case <-time.After(3 * time.Second):
			}
		} else if slot > 3 {
			select {
			case <-time.After(50 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}

		content, _ := json.Marshal(map[string]any{
			"answers": []map[string]any{
				{"slot": slot, "text": fmt.Sprintf("ans%d", slot)},
			},
		})
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": string(content)}},
			},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	defer llmSrv.Close()

	var (
		mu             sync.Mutex
		submittedOrder []int
		finished       bool
	)
	submittedCh := make(chan int, 10)

	var moodleSrv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeQuizViewHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777&page=0", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		next := p + 1
		if p == 4 {
			next = -1
		}
		qText := fmt.Sprintf("Вопрос МаркерСтраницы%d", p+1)
		html := renderAttemptPageHTML(moodleSrv.URL, 777, 555, p, next, p+1, qText, "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			_ = r.ParseMultipartForm(1 << 20)
			thisPage, _ := strconv.Atoi(r.FormValue("thispage"))
			mu.Lock()
			submittedOrder = append(submittedOrder, thisPage)
			mu.Unlock()
			submittedCh <- thisPage
			if thisPage == 4 {
				http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/summary.php?attempt=555&cmid=777", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("%s/mod/quiz/attempt.php?attempt=555&cmid=777&page=%d", moodleSrv.URL, thisPage+1), http.StatusSeeOther)
			return
		}
		mu.Lock()
		finished = true
		mu.Unlock()
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/review.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/summary.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeSummaryHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeReviewHTML, "BASE", moodleSrv.URL)))
	})
	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	f := newFake(t)
	sy, st, _ := setup(t, f)
	sy.MoodleBase = moodleSrv.URL
	sy.Cfg = &config.Config{LLMAPIKey: "k", LLMAPIURL: llmSrv.URL, LLMModel: "m", AdminID: 10}
	addAccount(t, sy, 10, model.AccountMoodle, "student", "pass")
	acc, _ := st.GetAccount(10, model.AccountMoodle)
	cli, err := sy.moodleClient()
	if err != nil {
		t.Fatal(err)
	}
	item := model.MoodleItem{CMID: 777, Course: "Информатика", Kind: model.MoodleQuiz, Title: "Тест 5 стр", Status: model.MoodleTodo}

	done := make(chan struct{})
	go func() {
		defer close(done)
		sy.processQuiz(context.Background(), *acc, cli, item, true)
	}()

	// Ждём отправки страниц 0 и 1 ДО разблокировки страниц 2..4.
	var early []int
	for len(early) < 2 {
		select {
		case p := <-submittedCh:
			early = append(early, p)
		case <-time.After(2 * time.Second):
			t.Fatalf("не дождались инкрементальной отправки страниц 0 и 1, пришло %v", early)
		}
	}
	if !reflect.DeepEqual(early, []int{0, 1}) {
		t.Fatalf("ранние отправки = %v, ожидали [0 1] до решения страниц 2..4", early)
	}

	// Разблокируем страницы 2..4 и ждём полного завершения.
	release()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("processQuiz не завершился вовремя")
	}

	mu.Lock()
	gotOrder := append([]int(nil), submittedOrder...)
	gotFinished := finished
	mu.Unlock()

	if !reflect.DeepEqual(gotOrder, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("порядок отправки страниц = %v, want [0 1 2 3 4]", gotOrder)
	}
	if !gotFinished {
		t.Fatal("FinishAttempt не вызван")
	}
	if maxActiveLLM.Load() < 2 {
		t.Fatalf("ожидали параллельное решение страниц (maxActiveLLM=%d < 2)", maxActiveLLM.Load())
	}
	if v, ok, _ := st.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", acc.TelegramID, item.CMID)); !ok || v == "" {
		t.Fatal("тест не отмечен решённым")
	}
}

func TestPartialProgressOnLaterPageError(t *testing.T) {
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		prompt := string(body)

		slot := 0
		for s := 1; s <= 4; s++ {
			if strings.Contains(prompt, fmt.Sprintf("МаркерОшибки%d", s)) {
				slot = s
				break
			}
		}
		// Страница 2 (слот 3) возвращает не-JSON -> падает после repair-повтора без сетевых бэкоффов.
		if slot == 3 {
			resp, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]string{"content": "не могу разобрать json"}},
				},
			})
			_, _ = w.Write(resp)
			return
		}
		if slot == 1 || slot == 2 {
			// Даём страницам 0 и 1 гарантированно успеть, хотя координатор и так ждёт 0 и 1 до проверки 2.
		} else if slot == 4 {
			time.Sleep(15 * time.Millisecond)
		}

		content, _ := json.Marshal(map[string]any{
			"answers": []map[string]any{
				{"slot": slot, "text": fmt.Sprintf("ok%d", slot)},
			},
		})
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": string(content)}},
			},
		})
		_, _ = w.Write(resp)
	}))
	defer llmSrv.Close()

	var (
		mu             sync.Mutex
		submittedOrder []int
		finished       bool
	)
	var moodleSrv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeQuizViewHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777&page=0", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		next := p + 1
		if p == 3 {
			next = -1
		}
		html := renderAttemptPageHTML(moodleSrv.URL, 777, 555, p, next, p+1, fmt.Sprintf("Вопрос МаркерОшибки%d", p+1), "")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			_ = r.ParseMultipartForm(1 << 20)
			thisPage, _ := strconv.Atoi(r.FormValue("thispage"))
			mu.Lock()
			submittedOrder = append(submittedOrder, thisPage)
			mu.Unlock()
			http.Redirect(w, r, fmt.Sprintf("%s/mod/quiz/attempt.php?attempt=555&cmid=777&page=%d", moodleSrv.URL, thisPage+1), http.StatusSeeOther)
			return
		}
		mu.Lock()
		finished = true
		mu.Unlock()
	})
	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	f := newFake(t)
	sy, st, sent := setup(t, f)
	sy.MoodleBase = moodleSrv.URL
	sy.Cfg = &config.Config{LLMAPIKey: "k", LLMAPIURL: llmSrv.URL, LLMModel: "m", AdminID: 10}
	addAccount(t, sy, 10, model.AccountMoodle, "student", "pass")
	acc, _ := st.GetAccount(10, model.AccountMoodle)
	cli, _ := sy.moodleClient()
	item := model.MoodleItem{CMID: 777, Course: "Информатика", Kind: model.MoodleQuiz, Title: "Тест 4 стр", Status: model.MoodleTodo}

	sy.processQuiz(context.Background(), *acc, cli, item, false)

	mu.Lock()
	gotOrder := append([]int(nil), submittedOrder...)
	gotFinished := finished
	mu.Unlock()

	if !reflect.DeepEqual(gotOrder, []int{0, 1}) {
		t.Fatalf("отправленные страницы = %v, ожидали [0 1]", gotOrder)
	}
	if gotFinished {
		t.Fatal("FinishAttempt НЕ должен вызываться при падении страницы 2")
	}
	if all := strings.Join(*sent, "\n"); !strings.Contains(all, "Часть ответов уже сохранена в Moodle") {
		t.Fatalf("ожидали уведомление о частичном сохранении, got: %s", all)
	}
}

func TestSequentialNavigationFallback(t *testing.T) {
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		prompt := string(body)
		slot := 1
		if strings.Contains(prompt, "ПослВопрос2") {
			slot = 2
		}
		content, _ := json.Marshal(map[string]any{
			"answers": []map[string]any{
				{"slot": slot, "text": fmt.Sprintf("seq%d", slot)},
			},
		})
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": string(content)}},
			},
		})
		_, _ = w.Write(resp)
	}))
	defer llmSrv.Close()

	var (
		mu             sync.Mutex
		currentPage    int
		submittedOrder []int
		finished       bool
	)
	var moodleSrv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeQuizViewHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777&page=0", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		reqPage, _ := strconv.Atoi(r.URL.Query().Get("page"))
		mu.Lock()
		cur := currentPage
		mu.Unlock()
		// В sequential navigation запрос будущей страницы редиректит на текущую.
		if reqPage != cur {
			http.Redirect(w, r, fmt.Sprintf("%s/mod/quiz/attempt.php?attempt=555&cmid=777&page=%d", moodleSrv.URL, cur), http.StatusSeeOther)
			return
		}
		next := -1
		if cur == 0 {
			next = 1
		}
		html := renderAttemptPageHTML(moodleSrv.URL, 777, 555, cur, next, cur+1, fmt.Sprintf("ПослВопрос%d", cur+1), "")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			_ = r.ParseMultipartForm(1 << 20)
			thisPage, _ := strconv.Atoi(r.FormValue("thispage"))
			mu.Lock()
			submittedOrder = append(submittedOrder, thisPage)
			if thisPage == 0 {
				currentPage = 1
			}
			mu.Unlock()
			if thisPage == 1 {
				http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/summary.php?attempt=555&cmid=777", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777&page=1", http.StatusSeeOther)
			return
		}
		mu.Lock()
		finished = true
		mu.Unlock()
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/review.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/summary.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeSummaryHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeReviewHTML, "BASE", moodleSrv.URL)))
	})
	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	f := newFake(t)
	sy, st, sent := setup(t, f)
	sy.MoodleBase = moodleSrv.URL
	sy.Cfg = &config.Config{LLMAPIKey: "k", LLMAPIURL: llmSrv.URL, LLMModel: "m", AdminID: 10}
	addAccount(t, sy, 10, model.AccountMoodle, "student", "pass")
	acc, _ := st.GetAccount(10, model.AccountMoodle)
	cli, _ := sy.moodleClient()
	item := model.MoodleItem{CMID: 777, Course: "Информатика", Kind: model.MoodleQuiz, Title: "Последовательный тест", Status: model.MoodleTodo}

	sy.processQuiz(context.Background(), *acc, cli, item, false)

	mu.Lock()
	gotOrder := append([]int(nil), submittedOrder...)
	gotFinished := finished
	mu.Unlock()

	if !reflect.DeepEqual(gotOrder, []int{0, 1}) {
		t.Fatalf("последовательная отправка = %v, want [0 1]", gotOrder)
	}
	if !gotFinished {
		t.Fatal("FinishAttempt не вызван в sequential navigation")
	}
	all := strings.Join(*sent, "\n")
	if !strings.Contains(all, "seq1") || !strings.Contains(all, "seq2") {
		t.Fatalf("в отчёте нет ответов обеих страниц: %s", all)
	}
}

func TestTimeBudgetTriggersFastMode(t *testing.T) {
	// Случай 1: Moodle прислал TimeLeft = "2 мин 00 сек" (<= 3 мин) -> fast mode через processQuiz.
	// Модель на первом раунде возвращает ответ с confidence=0.1 и просьбой search,
	// а websearch включён. При fast mode веб-поиск и 2-й раунд НЕ запускаются.
	var llmCalls atomic.Int32
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCalls.Add(1)
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{
					"content": `{"answers":[{"slot":1,"text":"42","confidence":0.1,"search":"найти 42"}]}`,
				}},
			},
		})
		_, _ = w.Write(resp)
	}))
	defer llmSrv.Close()

	var finished atomic.Bool
	var moodleSrv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeQuizViewHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		html := renderAttemptPageHTML(moodleSrv.URL, 777, 555, 0, -1, 1, "Быстрый вопрос по алгоритмам?", "Осталось времени 2 мин 00 сек")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/summary.php?attempt=555&cmid=777", http.StatusSeeOther)
			return
		}
		finished.Store(true)
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/review.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/summary.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeSummaryHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeReviewHTML, "BASE", moodleSrv.URL)))
	})
	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	fw := &fakeWeb{}
	f := newFake(t)
	sy, st, _ := setup(t, f)
	sy.MoodleBase = moodleSrv.URL
	sy.Web = &websearch.Searcher{Providers: []websearch.Provider{fw}}
	sy.Cfg = &config.Config{LLMAPIKey: "k", LLMAPIURL: llmSrv.URL, LLMModel: "m", AdminID: 10}
	addAccount(t, sy, 10, model.AccountMoodle, "student", "pass")
	acc, _ := st.GetAccount(10, model.AccountMoodle)
	cli, _ := sy.moodleClient()
	item := model.MoodleItem{CMID: 777, Course: "Алгоритмы", Kind: model.MoodleQuiz, Title: "Короткий таймер", Status: model.MoodleTodo}

	sy.processQuiz(context.Background(), *acc, cli, item, false)

	if !finished.Load() {
		t.Fatal("тест с малым таймером должен был завершиться")
	}
	if llmCalls.Load() != 1 {
		t.Fatalf("ожидали 1 вызов LLM без раунда уточнений, было %d", llmCalls.Load())
	}
	if len(fw.queries) != 0 {
		t.Fatalf("веб-поиск не должен вызываться в быстром режиме, было %v", fw.queries)
	}

	// Случай 2: solvePageWithOpts с дедлайном < 60s автоматически включает быстрый режим,
	// даже если FastMode: false и модель отказывается (не уходит на доп. раунды).
	llmCalls.Store(0)
	fw2 := &fakeWeb{}
	sy2 := &Syncer{Web: &websearch.Searcher{Providers: []websearch.Provider{fw2}}}
	srv2, _ := fakeLLM(t, `{"answers":[{"slot":1,"choice":"0"},{"slot":2,"text":"невозможно решить","search":"доп поиск"}]}`)
	_, _, _, err := sy2.solvePageWithOpts(
		context.Background(),
		quizTag{TG: 1, CMID: 2},
		nil,
		sy2.webSearcher(),
		chainFor(srv2),
		"Сети",
		"",
		tcpQuestions(),
		solvePageOpts{Deadline: time.Now().Add(30 * time.Second)},
	)
	if err == nil {
		t.Fatal("ожидали ошибку валидации отказа")
	}
	if len(fw2.queries) != 0 {
		t.Fatalf("веб-поиск при остатке <60s не должен вызываться: %v", fw2.queries)
	}
}

func TestDescribeQuestionImagesParallel(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	sy := &Syncer{
		DescribeImage: func(_ context.Context, src string) (string, error) {
			cur := active.Add(1)
			for {
				prev := maxActive.Load()
				if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(35 * time.Millisecond)
			active.Add(-1)
			return "desc-" + src, nil
		},
	}

	qs := []moodle.QuizQuestion{
		{Slot: 1, Text: "Q1", Images: []string{"img1", "img2"}},
		{Slot: 2, Text: "Q2", Images: []string{"img3", "img4"}},
	}
	var pl quizlog.Page
	out := sy.describeQuestionImages(context.Background(), quizTag{TG: 1, CMID: 2, Page: 0}, nil, qs, &pl)

	if maxActive.Load() < 2 {
		t.Fatalf("картинки должны описываться параллельно (maxActive=%d < 2)", maxActive.Load())
	}
	wantQ1 := "Q1\nРисунок 1:\ndesc-img1\nРисунок 2:\ndesc-img2"
	wantQ2 := "Q2\nРисунок 1:\ndesc-img3\nРисунок 2:\ndesc-img4"
	if out[0].Text != wantQ1 {
		t.Fatalf("Q1 text = %q, want %q", out[0].Text, wantQ1)
	}
	if out[1].Text != wantQ2 {
		t.Fatalf("Q2 text = %q, want %q", out[1].Text, wantQ2)
	}
	wantNotes := []string{
		"вопрос 1, рисунок 1 распознан (9 симв.)",
		"вопрос 1, рисунок 2 распознан (9 симв.)",
		"вопрос 2, рисунок 1 распознан (9 симв.)",
		"вопрос 2, рисунок 2 распознан (9 симв.)",
	}
	if !reflect.DeepEqual(pl.Notes, wantNotes) {
		t.Fatalf("pl.Notes = %v, want %v", pl.Notes, wantNotes)
	}
}

func TestCollectCourseMaterialsUsesRecordingsDirAndUTF8(t *testing.T) {
	st, err := setupStore(t)
	if err != nil {
		t.Fatalf("setupStore: %v", err)
	}
	recDir := t.TempDir()
	relDir := filepath.Join("Матан", "лекция-1")
	fullDir := filepath.Join(recDir, relDir)
	if err := os.MkdirAll(fullDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fullDir, "notes.md"), []byte("Предел последовательности по Коши."), 0o644); err != nil {
		t.Fatal(err)
	}
	// Более 32000 кириллических символов (>64000 байт), чтобы проверить обрезку по рунам.
	longTrans := strings.Repeat("Б", 35000)
	if err := os.WriteFile(filepath.Join(fullDir, "transcript.txt"), []byte(longTrans), 0o644); err != nil {
		t.Fatal(err)
	}

	pack, err := st.EnsurePack(model.Lesson{ID: 1, Discipline: "Матан", Date: "2026-11-14"}, "", relDir)
	if err != nil {
		t.Fatal(err)
	}
	pack.Dir = relDir
	pack.Status = model.PackDone
	if err := st.SavePack(pack); err != nil {
		t.Fatal(err)
	}

	sy := &Syncer{
		Store: st,
		Cfg:   &config.Config{RecordingsDir: recDir},
	}
	got := sy.collectCourseMaterials(quizTag{TG: 1, CMID: 2}, "Матан (1 курс)")
	if !strings.Contains(got, "Предел последовательности по Коши.") {
		t.Fatalf("материалы из RecordingsDir не прочитаны: %.200s", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("после обрезки материалов получился невалидный UTF-8")
	}
	if n := utf8.RuneCountInString(got); n != 32000 {
		t.Fatalf("длина материалов = %d рун, ожидали 32000 (обрезка по рунам, а не байтам)", n)
	}
}

func TestOpenPageSSRFRedirectBlocked(t *testing.T) {
	var secretHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "http://"+r.Host+"/secret", http.StatusFound)
		case "/redir-loopback":
			// Явный редирект на 127.0.0.1
			http.Redirect(w, r, "http://127.0.0.1:"+strconv.Itoa(r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr).Port)+"/secret", http.StatusFound)
		case "/redir-localhost":
			http.Redirect(w, r, "http://localhost/secret", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/secret":
			secretHits.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("SECRET_DATA"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ctx := context.Background()

	// 1. Прямые запросы к внутренним адресам блокируются сразу.
	for _, blocked := range []string{
		srv.URL + "/secret",
		"http://localhost/secret",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/admin",
		"http://[::1]/secret",
		"file:///etc/passwd",
	} {
		if _, err := openPage(ctx, blocked); err == nil {
			t.Errorf("openPage(%q) должен быть заблокирован", blocked)
		}
	}

	// 2. Редирект с "публичного" хоста на 127.0.0.1 / localhost / >5 редиректов блокируется CheckRedirect.
	oldDialer := openPageDialer
	oldLookup := openPageLookupIP
	srvAddr := srv.Listener.Addr().String()
	openPageDialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		if host == "public.example.test" {
			return (&net.Dialer{}).DialContext(ctx, network, srvAddr)
		}
		return safeDialContext(ctx, network, addr)
	}
	openPageLookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if host == "public.example.test" {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}
		if host == "rebind.example.test" {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return nil, fmt.Errorf("unexpected DNS lookup in test: %s", host)
	}
	defer func() {
		openPageDialer = oldDialer
		openPageLookupIP = oldLookup
	}()

	if _, err := openPage(ctx, "http://public.example.test/redir-loopback"); err == nil {
		t.Fatal("редирект на 127.0.0.1 должен блокироваться")
	}
	if _, err := openPage(ctx, "http://public.example.test/redir-localhost"); err == nil {
		t.Fatal("редирект на localhost должен блокироваться")
	}
	if _, err := openPage(ctx, "http://public.example.test/loop"); err == nil || !strings.Contains(err.Error(), "редирект") {
		t.Fatalf("ожидали ошибку лимита редиректов, got: %v", err)
	}
	if _, err := safeDialContext(ctx, "tcp", "rebind.example.test:80"); err == nil {
		t.Fatal("DNS-резолв на 127.0.0.1 должен блокироваться в safeDialContext")
	}
	if secretHits.Load() != 0 {
		t.Fatalf("/secret не должен быть достигнут, hits=%d", secretHits.Load())
	}
}

func TestProcessQuizPreventsConcurrentSameQuiz(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	var attemptStarts atomic.Int32
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"answers\":[{\"slot\":1,\"text\":\"ok\"}]}"}}]}`))
	}))
	defer llmSrv.Close()

	var moodleSrv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mod/quiz/view.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeQuizViewHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/startattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if attemptStarts.Add(1) == 1 {
			close(started)
			<-release
		}
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(renderAttemptPageHTML(moodleSrv.URL, 777, 555, 0, -1, 1, "Q1", "")))
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/summary.php?attempt=555&cmid=777", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/review.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/summary.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeSummaryHTML, "BASE", moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/review.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeReviewHTML, "BASE", moodleSrv.URL)))
	})
	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	f := newFake(t)
	sy, st, _ := setup(t, f)
	sy.MoodleBase = moodleSrv.URL
	sy.Cfg = &config.Config{LLMAPIKey: "k", LLMAPIURL: llmSrv.URL, LLMModel: "m", AdminID: 10}
	addAccount(t, sy, 10, model.AccountMoodle, "student", "pass")
	acc, _ := st.GetAccount(10, model.AccountMoodle)
	cli, _ := sy.moodleClient()
	item := model.MoodleItem{CMID: 777, Course: "Курс", Kind: model.MoodleQuiz, Title: "Тест", Status: model.MoodleTodo}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sy.processQuiz(context.Background(), *acc, cli, item, false)
	}()

	<-started
	// Второй запуск того же теста того же студента должен мгновенно выйти из-за TryLockQuiz.
	sy.processQuiz(context.Background(), *acc, cli, item, false)
	close(release)
	wg.Wait()

	if attemptStarts.Load() != 1 {
		t.Fatalf("ожидали 1 запуск попытки благодаря TryLockQuiz, было %d", attemptStarts.Load())
	}
	if v, ok, _ := st.GetSetting(fmt.Sprintf("quiz_solved:%d:%d", acc.TelegramID, item.CMID)); !ok || v == "" {
		t.Fatal("ожидали завершение первого запуска теста")
	}
}

func TestSubmitPageAnswersAllCheckSlotsAndError(t *testing.T) {
	var (
		mu         sync.Mutex
		checksSeen []string
		failSlot2  bool
	)
	var moodleSrv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/my/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<script>M.cfg = {"sesskey":"SK_TEST"};</script>`))
	})
	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(`<!DOCTYPE html>
<html><body><script>M.cfg={"sesskey":"SK_TEST"};</script>
<form id="responseform" method="post" action="%s/mod/quiz/processattempt.php?cmid=777" enctype="multipart/form-data">
  <input type="hidden" name="attempt" value="555">
  <input type="hidden" name="thispage" value="0">
  <input type="hidden" name="nextpage" value="-1">
  <input type="hidden" name="slots" value="1,2">
  <div class="que shortanswer adaptive" id="question-10-1">
    <input type="hidden" name="q10:1_:sequencecheck" value="1">
    <div class="qtext"><p>Q1</p></div>
    <div class="answer"><input type="text" name="q10:1_answer" value=""></div>
    <input type="submit" name="q10:1_-submit" value="1">
  </div>
  <div class="que shortanswer adaptive" id="question-10-2">
    <input type="hidden" name="q10:2_:sequencecheck" value="1">
    <div class="qtext"><p>Q2</p></div>
    <div class="answer"><input type="text" name="q10:2_answer" value=""></div>
    <input type="submit" name="q10:2_-submit" value="1">
  </div>
</form></body></html>`, moodleSrv.URL)))
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		isCheck := false
		mu.Lock()
		for k := range r.MultipartForm.Value {
			if strings.HasSuffix(k, "_-submit") {
				isCheck = true
				checksSeen = append(checksSeen, k)
				if failSlot2 && strings.Contains(k, ":2_-submit") {
					mu.Unlock()
					http.Error(w, "check error", http.StatusInternalServerError)
					return
				}
			}
		}
		mu.Unlock()
		if isCheck {
			http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, moodleSrv.URL+"/mod/quiz/summary.php?attempt=555&cmid=777", http.StatusSeeOther)
	})
	mux.HandleFunc("/mod/quiz/summary.php", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(fakeSummaryHTML, "BASE", moodleSrv.URL)))
	})
	moodleSrv = httptest.NewServer(mux)
	defer moodleSrv.Close()

	c, err := moodle.New(moodleSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	page := &moodle.AttemptPage{
		CMID:      777,
		AttemptID: 555,
		Page:      0,
		NextPage:  -1,
		Slots:     []int{1, 2},
		Questions: []moodle.QuizQuestion{
			{Slot: 1, QubaID: 10, InputName: "q10:1_answer", HasSubmitButton: true, SubmitButtonName: "q10:1_-submit", SubmitButtonValue: "1"},
			{Slot: 2, QubaID: 10, InputName: "q10:2_answer", HasSubmitButton: true, SubmitButtonName: "q10:2_-submit", SubmitButtonValue: "1"},
		},
	}
	answers := map[int]moodle.QuizAnswer{
		1: {Text: "a"},
		2: {Text: "b"},
	}
	if _, err := submitPageAnswers(context.Background(), quizTag{TG: 1, CMID: 777}, c, page, answers); err != nil {
		t.Fatalf("submitPageAnswers: %v", err)
	}
	mu.Lock()
	gotChecks := append([]string(nil), checksSeen...)
	checksSeen = nil
	failSlot2 = true
	mu.Unlock()

	if !reflect.DeepEqual(gotChecks, []string{"q10:1_-submit", "q10:2_-submit"}) {
		t.Fatalf("проверки слотов = %v, ожидали оба слота", gotChecks)
	}

	if _, err := submitPageAnswers(context.Background(), quizTag{TG: 1, CMID: 777}, c, page, answers); err == nil {
		t.Fatal("ожидали ошибку при падении проверки слота 2")
	}
}

func TestOpenPageCaseInsensitiveContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/plain":
			w.Header().Set("Content-Type", "TEXT/PLAIN; charset=utf-8")
			_, _ = w.Write([]byte("Простой текст ответа"))
		case "/html":
			w.Header().Set("Content-Type", "Text/HTML; charset=utf-8")
			_, _ = w.Write([]byte("<html><body><p>HTML текст ответа</p></body></html>"))
		}
	}))
	defer srv.Close()

	oldDialer := openPageDialer
	oldLookup := openPageLookupIP
	srvHost := strings.TrimPrefix(srv.URL, "http://")
	openPageDialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srvHost)
	}
	openPageLookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	t.Cleanup(func() {
		openPageDialer = oldDialer
		openPageLookupIP = oldLookup
	})

	txt, err := openPage(context.Background(), "http://public.example.test/plain")
	if err != nil || txt != "Простой текст ответа" {
		t.Fatalf("TEXT/PLAIN: txt=%q err=%v", txt, err)
	}
	htm, err := openPage(context.Background(), "http://public.example.test/html")
	if err != nil || htm != "HTML текст ответа" {
		t.Fatalf("Text/HTML: htm=%q err=%v", htm, err)
	}
}

func TestSolveAndSubmitFreeNavDrainsRemainingPagesOnEarlyFail(t *testing.T) {
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		prompt := string(body)
		if strings.Contains(prompt, "Стр0") {
			time.Sleep(80 * time.Millisecond)
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		slot := 2
		if strings.Contains(prompt, "Стр2") {
			slot = 3
		}
		content, _ := json.Marshal(map[string]any{
			"answers": []map[string]any{{"slot": slot, "text": "ok"}},
		})
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": string(content)}}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	defer llmSrv.Close()

	c, _ := moodle.New("https://moodle.example.invalid")
	pages := []*moodle.AttemptPage{
		{Page: 0, Questions: []moodle.QuizQuestion{{Slot: 1, QType: "shortanswer", Text: "Стр0"}}},
		{Page: 1, Questions: []moodle.QuizQuestion{{Slot: 2, QType: "shortanswer", Text: "Стр1"}}},
		{Page: 2, Questions: []moodle.QuizQuestion{{Slot: 3, QType: "shortanswer", Text: "Стр2"}}},
	}
	chain := chainFor(llmSrv)
	sy := &Syncer{}
	solveCtx, solveCancel := context.WithCancel(context.Background())
	defer solveCancel()

	run := &quizlog.Run{}
	_, _, _, err := sy.solveAndSubmitFreeNav(
		context.Background(), solveCtx, solveCancel,
		quizTag{TG: 1, CMID: 10}, c, nil, chain, "Курс", "",
		pages, true, time.Time{}, run,
	)
	if err == nil {
		t.Fatal("ожидалась ошибка при падении страницы 0")
	}
	if len(run.Pages) != len(pages) {
		t.Fatalf("ожидались все %d страниц в run.Pages после дренирования resultsCh, получено %d: %+v",
			len(pages), len(run.Pages), run.Pages)
	}
}
