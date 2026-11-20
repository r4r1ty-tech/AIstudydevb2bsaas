package uni

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

// adaptiveStub — заглушка Moodle с мгновенной проверкой: состояние вопроса
// зависит от отправленного ответа, как в behaviour=adaptive.
type adaptiveStub struct {
	srv       *httptest.Server
	buttons   bool
	failCheck bool
	slots     []int

	mu      sync.Mutex
	correct map[int]string
	values  map[int]string
	states  map[int]string
	checks  []string
	submits int
}

func newAdaptiveStub(t *testing.T, buttons bool, slots ...int) *adaptiveStub {
	t.Helper()
	s := &adaptiveStub{
		buttons: buttons,
		slots:   slots,
		correct: map[int]string{},
		values:  map[int]string{},
		states:  map[int]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/my/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<script>M.cfg = {"sesskey":"SK_TEST"};</script>`))
	})
	mux.HandleFunc("/mod/quiz/attempt.php", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		html := s.pageHTML()
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/mod/quiz/processattempt.php", s.handlePost)
	mux.HandleFunc("/mod/quiz/summary.php", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h2>Итог</h2></body></html>`))
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *adaptiveStub) pageHTML() string {
	states := make([]string, len(s.slots))
	for i, slot := range s.slots {
		st := s.states[slot]
		if st == "" {
			st = "notyetanswered"
		}
		states[i] = st
	}
	var b strings.Builder
	b.WriteString(`<html><body><form id="responseform" method="post">`)
	b.WriteString(`<input type="hidden" name="attempt" value="555">`)
	b.WriteString(`<input type="hidden" name="thispage" value="0">`)
	b.WriteString(`<input type="hidden" name="nextpage" value="0">`)
	slotStrs := make([]string, len(s.slots))
	for i, slot := range s.slots {
		slotStrs[i] = fmt.Sprint(slot)
	}
	fmt.Fprintf(&b, `<input type="hidden" name="slots" value="%s">`, strings.Join(slotStrs, ","))
	for i, slot := range s.slots {
		fmt.Fprintf(&b, `<div class="que shortanswer adaptive %s" id="question-10-%d">`, states[i], slot)
		fmt.Fprintf(&b, `<input type="hidden" name="q10:%d_:sequencecheck" value="1">`, slot)
		fmt.Fprintf(&b, `<div class="qtext"><p>Вопрос %d</p></div>`, slot)
		fmt.Fprintf(&b, `<div class="answer"><input type="text" name="q10:%d_answer" value="%s"></div>`,
			slot, s.values[slot])
		if s.buttons {
			fmt.Fprintf(&b, `<input type="submit" name="q10:%d_-submit" value="1">`, slot)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</form></body></html>`)
	return b.String()
}

func (s *adaptiveStub) handlePost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(1 << 20)
	checkSlot := 0
	for name := range r.MultipartForm.Value {
		var quba, slot int
		if _, err := fmt.Sscanf(name, "q%d:%d_-submit", &quba, &slot); err == nil {
			checkSlot = slot
		}
	}
	if checkSlot > 0 && s.failCheck {
		http.Error(w, "check error", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, slot := range s.slots {
		if v := r.MultipartForm.Value[fmt.Sprintf("q10:%d_answer", slot)]; len(v) > 0 {
			s.values[slot] = v[0]
		}
	}
	if checkSlot > 0 {
		v := s.values[checkSlot]
		s.checks = append(s.checks, fmt.Sprintf("%d=%s", checkSlot, v))
		if v == s.correct[checkSlot] {
			s.states[checkSlot] = "correct"
		} else {
			s.states[checkSlot] = "incorrect"
		}
		http.Redirect(w, r, s.srv.URL+"/mod/quiz/attempt.php?attempt=555&cmid=777", http.StatusSeeOther)
		return
	}
	s.submits++
	http.Redirect(w, r, s.srv.URL+"/mod/quiz/summary.php?attempt=555&cmid=777", http.StatusSeeOther)
}

func (s *adaptiveStub) lockApply(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

func (s *adaptiveStub) checkLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.checks...)
}

func (s *adaptiveStub) page() *moodle.AttemptPage {
	slots := append([]int(nil), s.slots...)
	sort.Ints(slots)
	p := &moodle.AttemptPage{CMID: 777, AttemptID: 555, Page: 0, NextPage: 0, Slots: slots}
	for _, slot := range slots {
		p.Questions = append(p.Questions, moodle.QuizQuestion{
			Slot:              slot,
			QubaID:            10,
			QType:             "shortanswer",
			Behaviour:         "adaptive",
			State:             "notyetanswered",
			SequenceCheck:     1,
			InputName:         fmt.Sprintf("q10:%d_answer", slot),
			HasSubmitButton:   s.buttons,
			SubmitButtonName:  fmt.Sprintf("q10:%d_-submit", slot),
			SubmitButtonValue: "1",
		})
	}
	return p
}

func (s *adaptiveStub) client(t *testing.T) *moodle.Client {
	t.Helper()
	cli, err := moodle.New(s.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

// Решение adaptive: неверный ответ переспрашивается с вердиктом Moodle,
// исправленный уходит в финальной отправке.
func TestSubmitPageVerifiedFixesWrongAnswer(t *testing.T) {
	st := newAdaptiveStub(t, true, 1)
	st.lockApply(func() { st.correct[1] = "right" })
	ai := newFakeAISeq(t, `{"answers":[{"slot":1,"text":"right"}]}`)
	store, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	sy, chain := aiSyncer(t, store, ai)
	answers := map[int]moodle.QuizAnswer{1: {Text: "wrong"}}

	_, got, err := sy.submitPageVerified(context.Background(), context.Background(),
		quizTag{TG: 1, CMID: 777}, st.client(t), nil, chain, "Курс", "",
		st.page(), answers, solvePageOpts{Budget: 5 * time.Minute})
	if err != nil {
		t.Fatalf("submitPageVerified: %v", err)
	}
	if n := ai.calls.Load(); n != 1 {
		t.Errorf("запросов к ИИ = %d, ожидали 1 переспрос", n)
	}
	if p := ai.prompts(); !strings.Contains(p, "Прошлый ответ не принят: Moodle отметил его неверным") {
		t.Errorf("в промпте нет обратной связи Moodle:\n%s", p)
	}
	if checks := st.checkLog(); len(checks) != 2 || checks[0] != "1=wrong" || checks[1] != "1=right" {
		t.Errorf("проверки = %v, ожидали [1=wrong 1=right]", checks)
	}
	if got[1].Text != "right" {
		t.Errorf("итоговый ответ = %q, ожидали right", got[1].Text)
	}
	st.mu.Lock()
	final, submits := st.values[1], st.submits
	st.mu.Unlock()
	if final != "right" || submits != 1 {
		t.Errorf("финальная отправка: значение %q, отправок %d; ожидали right и 1", final, submits)
	}
}

// Верный ответ не переспрашивается: ИИ не вызывается вовсе.
func TestSubmitPageVerifiedKeepsCorrectAnswer(t *testing.T) {
	st := newAdaptiveStub(t, true, 1, 2)
	st.lockApply(func() {
		st.correct[1] = "one"
		st.correct[2] = "two"
	})
	ai := newFakeAISeq(t, `{"answers":[]}`)
	store, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	sy, chain := aiSyncer(t, store, ai)
	answers := map[int]moodle.QuizAnswer{1: {Text: "one"}, 2: {Text: "two"}}

	_, _, err = sy.submitPageVerified(context.Background(), context.Background(),
		quizTag{TG: 1, CMID: 777}, st.client(t), nil, chain, "Курс", "",
		st.page(), answers, solvePageOpts{Budget: 5 * time.Minute})
	if err != nil {
		t.Fatalf("submitPageVerified: %v", err)
	}
	if n := ai.calls.Load(); n != 0 {
		t.Errorf("запросов к ИИ = %d, ожидали 0", n)
	}
	if checks := st.checkLog(); len(checks) != 2 {
		t.Errorf("проверок = %v, ожидали по одной на верный слот", checks)
	}
}

// Не больше двух раундов проверки: после второго неверного вердикта правки нет.
func TestSubmitPageVerifiedRoundLimit(t *testing.T) {
	st := newAdaptiveStub(t, true, 1)
	st.lockApply(func() { st.correct[1] = "right" })
	ai := newFakeAISeq(t, `{"answers":[{"slot":1,"text":"wrong2"}]}`)
	store, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	sy, chain := aiSyncer(t, store, ai)
	answers := map[int]moodle.QuizAnswer{1: {Text: "wrong"}}

	_, got, err := sy.submitPageVerified(context.Background(), context.Background(),
		quizTag{TG: 1, CMID: 777}, st.client(t), nil, chain, "Курс", "",
		st.page(), answers, solvePageOpts{Budget: 5 * time.Minute})
	if err != nil {
		t.Fatalf("submitPageVerified: %v", err)
	}
	if checks := st.checkLog(); len(checks) != quizCheckRounds {
		t.Errorf("проверок = %v, ожидали %d раунда", checks, quizCheckRounds)
	}
	if n := ai.calls.Load(); n != 1 {
		t.Errorf("запросов к ИИ = %d, ожидали 1 (правка только между раундами)", n)
	}
	st.mu.Lock()
	final := st.values[1]
	st.mu.Unlock()
	if final != "wrong2" || got[1].Text != "wrong2" {
		t.Errorf("отправлено %q, ответ %q; ожидали wrong2 как есть", final, got[1].Text)
	}
}

// Без кнопок проверки поведение прежнее: одна отправка и переход дальше.
func TestSubmitPageVerifiedWithoutCheckButtons(t *testing.T) {
	st := newAdaptiveStub(t, false, 1, 2)
	ai := newFakeAISeq(t, `{"answers":[]}`)
	store, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	sy, chain := aiSyncer(t, store, ai)
	answers := map[int]moodle.QuizAnswer{1: {Text: "a"}, 2: {Text: "b"}}

	updated, got, err := sy.submitPageVerified(context.Background(), context.Background(),
		quizTag{TG: 1, CMID: 777}, st.client(t), nil, chain, "Курс", "",
		st.page(), answers, solvePageOpts{Budget: 5 * time.Minute})
	if err != nil {
		t.Fatalf("submitPageVerified: %v", err)
	}
	if checks := st.checkLog(); len(checks) != 0 {
		t.Errorf("проверок = %v, ожидали ни одной", checks)
	}
	st.mu.Lock()
	submits := st.submits
	st.mu.Unlock()
	if submits != 1 {
		t.Errorf("отправок = %d, ожидали 1", submits)
	}
	if updated == nil || updated.Page != -1 {
		t.Errorf("страница после отправки = %+v, ожидали переход на summary", updated)
	}
	if got[1].Text != "a" || got[2].Text != "b" {
		t.Errorf("ответы изменились: %v", got)
	}
}

// Ошибка проверки не роняет страницу: ответы отправляются как есть.
func TestSubmitPageVerifiedCheckErrorKeepsAnswers(t *testing.T) {
	st := newAdaptiveStub(t, true, 1)
	st.lockApply(func() {
		st.correct[1] = "right"
		st.failCheck = true
	})
	ai := newFakeAISeq(t, `{"answers":[{"slot":1,"text":"right"}]}`)
	store, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	sy, chain := aiSyncer(t, store, ai)
	answers := map[int]moodle.QuizAnswer{1: {Text: "wrong"}}

	_, got, err := sy.submitPageVerified(context.Background(), context.Background(),
		quizTag{TG: 1, CMID: 777}, st.client(t), nil, chain, "Курс", "",
		st.page(), answers, solvePageOpts{Budget: 5 * time.Minute})
	if err != nil {
		t.Fatalf("ошибка проверки не должна ронять страницу: %v", err)
	}
	if n := ai.calls.Load(); n != 0 {
		t.Errorf("запросов к ИИ = %d, ожидали 0 после ошибки проверки", n)
	}
	st.mu.Lock()
	final, submits := st.values[1], st.submits
	st.mu.Unlock()
	if final != "wrong" || submits != 1 || got[1].Text != "wrong" {
		t.Errorf("отправлено %q (отправок %d), ожидали исходный ответ wrong", final, submits)
	}
}

// Без дедлайна бюджет 0 — ограничения нет, правка разрешена; тесный бюджет и
// истёкший контекст — запрещены.
func TestQuizRetryAllowed(t *testing.T) {
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		ctx  context.Context
		opts solvePageOpts
		want bool
	}{
		{"без дедлайна", context.Background(), solvePageOpts{}, true},
		{"бюджет больше порога", context.Background(), solvePageOpts{Budget: 2 * time.Minute}, true},
		{"тесный бюджет", context.Background(), solvePageOpts{Budget: 10 * time.Second}, false},
		{"дедлайн прошёл", context.Background(), solvePageOpts{Budget: -1}, false},
		{"мало времени до дедлайна", context.Background(), solvePageOpts{Deadline: time.Now().Add(10 * time.Second)}, false},
		{"контекст истёк", expired, solvePageOpts{}, false},
	}
	for _, tc := range cases {
		if got := quizRetryAllowed(tc.ctx, tc.opts); got != tc.want {
			t.Errorf("%s: quizRetryAllowed = %t, ожидали %t", tc.name, got, tc.want)
		}
	}
}

// Мало времени на страницу — неверный ответ не переспрашиваем.
func TestSubmitPageVerifiedSkipsRetryOnLowBudget(t *testing.T) {
	st := newAdaptiveStub(t, true, 1)
	ai := newFakeAISeq(t, `{"answers":[{"slot":1,"text":"right"}]}`)
	store, err := setupStore(t)
	if err != nil {
		t.Fatal(err)
	}
	sy, chain := aiSyncer(t, store, ai)
	answers := map[int]moodle.QuizAnswer{1: {Text: "wrong"}}

	if _, _, err := sy.submitPageVerified(context.Background(), context.Background(),
		quizTag{TG: 1, CMID: 777}, st.client(t), nil, chain, "Курс", "",
		st.page(), answers, solvePageOpts{Budget: 10 * time.Second}); err != nil {
		t.Fatalf("submitPageVerified: %v", err)
	}
	if n := ai.calls.Load(); n != 0 {
		t.Errorf("запросов к ИИ = %d, ожидали 0 при тесном бюджете", n)
	}
	if checks := st.checkLog(); len(checks) != 1 {
		t.Errorf("проверок = %v, ожидали только первый раунд", checks)
	}
}
