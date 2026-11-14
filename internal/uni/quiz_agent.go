package uni

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notes"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizlog"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websearch"
)

// Решение страницы теста — цикл до quizMaxRounds раундов:
//
//  0. рисунки условия → текст (vision), поиск по тексту каждого вопроса;
//  1. модель отвечает JSON-ом и может попросить: "search" — свой запрос,
//     "open" — прочитать страницу из выдачи целиком;
//  2. ответы проверяются (вариант из списка, не отказ, не пусто); причины
//     отказа возвращаются модели;
//  3. если что-то нашлось/открылось или есть непринятые ответы — новый раунд.
//
// Попытку сдаём, только если на каждый вопрос есть годный ответ.
const (
	quizMaxRounds   = 3 // раундов уточнения после первого ответа
	quizMaxOpens    = 3 // страниц за раунд
	quizOpenRunes   = 4000
	quizMaxImages   = 4 // рисунков на вопрос
	quizTextAnsRune = 200
)

type llmAnswerItem struct {
	Slot    int               `json:"slot"`
	Text    string            `json:"text,omitempty"`
	Choice  string            `json:"choice,omitempty"`
	Choices []string          `json:"choices,omitempty"`
	Matches map[string]string `json:"matches,omitempty"`
	// Confidence 0..1; nil — модель не сказала.
	Confidence *float64 `json:"confidence,omitempty"`
	// Search — свой поисковый запрос, если не уверена.
	Search string `json:"search,omitempty"`
	// Open — адреса из результатов поиска, которые надо прочитать целиком.
	Open []string `json:"open,omitempty"`
}

type llmSolveResponse struct {
	Answers []llmAnswerItem `json:"answers"`
}

// pageCtx — всё, что модель уже видела по странице.
type pageCtx struct {
	web      map[int]webFinding
	opened   map[string]string // url → текст страницы
	allowed  map[string]bool   // url из выдачи: открывать можно только их
	feedback map[int]string    // slot → почему прошлый ответ не принят
	queried  map[string]bool
}

func (pc *pageCtx) addFindings(f map[int]webFinding) {
	mergeFindings(pc.web, f)
	for _, x := range f {
		for _, h := range x.Hits {
			pc.allowed[h.URL] = true
		}
	}
}

type pageAsk struct {
	search map[int]string
	open   []string
}

func (s *Syncer) solvePage(ctx context.Context, c *moodle.Client, ws *websearch.Searcher, chain []llm.Target, course, materials string, questions []moodle.QuizQuestion) (map[int]moodle.QuizAnswer, llm.Result, quizlog.Page, error) {
	var pl quizlog.Page
	var res llm.Result
	if len(questions) == 0 {
		return nil, res, pl, nil
	}
	// Тип, который решатель не умеет заполнять: сдавать без него нельзя,
	// а спрашивать модель бессмысленно.
	var unsup []string
	for _, q := range questions {
		if unsupportedQuestion(q) {
			unsup = append(unsup, fmt.Sprintf("вопрос %d (тип %s)", q.Slot, q.QType))
		}
	}
	if len(unsup) > 0 {
		return nil, res, pl, fmt.Errorf("решатель не умеет заполнять %s — пройди его руками", strings.Join(unsup, ", "))
	}

	qs := s.describeQuestionImages(ctx, c, questions, &pl)

	pc := &pageCtx{web: map[int]webFinding{}, opened: map[string]string{}, allowed: map[string]bool{}, queried: map[string]bool{}}
	if ws != nil {
		queries := map[int]string{}
		for _, q := range qs {
			if qq := quizQuery(q); qq != "" {
				queries[q.Slot] = qq
				pc.queried[qq] = true
			}
		}
		found, logs := s.research(ctx, ws, queries)
		pc.addFindings(found)
		pl.Search = append(pl.Search, logs...)
	}

	final := map[int]moodle.QuizAnswer{}
	refined := map[int]bool{}
	for round := 0; ; round++ {
		ans, ask, r, err := s.askPage(ctx, chain, course, materials, qs, pc)
		pl.Attempts = append(pl.Attempts, r.Attempts...)
		if r.Text != "" {
			pl.Raw += fmt.Sprintf("--- раунд %d ---\n%s\n", round+1, r.Text)
		}
		if err != nil {
			if round == 0 {
				return nil, r, pl, err
			}
			// Уточнение упало — остаёмся с ответами прошлых раундов.
			logx.Warnf("uni", "quiz round %d: %v", round+1, err)
			break
		}
		res = r
		for slot, a := range ans {
			if round > 0 {
				refined[slot] = true
			}
			final[slot] = a
		}
		bad := validateAnswers(qs, final)
		if round >= quizMaxRounds {
			break
		}
		progressed := len(bad) > 0
		pc.feedback = bad
		if ws != nil && s.followUp(ctx, ws, pc, ask, &pl) {
			progressed = true
		}
		if !progressed {
			break
		}
	}
	for slot := range refined {
		pl.Refined = append(pl.Refined, slot)
	}
	sort.Ints(pl.Refined)

	if bad := validateAnswers(qs, final); len(bad) > 0 {
		var why []string
		for _, q := range qs {
			if w, ok := bad[q.Slot]; ok {
				why = append(why, fmt.Sprintf("вопрос %d — %s", q.Slot, w))
			}
		}
		return final, res, pl, fmt.Errorf("нет годного ответа: %s", strings.Join(why, "; "))
	}
	return final, res, pl, nil
}

// followUp выполняет просьбы модели: новые запросы и чтение страниц.
// true — появилось что-то новое.
func (s *Syncer) followUp(ctx context.Context, ws *websearch.Searcher, pc *pageCtx, ask pageAsk, pl *quizlog.Page) bool {
	got := false
	queries := map[int]string{}
	for slot, q := range ask.search {
		if !pc.queried[q] {
			queries[slot] = q
			pc.queried[q] = true
		}
	}
	if len(queries) > 0 {
		found, logs := s.research(ctx, ws, queries)
		for i := range logs {
			logs[i].Refine = true
		}
		pl.Search = append(pl.Search, logs...)
		if len(found) > 0 {
			pc.addFindings(found)
			got = true
		}
	}
	n := 0
	for _, u := range ask.open {
		if n >= quizMaxOpens {
			break
		}
		if !pc.allowed[u] {
			continue // модель выдумала адрес или он не из выдачи
		}
		if _, done := pc.opened[u]; done {
			continue
		}
		n++
		cctx, cancel := context.WithTimeout(ctx, quizSearchTimeout)
		text, err := openPageFn(cctx, u)
		cancel()
		l := quizlog.Search{Query: "open " + u, Provider: "page", Refine: true}
		if err != nil {
			l.Error = err.Error()
			pc.opened[u] = "" // не пробуем снова
		} else {
			l.Hits = 1
			pc.opened[u] = text
			got = true
		}
		pl.Search = append(pl.Search, l)
	}
	return got
}

const quizSysPrompt = "Ты — отличник университета. Реши тест по дисциплине по материалам лекций, условиям и найденному в интернете.\n" +
	"Отвечай СТРОГО одним JSON-объектом, без текста до и после:\n" +
	"{\"answers\": [{\"slot\": 1, \"choice\": \"0\", \"confidence\": 0.9}, {\"slot\": 2, \"text\": \"10110\", \"confidence\": 0.4, \"search\": \"запрос\", \"open\": [\"https://...\"]}]}\n" +
	"Поля ответа:\n" +
	"- multichoice с одним ответом: \"choice\" — ключ варианта в квадратных скобках.\n" +
	"- multichoice с несколькими ответами: \"choices\" — список ключей выбранных вариантов.\n" +
	"- shortanswer/numerical: \"text\" — только сам ответ, без пояснений, пробелов и точки в конце.\n" +
	"- match: \"matches\" — словарь поле -> ключ варианта.\n" +
	"- \"confidence\" от 0 до 1 — насколько уверен.\n" +
	"- \"search\" (по желанию) — запрос для поисковика (3–10 слов), если хочешь что-то проверить; результаты покажут в следующем раунде.\n" +
	"- \"open\" (по желанию) — до 3 адресов ИЗ ПОКАЗАННЫХ результатов поиска, страницы которых надо прочитать целиком.\n" +
	"Правила:\n" +
	"- Отвечай на КАЖДЫЙ вопрос. Отказ, «невозможно решить», «нет данных» — запрещены: если данных мало, реши сам по своим знаниям и дай самый вероятный ответ с низкой confidence.\n" +
	"- Задачи на коды (Шеннон-Фано, Хаффман, Элиас, декодирование) решай сам по шагам в уме и выдавай только результат.\n" +
	"- Материалы лекций главнее интернета. В найденных банках ответов варианты бывают в другом порядке или от другого варианта теста — сверяй по тексту, а не по букве.\n" +
	"- Если ответ из прошлого раунда не принят, ниже указана причина — исправь."

// askPage — один раунд: промпт со всем, что известно, ответ модели,
// разбор JSON (с одной попыткой починки) и приведение ключей.
func (s *Syncer) askPage(ctx context.Context, chain []llm.Target, course, materials string, qs []moodle.QuizQuestion, pc *pageCtx) (map[int]moodle.QuizAnswer, pageAsk, llm.Result, error) {
	var ask pageAsk
	user := buildQuizPrompt(course, materials, qs, pc)
	res, err := llm.Ask(ctx, chain, quizSysPrompt, user)
	if err != nil {
		return nil, ask, res, fmt.Errorf("ask llm: %w", err)
	}
	parsed, perr := extractQuizJSON(res.Text)
	if perr != nil {
		// Модель ответила рассуждением — просим ещё раз только JSON.
		logx.Warnf("uni", "quiz: ответ не JSON (%v), переспрашиваю", perr)
		res2, err2 := llm.Ask(ctx, chain, quizSysPrompt, user+
			"\n\nВНИМАНИЕ: твой прошлый ответ был не JSON. Верни ТОЛЬКО JSON-объект {\"answers\": [...]} по схеме, без пояснений и без отказов.")
		res2.Attempts = append(res.Attempts, res2.Attempts...)
		res2.Text = res.Text + "\n--- повтор: только JSON ---\n" + res2.Text
		res = res2
		if err2 != nil {
			return nil, ask, res, fmt.Errorf("ask llm: %w", err2)
		}
		if parsed, perr = extractQuizJSON(res2.Text); perr != nil {
			return nil, ask, res, fmt.Errorf("ответ ИИ не JSON: %w", perr)
		}
	}

	bySlot := map[int]moodle.QuizQuestion{}
	for _, q := range qs {
		bySlot[q.Slot] = q
	}
	out := map[int]moodle.QuizAnswer{}
	ask.search = map[int]string{}
	for _, a := range parsed.Answers {
		q, ok := bySlot[a.Slot]
		if !ok {
			continue
		}
		out[a.Slot] = normalizeAnswer(q, a)
		if q := strings.TrimSpace(a.Search); q != "" {
			ask.search[a.Slot] = q
		}
		for _, u := range a.Open {
			if u = strings.TrimSpace(u); u != "" {
				ask.open = append(ask.open, u)
			}
		}
	}
	return out, ask, res, nil
}

// choiceKey — ключ варианта в промпте. У radio это value; у checkbox value
// у всех «1», поэтому ключ — имя поля.
func choiceKey(ch moodle.QuizChoice) string {
	if ch.IsMulti {
		return ch.Name
	}
	return ch.Value
}

func buildQuizPrompt(course, materials string, qs []moodle.QuizQuestion, pc *pageCtx) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Курс: %s\n\n", course)
	if materials != "" {
		b.WriteString("--- Материалы курса (конспекты/транскрипты) ---\n")
		b.WriteString(materials)
		b.WriteString("\n\n")
	}
	b.WriteString("--- Вопросы теста ---\n")
	for _, q := range qs {
		kind := q.QType
		if len(q.Choices) > 0 && q.Choices[0].IsMulti {
			kind += ", несколько ответов"
		} else if len(q.Choices) > 0 {
			kind += ", один ответ"
		}
		fmt.Fprintf(&b, "Вопрос %d (тип: %s):\n%s\n", q.Slot, kind, q.Text)
		if q.Prompt != "" {
			fmt.Fprintf(&b, "Подсказка: %s\n", q.Prompt)
		}
		if len(q.Choices) > 0 {
			b.WriteString("Варианты ответа:\n")
			for _, ch := range q.Choices {
				fmt.Fprintf(&b, "- [%s] %s\n", choiceKey(ch), ch.Text)
			}
		}
		if len(q.MatchOptions) > 0 {
			b.WriteString("Соответствия:\n")
			for _, mo := range q.MatchOptions {
				fmt.Fprintf(&b, "- %s (поле: %s)\n", mo.Question, mo.Name)
				for _, opt := range mo.Choices {
					fmt.Fprintf(&b, "    [%s] %s\n", opt.Value, opt.Text)
				}
			}
		}
		if pc != nil {
			writeFindings(&b, pc.web[q.Slot])
			if why, ok := pc.feedback[q.Slot]; ok {
				fmt.Fprintf(&b, "Прошлый ответ не принят: %s\n", why)
			}
		}
		b.WriteString("\n")
	}
	if pc != nil {
		var urls []string
		for u, t := range pc.opened {
			if t != "" {
				urls = append(urls, u)
			}
		}
		sort.Strings(urls)
		if len(urls) > 0 {
			b.WriteString("--- Прочитанные страницы ---\n")
			for _, u := range urls {
				fmt.Fprintf(&b, "[%s]\n%s\n\n", u, pc.opened[u])
			}
		}
	}
	return b.String()
}

// extractQuizJSON достаёт первый JSON-объект из ответа: модели любят
// обернуть его в ```json``` или дописать рассуждение до/после.
func extractQuizJSON(text string) (llmSolveResponse, error) {
	var out llmSolveResponse
	t := strings.TrimSpace(text)
	for start := 0; start < len(t); {
		i := strings.IndexByte(t[start:], '{')
		if i < 0 {
			break
		}
		i += start
		dec := json.NewDecoder(strings.NewReader(t[i:]))
		var cand llmSolveResponse
		if err := dec.Decode(&cand); err == nil && len(cand.Answers) > 0 {
			return cand, nil
		}
		start = i + 1
	}
	if err := json.Unmarshal([]byte(t), &out); err != nil {
		return out, err
	}
	if len(out.Answers) == 0 {
		return out, errors.New("в JSON нет answers")
	}
	return out, nil
}

// normalizeAnswer приводит ответ модели к ключам формы Moodle: текст
// варианта → value/имя, лишние пробелы прочь.
func normalizeAnswer(q moodle.QuizQuestion, a llmAnswerItem) moodle.QuizAnswer {
	ans := moodle.QuizAnswer{Text: strings.TrimSpace(a.Text), Choice: strings.TrimSpace(a.Choice), Matches: map[string]string{}}
	findChoice := func(v string) (moodle.QuizChoice, bool) {
		v = strings.TrimSpace(v)
		for _, ch := range q.Choices {
			if v == choiceKey(ch) {
				return ch, true
			}
		}
		for _, ch := range q.Choices {
			if strings.EqualFold(v, strings.TrimSpace(ch.Text)) {
				return ch, true
			}
		}
		return moodle.QuizChoice{}, false
	}
	switch {
	case len(q.Choices) > 0 && q.Choices[0].IsMulti:
		seen := map[string]bool{}
		for _, v := range a.Choices {
			if ch, ok := findChoice(v); ok && !seen[ch.Name] {
				seen[ch.Name] = true
				ans.Choices = append(ans.Choices, ch.Name)
			}
		}
		ans.Choice = ""
	case len(q.Choices) > 0:
		v := ans.Choice
		if v == "" && len(a.Choices) > 0 {
			v = a.Choices[0]
		}
		if v == "" {
			v = ans.Text
		}
		if ch, ok := findChoice(v); ok {
			ans.Choice = ch.Value
		} else {
			ans.Choice = v // не нашли — пусть validateAnswers скажет
		}
	case len(q.MatchOptions) > 0:
		for _, mo := range q.MatchOptions {
			v, ok := a.Matches[mo.Name]
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			for _, o := range mo.Choices {
				if v == o.Value {
					break
				}
				if strings.EqualFold(v, strings.TrimSpace(o.Text)) {
					v = o.Value
					break
				}
			}
			ans.Matches[mo.Name] = v
		}
	}
	return ans
}

func unsupportedQuestion(q moodle.QuizQuestion) bool {
	return len(q.Choices) == 0 && len(q.MatchOptions) == 0 && q.InputName == ""
}

var quizRefusal = regexp.MustCompile(`(?i)(невозможно|не могу|не удаётся|не удается|отсутству|недостаточно|нет данных|нет информации|cannot|can't|unable|not enough)`)
var quizDigit = regexp.MustCompile(`\d`)

// validateAnswers — slot → почему ответ не годится. Пустая карта — сдавать можно.
func validateAnswers(qs []moodle.QuizQuestion, answers map[int]moodle.QuizAnswer) map[int]string {
	bad := map[int]string{}
	for _, q := range qs {
		a, ok := answers[q.Slot]
		if !ok {
			bad[q.Slot] = "нет ответа"
			continue
		}
		switch {
		case len(q.Choices) > 0 && q.Choices[0].IsMulti:
			if len(a.Choices) == 0 {
				bad[q.Slot] = "не выбран ни один вариант (ключи — в квадратных скобках)"
			}
		case len(q.Choices) > 0:
			found := false
			for _, ch := range q.Choices {
				if ch.Value == a.Choice {
					found = true
				}
			}
			if !found {
				bad[q.Slot] = fmt.Sprintf("вариант %q не из списка — нужен ключ из квадратных скобок", a.Choice)
			}
		case len(q.MatchOptions) > 0:
			var miss []string
			for _, mo := range q.MatchOptions {
				v := a.Matches[mo.Name]
				okv := false
				for _, o := range mo.Choices {
					if o.Value == v && v != "0" && v != "" {
						okv = true
					}
				}
				if !okv {
					miss = append(miss, mo.Question)
				}
			}
			if len(miss) > 0 {
				bad[q.Slot] = "не сопоставлено: " + strings.Join(miss, "; ")
			}
		default:
			t := strings.TrimSpace(a.Text)
			switch {
			case t == "":
				bad[q.Slot] = "пустой ответ"
			case quizRefusal.MatchString(t):
				bad[q.Slot] = "вместо ответа отказ — отказываться нельзя, реши сам"
			case len([]rune(t)) > quizTextAnsRune:
				bad[q.Slot] = "слишком длинно — нужен только сам ответ"
			case q.QType == "numerical" && !quizDigit.MatchString(t):
				bad[q.Slot] = "нужен числовой ответ"
			}
		}
	}
	return bad
}

const quizImagePrompt = "Это рисунок из вопроса университетского теста. Перепиши его содержимое точно, без вступлений: таблицы — строками «ячейка | ячейка», коды и битовые строки — символ в символ, деревья и схемы — словами (какой узел/символ какой код получает), формулы — в LaTeX."

// describeQuestionImages — копии вопросов, где рисунки условия переписаны
// текстом. Не вышло — так и пишем: модель хотя бы знает, что рисунок был.
func (s *Syncer) describeQuestionImages(ctx context.Context, c *moodle.Client, qs []moodle.QuizQuestion, pl *quizlog.Page) []moodle.QuizQuestion {
	out := make([]moodle.QuizQuestion, len(qs))
	copy(out, qs)
	for i := range out {
		q := &out[i]
		for n, src := range q.Images {
			if n >= quizMaxImages {
				break
			}
			desc, err := s.describeImage(ctx, c, src)
			if err != nil {
				logx.Warnf("uni", "quiz image slot %d #%d: %v", q.Slot, n+1, err)
				pl.Notes = append(pl.Notes, fmt.Sprintf("вопрос %d, рисунок %d: %v", q.Slot, n+1, err))
				q.Text += fmt.Sprintf("\nРисунок %d: (не удалось распознать)", n+1)
				continue
			}
			pl.Notes = append(pl.Notes, fmt.Sprintf("вопрос %d, рисунок %d распознан (%d симв.)", q.Slot, n+1, len([]rune(desc))))
			q.Text += fmt.Sprintf("\nРисунок %d:\n%s", n+1, desc)
		}
	}
	return out
}

func (s *Syncer) describeImage(ctx context.Context, c *moodle.Client, src string) (string, error) {
	if s.DescribeImage != nil {
		return s.DescribeImage(ctx, src)
	}
	if c == nil {
		return "", errors.New("нет сессии Moodle")
	}
	conf := s.getCfg()
	if conf == nil {
		return "", errors.New("нет конфига")
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	raw, mime, err := c.FetchFile(cctx, src)
	if err != nil {
		return "", fmt.Errorf("скачать: %w", err)
	}
	return notes.DescribeImage(cctx, conf, raw, mime, quizImagePrompt)
}

// openPageFn — подмена в тестах (httptest слушает loopback, а он запрещён).
var openPageFn = openPage

// openPage — текст страницы из выдачи. Только http(s) и не внутренние адреса:
// адрес приходит от модели, это не должен быть путь в локальную сеть VDS.
func openPage(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errors.New("плохой адрес")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return "", errors.New("внутренний адрес")
	}
	if h := strings.ToLower(u.Hostname()); h == "localhost" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return "", errors.New("внутренний адрес")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.6")
	resp, err := (&http.Client{Timeout: quizSearchTimeout}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	body := io.LimitReader(resp.Body, 2<<20)
	var text string
	if strings.Contains(ct, "html") || ct == "" {
		doc, err := goquery.NewDocumentFromReader(body)
		if err != nil {
			return "", err
		}
		doc.Find("script, style, noscript, nav, header, footer, aside, form, svg").Remove()
		root := doc.Find("article, main").First()
		if root.Length() == 0 {
			root = doc.Find("body")
		}
		text, _ = extractPageText(root)
	} else if strings.HasPrefix(ct, "text/") {
		b, _ := io.ReadAll(body)
		text = string(b)
	} else {
		return "", fmt.Errorf("не текст: %s", ct)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("пустая страница")
	}
	if r := []rune(text); len(r) > quizOpenRunes {
		text = string(r[:quizOpenRunes]) + "…"
	}
	return text, nil
}

// extractPageText — текст блоками, по строке на блок.
func extractPageText(sel *goquery.Selection) (string, error) {
	var lines []string
	sel.Find("h1, h2, h3, h4, p, li, td, pre, blockquote").Each(func(_ int, x *goquery.Selection) {
		if x.Find("p, li, td").Length() > 0 {
			return // вложенные блоки возьмём по отдельности
		}
		if t := strings.Join(strings.Fields(x.Text()), " "); t != "" {
			lines = append(lines, t)
		}
	})
	if len(lines) == 0 {
		return strings.Join(strings.Fields(sel.Text()), " "), nil
	}
	return strings.Join(lines, "\n"), nil
}
