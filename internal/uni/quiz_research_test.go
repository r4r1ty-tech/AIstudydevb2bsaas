package uni

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websearch"
)

type fakeWeb struct {
	mu      sync.Mutex
	queries []string
}

func (f *fakeWeb) Name() string { return "fake" }
func (f *fakeWeb) Search(_ context.Context, q string, _ int) ([]websearch.Hit, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	return []websearch.Hit{{Title: "Ответ на " + q, URL: "https://ex/" + q, Snippet: "правильно: SYN"}}, nil
}

func TestQuizQuery(t *testing.T) {
	q := moodle.QuizQuestion{Text: "Вопрос 1 Пока нет ответа Балл: 1,00 Отметить вопрос   Какой пакет начинает   TCP-соединение? Выберите один ответ:"}
	if got := quizQuery(q); got != "Какой пакет начинает TCP-соединение?" {
		t.Fatalf("query=%q", got)
	}
	if quizQuery(moodle.QuizQuestion{Text: "Ответ:"}) != "" {
		t.Fatal("пустой вопрос должен давать пустой запрос")
	}
	long := strings.Repeat("слово ", 100)
	if r := []rune(quizQuery(moodle.QuizQuestion{Text: long})); len(r) > quizQueryRunes {
		t.Fatalf("не обрезан: %d", len(r))
	}
}

// Дословный запрос — очищенный текст вопроса в кавычках; короткие фразы
// (меньше quizExactMinRunes) в поиск не идут, длинные обрезаются по слову.
func TestQuizExactQuery(t *testing.T) {
	q := moodle.QuizQuestion{Text: "Вопрос 1 Пока нет ответа Балл: 1,00 Отметить вопрос Какие три пакета участвуют в установлении TCP-соединения между узлами сети?"}
	want := `"Какие три пакета участвуют в установлении TCP-соединения между узлами сети?"`
	if got := quizExactQuery(q); got != want {
		t.Fatalf("exact=%q, ждал %q", got, want)
	}
	if quizExactQuery(moodle.QuizQuestion{Text: "Что такое DNS?"}) != "" {
		t.Fatal("короткая фраза не должна идти в поиск")
	}
	long := `"` + strings.Repeat("слово ", 100) + `"`
	got := []rune(quizExactQuery(moodle.QuizQuestion{Text: long}))
	if got[0] != '"' || got[len(got)-1] != '"' || len(got) > quizExactRunes+2 {
		t.Fatalf("длинная фраза: %q (%d рун)", string(got), len(got))
	}
	if strings.Contains(string(got), `""`) {
		t.Fatalf("двойные кавычки внутри фразы: %q", string(got))
	}
}

// На странице сначала ищутся дословные фразы, потом ключевые слова;
// одинаковые запросы не повторяются.
func TestPageSearchReqsExactFirst(t *testing.T) {
	pc := &pageCtx{queried: map[string]bool{}}
	const long = "Какие три пакета участвуют в установлении TCP-соединения между узлами сети?"
	qs := []moodle.QuizQuestion{
		{Slot: 1, Text: long},
		{Slot: 2, Text: "Какой флаг у первого пакета TCP?"},
		{Slot: 3, Text: long},
	}
	reqs := pageSearchReqs(pc, qs)
	if len(reqs) != 3 {
		t.Fatalf("запросов %d: %+v", len(reqs), reqs)
	}
	if !reqs[0].Exact || reqs[0].Slot != 1 {
		t.Fatalf("первым должен быть дословный запрос слота 1: %+v", reqs[0])
	}
	seen := map[string]bool{}
	for _, r := range reqs {
		if seen[r.Query] {
			t.Fatalf("повтор запроса %q", r.Query)
		}
		seen[r.Query] = true
	}
	// Повторный вызов по той же странице новых запросов не даёт.
	if again := pageSearchReqs(pc, qs); len(again) != 0 {
		t.Fatalf("повторные запросы: %+v", again)
	}
}

// Полный текст страницы показывается вместо сниппета, но с выдержкой.
func TestWriteFindingsContentInsteadOfSnippet(t *testing.T) {
	content := "дословный текст вопроса: " + strings.Repeat("а", 3000)
	f := webFinding{Hits: []websearch.Hit{
		{Title: "studfile", URL: "https://s/1", Snippet: "обрезанный сниппет", Content: content},
		{Title: "other", URL: "https://s/2", Snippet: "короткий сниппет"},
	}}
	var b strings.Builder
	writeFindings(&b, f)
	out := b.String()
	for _, want := range []string{"https://s/1", "studfile", "дословный текст вопроса", "[текст страницы, выдержка]", "https://s/2", "короткий сниппет"} {
		if !strings.Contains(out, want) {
			t.Errorf("в промпте нет %q: %s", want, out)
		}
	}
	if strings.Contains(out, strings.Repeat("а", quizContentRunes+1)) {
		t.Error("content не обрезан до quizContentRunes")
	}
	if strings.Contains(out, "обрезанный сниппет") {
		t.Error("при наличии content сниппет показывать не нужно")
	}
}

// Лимит поисков на страницу: лишние запросы не уходят поставщику.
func TestResearchLimit(t *testing.T) {
	fw := &fakeWeb{}
	sy := &Syncer{}
	var reqs []searchReq
	for i := 1; i <= 10; i++ {
		reqs = append(reqs, searchReq{Slot: i, Query: fmt.Sprintf("q%d", i)})
	}
	ws := &websearch.Searcher{Providers: []websearch.Provider{fw}}
	found, logs := sy.research(context.Background(), quizTag{TG: 1, CMID: 2}, ws, reqs, quizMaxSearchesPage)
	if len(logs) != quizMaxSearchesPage || len(fw.queries) != quizMaxSearchesPage {
		t.Fatalf("логов %d, запросов %d; ждал %d", len(logs), len(fw.queries), quizMaxSearchesPage)
	}
	if len(found) != quizMaxSearchesPage {
		t.Fatalf("находок %d", len(found))
	}
	if _, logs2 := sy.research(context.Background(), quizTag{TG: 1, CMID: 2}, ws, reqs, 0); len(logs2) != 0 {
		t.Fatalf("при исчерпанном бюджете ушло %d запросов", len(logs2))
	}
	if len(fw.queries) != quizMaxSearchesPage {
		t.Fatalf("после исчерпания бюджета запросов %d", len(fw.queries))
	}
}

// Страница из пяти вопросов: дословные фразы идут первыми, всего не больше
// quizMaxSearchesPage поисков, один и тот же запрос дважды не ищется.
func TestSolvePageSearchBudget(t *testing.T) {
	var qs []moodle.QuizQuestion
	subjects := []string{"TCP", "UDP", "IP", "HTTP", "DNS"}
	for i, s := range subjects {
		qs = append(qs, moodle.QuizQuestion{Slot: i + 1, QType: "shortanswer", InputName: fmt.Sprintf("q%d", i+1),
			Text: fmt.Sprintf("%s: какой протокол обеспечивает надёжную доставку сегментов между узлами сети?", s)})
	}
	var ans []string
	for i := range qs {
		ans = append(ans, fmt.Sprintf(`{"slot":%d,"text":"TCP","confidence":0.9}`, i+1))
	}
	srv, _ := fakeLLM(t, `{"answers":[`+strings.Join(ans, ",")+`]}`)
	fw := &fakeWeb{}
	sy := &Syncer{Web: &websearch.Searcher{Providers: []websearch.Provider{fw}}}
	if _, _, _, err := sy.solvePage(context.Background(), quizTag{TG: 1, CMID: 2}, nil, sy.webSearcher(), chainFor(srv), "", "", qs); err != nil {
		t.Fatal(err)
	}
	exact := 0
	for _, q := range fw.queries {
		if strings.HasPrefix(q, `"`) {
			exact++
		}
	}
	if len(fw.queries) != quizMaxSearchesPage || exact != len(qs) {
		t.Fatalf("запросов %d (дословных %d); ждал %d, из них %d дословных: %v",
			len(fw.queries), exact, quizMaxSearchesPage, len(qs), fw.queries)
	}
}

// fakeLLM отвечает по очереди заготовками; последняя повторяется.
func fakeLLM(t *testing.T, replies ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var prompts []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(prompts)
		prompts = append(prompts, string(body))
		mu.Unlock()
		content := replies[len(replies)-1]
		if n < len(replies) {
			content = replies[n]
		}
		out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &prompts
}

func chainFor(srv *httptest.Server) []llm.Target {
	cfg := &config.Config{LLMAPIKey: "k", LLMAPIURL: srv.URL, LLMModel: "m"}
	return llm.ChainFor(cfg, llm.Choice{}, true, "")
}

func tcpQuestions() []moodle.QuizQuestion {
	return []moodle.QuizQuestion{
		{Slot: 1, QType: "multichoice", InputName: "q1:1_answer", Text: "Сколько пакетов в рукопожатии TCP?", Choices: []moodle.QuizChoice{{Name: "q1:1_answer", Value: "0", Text: "3"}, {Name: "q1:1_answer", Value: "1", Text: "2"}}},
		{Slot: 2, QType: "shortanswer", InputName: "q1:2_answer", Text: "Какой флаг у первого пакета TCP?"},
	}
}

// Модель отказывается, просит поиск и чтение страницы; во втором раунде
// видит причину отказа, найденное и текст страницы — и отвечает.
func TestSolvePageAgentLoop(t *testing.T) {
	srv, prompts := fakeLLM(t,
		`{"answers":[{"slot":1,"choice":"3","confidence":0.9},{"slot":2,"text":"Невозможно решить: нет данных","confidence":0.1,"search":"первый пакет TCP","open":["https://ex/Какой флаг у первого пакета TCP?","https://evil.local/x"]}]}`,
		"Подумаю... вот ответ:\n```json\n{\"answers\":[{\"slot\":1,\"choice\":\"0\"},{\"slot\":2,\"text\":\"SYN\",\"confidence\":0.95}]}\n```",
	)
	var opened []string
	old := openPageFn
	openPageFn = func(_ context.Context, u string) (string, error) {
		opened = append(opened, u)
		return "Первый сегмент несёт флаг SYN.", nil
	}
	defer func() { openPageFn = old }()

	fw := &fakeWeb{}
	sy := &Syncer{Web: &websearch.Searcher{Providers: []websearch.Provider{fw}}}
	ans, _, pl, err := sy.solvePage(context.Background(), quizTag{TG: 1, CMID: 2}, nil, sy.webSearcher(), chainFor(srv), "Сети", "", tcpQuestions())
	if err != nil {
		t.Fatal(err)
	}
	if len(*prompts) != 2 {
		t.Fatalf("ожидал 2 раунда, было %d", len(*prompts))
	}
	if ans[1].Choice != "0" || ans[2].Text != "SYN" {
		t.Fatalf("ответы %+v", ans)
	}
	if len(opened) != 1 || !strings.HasPrefix(opened[0], "https://ex/") {
		t.Fatalf("открыто %v (выдуманный адрес открывать нельзя)", opened)
	}
	p2 := (*prompts)[1]
	for _, want := range []string{"Прошлый ответ не принят", "первый пакет TCP", "Первый сегмент несёт флаг SYN"} {
		if !strings.Contains(p2, want) {
			t.Errorf("во втором раунде нет %q", want)
		}
	}
	if len(pl.Refined) != 2 {
		t.Fatalf("refined=%v", pl.Refined)
	}
}

// Модель стоит на отказе — попытку не сдаём, а объясняем почему.
func TestSolvePageRefusalBlocksSubmit(t *testing.T) {
	srv, prompts := fakeLLM(t, `{"answers":[{"slot":1,"choice":"0"},{"slot":2,"text":"Невозможно решить: отсутствует таблица"}]}`)
	sy := &Syncer{}
	_, _, _, err := sy.solvePage(context.Background(), quizTag{TG: 1, CMID: 2}, nil, nil, chainFor(srv), "", "", tcpQuestions())
	if err == nil || !strings.Contains(err.Error(), "вопрос 2") {
		t.Fatalf("err=%v", err)
	}
	if len(*prompts) != 1+quizMaxRounds {
		t.Fatalf("раундов %d", len(*prompts))
	}
}

// Рассуждение вместо JSON — один переспрос «только JSON».
func TestSolvePageRepairsNotJSON(t *testing.T) {
	srv, prompts := fakeLLM(t, "Решение: сначала строим дерево...", `{"answers":[{"slot":1,"choice":"0"},{"slot":2,"text":"SYN"}]}`)
	sy := &Syncer{}
	ans, _, _, err := sy.solvePage(context.Background(), quizTag{TG: 1, CMID: 2}, nil, nil, chainFor(srv), "", "", tcpQuestions())
	if err != nil || ans[2].Text != "SYN" || len(*prompts) != 2 {
		t.Fatalf("ans=%v err=%v calls=%d", ans, err, len(*prompts))
	}
	if !strings.Contains((*prompts)[1], "прошлый ответ был не JSON") {
		t.Fatal("нет просьбы вернуть JSON")
	}
}

// Тип без полей ввода (drag-and-drop и т.п.) — модель не зовём, сдавать нельзя.
func TestSolvePageUnsupported(t *testing.T) {
	srv, prompts := fakeLLM(t, `{}`)
	sy := &Syncer{}
	_, _, _, err := sy.solvePage(context.Background(), quizTag{TG: 1, CMID: 2}, nil, nil, chainFor(srv), "", "", []moodle.QuizQuestion{{Slot: 1, QType: "ddwtos", Text: "Перетащите"}})
	if err == nil || !strings.Contains(err.Error(), "ddwtos") || len(*prompts) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(*prompts))
	}
}

// Неизвестный тип не валит страницу: вопрос остаётся без ответа, остальные решаются.
func TestSolvePageUnsupportedQuestionDoesNotBlockPage(t *testing.T) {
	supported := moodle.QuizQuestion{Slot: 1, QType: "multichoice", Text: "Сколько пакетов в рукопожатии TCP?",
		Choices: []moodle.QuizChoice{{Name: "q1:1_answer", Value: "0", Text: "3"}, {Name: "q1:1_answer", Value: "1", Text: "2"}}}
	unsupported := moodle.QuizQuestion{Slot: 2, QType: "ddwtos", Text: "Перетащите термины"}
	srv, prompts := fakeLLM(t, `{"answers":[{"slot":1,"choice":"0"}]}`)
	sy := &Syncer{}
	ans, _, _, err := sy.solvePage(context.Background(), quizTag{TG: 1, CMID: 2}, nil, nil, chainFor(srv), "", "", []moodle.QuizQuestion{supported, unsupported})
	if err != nil {
		t.Fatalf("страница с одним поддерживаемым вопросом должна решиться: %v", err)
	}
	if ans[1].Choice != "0" {
		t.Errorf("ответ слота 1 = %+v, ожидали вариант 0", ans[1])
	}
	if _, ok := ans[2]; ok {
		t.Error("на неподдерживаемый вопрос ответа быть не должно")
	}
	if len(*prompts) == 0 {
		t.Fatal("модель не вызывалась")
	}
	if strings.Contains((*prompts)[0], "Перетащите термины") {
		t.Error("неподдерживаемый вопрос попал в промпт модели")
	}
}

// У checkbox value у всех «1»: в промпте ключ — имя поля, ответ по тексту тоже принимается.
func TestSolvePageMultiKeys(t *testing.T) {
	q := moodle.QuizQuestion{Slot: 1, QType: "multichoice", Text: "Какие коды префиксные?", Choices: []moodle.QuizChoice{
		{Name: "q9:1_choice0", Value: "1", Text: "0, 10, 11", IsMulti: true},
		{Name: "q9:1_choice1", Value: "1", Text: "0, 01", IsMulti: true},
		{Name: "q9:1_choice2", Value: "1", Text: "00, 01, 1", IsMulti: true},
	}}
	srv, prompts := fakeLLM(t, `{"answers":[{"slot":1,"choices":["q9:1_choice0","00, 01, 1","1"]}]}`)
	sy := &Syncer{}
	ans, _, _, err := sy.solvePage(context.Background(), quizTag{TG: 1, CMID: 2}, nil, nil, chainFor(srv), "", "", []moodle.QuizQuestion{q})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ans[1].Choices, ",") != "q9:1_choice0,q9:1_choice2" {
		t.Fatalf("choices=%v", ans[1].Choices)
	}
	if !strings.Contains((*prompts)[0], "[q9:1_choice1] 0, 01") {
		t.Fatal("в промпте ключ checkbox не имя поля")
	}
}

// Рисунок условия переписывается текстом до вопроса модели.
func TestSolvePageImage(t *testing.T) {
	srv, prompts := fakeLLM(t, `{"answers":[{"slot":1,"text":"абв"}]}`)
	sy := &Syncer{DescribeImage: func(_ context.Context, src string) (string, error) {
		return "а | 0\nб | 10\nв | 11", nil
	}}
	q := moodle.QuizQuestion{Slot: 1, QType: "shortanswer", InputName: "a", Text: "Декодируйте 01011 по коду [рисунок 1]", Images: []string{"https://do/pluginfile.php/x.png"}}
	_, _, pl, err := sy.solvePage(context.Background(), quizTag{TG: 1, CMID: 2}, nil, nil, chainFor(srv), "", "", []moodle.QuizQuestion{q})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*prompts)[0], `Рисунок 1:\nа | 0`) {
		t.Fatalf("рисунок не попал в промпт: %s", (*prompts)[0])
	}
	if len(pl.Notes) != 1 {
		t.Fatalf("notes=%v", pl.Notes)
	}
}

// syncBuf — приёмник подробного лога: поиск пишет из горутин параллельно.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// dumpBodies — распакованные снимки в quiz-llm, в имени которых есть part.
func dumpBodies(t *testing.T, dir, part string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(filepath.Join(dir, "quiz-llm"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.Contains(d.Name(), part) || !strings.HasSuffix(d.Name(), ".gz") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(data))
		return nil
	})
	return out
}

// Подробный лог: каждая строка про тест несёт tg и cmid, снимки промпта и ответа легли в Dumper.
func TestSolvePageDebugLog(t *testing.T) {
	srv, _ := fakeLLM(t, `{"answers":[{"slot":1,"choice":"0","confidence":0.9},{"slot":2,"text":"SYN","confidence":0.9}]}`)
	var sink syncBuf
	dir := t.TempDir()
	logx.SetDebugSink(&sink, &logx.Dumper{Dir: dir, MaxBytes: 1 << 20})
	defer logx.SetDebugSink(nil, nil)

	fw := &fakeWeb{}
	sy := &Syncer{Web: &websearch.Searcher{Providers: []websearch.Provider{fw}}}
	if _, _, _, err := sy.solvePage(context.Background(), quizTag{TG: 1, CMID: 2}, nil, sy.webSearcher(), chainFor(srv), "Сети", "", tcpQuestions()); err != nil {
		t.Fatal(err)
	}

	out := sink.String()
	for _, want := range []string{"поиск slot=1", "раунд 1", "было"} {
		if !strings.Contains(out, want) {
			t.Errorf("в подробном логе нет %q", want)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, "uni: ") && (!strings.Contains(line, "tg=1 ") || !strings.Contains(line, "cmid=2 ")) {
			t.Errorf("строка про тест без tg/cmid: %s", line)
		}
	}

	prompts := dumpBodies(t, dir, "prompt")
	replies := dumpBodies(t, dir, "reply")
	if len(prompts) != 1 || len(replies) != 1 {
		t.Fatalf("снимков промпта %d, ответа %d; хотел по одному", len(prompts), len(replies))
	}
	if !strings.Contains(prompts[0], "=== system ===") || !strings.Contains(prompts[0], "Сколько пакетов в рукопожатии TCP?") {
		t.Errorf("снимок промпта неполный: %.200s", prompts[0])
	}
	if !strings.Contains(replies[0], `"slot":1`) {
		t.Errorf("снимок ответа: %q", replies[0])
	}
}

// Без поиска и с годным ответом — один запрос к модели.
func TestSolvePageWithoutWeb(t *testing.T) {
	srv, prompts := fakeLLM(t, `{"answers":[{"slot":1,"choice":"0","confidence":0.2,"search":"q"},{"slot":2,"text":"SYN"}]}`)
	sy := &Syncer{Cfg: &config.Config{}}
	ans, _, _, err := sy.solvePage(context.Background(), quizTag{TG: 1, CMID: 2}, nil, sy.webSearcher(), chainFor(srv), "", "", tcpQuestions())
	if err != nil || len(*prompts) != 1 || ans[1].Choice != "0" {
		t.Fatalf("ans=%v calls=%d err=%v", ans, len(*prompts), err)
	}
}
