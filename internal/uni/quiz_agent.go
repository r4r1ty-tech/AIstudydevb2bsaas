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
	"sync"
	"sync/atomic"
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
	quizMaxRounds         = 3 // раундов уточнения после первого ответа
	quizMaxOpens          = 3 // страниц за раунд
	quizOpenRunes         = 4000
	quizMaxImages         = 4 // рисунков на вопрос
	quizImageWorkers      = 4 // воркеров параллельного распознавания рисунков
	quizTextAnsRune       = 200
	quizFastModeRemaining = 60 * time.Second
	quizMaxRedirects      = 5

	// Консенсус — это 3 параллельных запроса, а не один. На коротких тестах
	// (3–5 минут) он может не успеть: ниже порога решаем обычной цепочкой.
	quizConsensusMinBudget   = 45 * time.Second
	quizConsensusFastBudget  = 4 * time.Minute
	quizConsensusEscalateMin = 45 * time.Second
	quizConsensusCallCap     = 3 * time.Minute
	quizConsensusShortCall   = 30 * time.Second
	quizSeqPageBudget        = 90 * time.Second
)

// solvePageOpts управляет бюджетом времени и режимом решения одной страницы.
type solvePageOpts struct {
	FastMode bool
	Deadline time.Time
	// Budget — сколько времени разумно потратить на эту страницу/волну.
	// 0 — без отдельного ограничения (только общий solveCtx).
	Budget time.Duration
}

// quizThinkMinBudget — ниже этого бюджета на страницу максимальные
// размышления не включаются: модель не успеет, и попытка сорвётся по таймеру.
const quizThinkMinBudget = 2 * time.Minute

// quizEffort — глубина размышлений для страницы. По умолчанию максимум,
// но в быстром режиме и при тесном бюджете она снижается.
func quizEffort(opts solvePageOpts) string {
	switch {
	case opts.FastMode:
		return "low"
	case opts.Budget > 0 && opts.Budget < quizThinkMinBudget:
		return "medium"
	default:
		return "max"
	}
}

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
	wrong    map[int][]string // slot → ранее проверенные неверные ответы (текстом)
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
	conf   map[int]float64 // slot → уверенность модели 0..1
}

// quizTag — метка для подробного лога: по ней grep'ом находятся человек (tg),
// тест (cmid) и страница попытки (page). Page < 0 — строка про тест целиком.
type quizTag struct {
	TG, CMID int64
	Page     int
}

const quizNoPage = -1

func (t quizTag) String() string {
	if t.Page < 0 {
		return fmt.Sprintf("tg=%d cmid=%d", t.TG, t.CMID)
	}
	return fmt.Sprintf("tg=%d cmid=%d page=%d", t.TG, t.CMID, t.Page)
}

func (s *Syncer) solvePage(ctx context.Context, tag quizTag, c *moodle.Client, ws *websearch.Searcher, chain []llm.Target, course, materials string, questions []moodle.QuizQuestion) (map[int]moodle.QuizAnswer, llm.Result, quizlog.Page, error) {
	return s.solvePageWithOpts(ctx, tag, c, ws, chain, course, materials, questions, solvePageOpts{})
}

// solvePageWithOpts решает страницу: сначала вопросы, найденные в базе проверенных ответов
// (сопоставленные с текущими вариантами по тексту), затем ИИ — только для остальных.
func (s *Syncer) solvePageWithOpts(ctx context.Context, tag quizTag, c *moodle.Client, ws *websearch.Searcher, chain []llm.Target, course, materials string, questions []moodle.QuizQuestion, opts solvePageOpts) (map[int]moodle.QuizAnswer, llm.Result, quizlog.Page, error) {
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
		logx.Warnf("uni", "quiz %s: тип не поддерживается: %s", tag, strings.Join(unsup, ", "))
		return nil, res, pl, fmt.Errorf("решатель не умеет заполнять %s — пройди его руками", strings.Join(unsup, ", "))
	}

	known, rest := s.answersFromBase(tag, questions)
	if len(known) > 0 {
		// Пометка для отчёта: какие слоты взяты из базы, а не от ИИ.
		pl.FromBase = sortedSlots(known)
		pl.Notes = append(pl.Notes, fmt.Sprintf("ответы из базы проверенных: слоты %v", pl.FromBase))
	}
	if len(rest) == 0 {
		logx.Debugf("uni", "quiz %s: вся страница из базы, ИИ не вызываю", tag)
		return known, res, pl, nil
	}

	answers, res, aiPl, err := s.solvePageAI(ctx, tag, c, ws, chain, course, materials, questions, rest, opts)
	aiPl.FromBase = pl.FromBase
	aiPl.Notes = append(pl.Notes, aiPl.Notes...)
	if answers == nil {
		answers = make(map[int]moodle.QuizAnswer, len(known))
	}
	for slot, a := range known {
		answers[slot] = a
	}
	return answers, res, aiPl, err
}

// solvePageAI решает через ИИ вопросы, которых нет в базе: раунды, веб-поиск, проверка ответов.
// all — все вопросы страницы (исходные, для хэшей базы), questions — те, что решает ИИ.
func (s *Syncer) solvePageAI(ctx context.Context, tag quizTag, c *moodle.Client, ws *websearch.Searcher, chain []llm.Target, course, materials string, all, questions []moodle.QuizQuestion, opts solvePageOpts) (map[int]moodle.QuizAnswer, llm.Result, quizlog.Page, error) {
	var pl quizlog.Page
	var res llm.Result
	fast := opts.FastMode
	if !fast && !opts.Deadline.IsZero() && time.Until(opts.Deadline) < quizFastModeRemaining {
		fast = true
	}
	if fast {
		ws = nil
		logx.Debugf("uni", "quiz %s: быстрый режим (без веб-поиска и уточнений)", tag)
	}

	qs := s.describeQuestionImages(ctx, tag, c, questions, &pl)
	logx.Debugf("uni", "quiz %s: вопросов %d, цепочка ИИ %d, материалы %d симв.", tag, len(qs), len(chain), len([]rune(materials)))

	pc := &pageCtx{web: map[int]webFinding{}, opened: map[string]string{}, allowed: map[string]bool{}, queried: map[string]bool{}, feedback: map[int]string{}, wrong: map[int][]string{}}
	wrongAll := s.wrongAnswersFor(tag, all)
	for slot, list := range wrongAll {
		for _, w := range list {
			pc.wrong[slot] = append(pc.wrong[slot], w.text)
		}
	}
	if ws != nil {
		queries := map[int]string{}
		for _, q := range qs {
			if qq := quizQuery(q); qq != "" {
				queries[q.Slot] = qq
				pc.queried[qq] = true
			}
		}
		found, logs := s.research(ctx, tag, ws, queries)
		pc.addFindings(found)
		pl.Search = append(pl.Search, logs...)
	} else {
		logx.Debugf("uni", "quiz %s: веб-поиск выключен", tag)
	}

	final := map[int]moodle.QuizAnswer{}
	refined := map[int]bool{}
	rounds := 0
	for round := 0; ; round++ {
		rn := round + 1
		rounds = rn
		ans, ask, r, err := s.askPage(ctx, tag, rn, chain, course, materials, qs, pc, opts)
		pl.Attempts = append(pl.Attempts, r.Attempts...)
		s.noteConfidence(&pl, ask)
		if r.Text != "" {
			pl.Raw += fmt.Sprintf("--- раунд %d ---\n%s\n", rn, r.Text)
		}
		if err != nil {
			if round == 0 {
				return nil, r, pl, err
			}
			// Уточнение упало — остаёмся с ответами прошлых раундов.
			logx.Warnf("uni", "quiz %s: раунд %d: %v", tag, rn, err)
			break
		}
		res = r
		for _, q := range qs {
			a, ok := ans[q.Slot]
			if !ok {
				continue
			}
			if round > 0 {
				refined[q.Slot] = true
				if prev, had := final[q.Slot]; had {
					if was, now := formatAnswerText(q, prev), formatAnswerText(q, a); was != now {
						logx.Debugf("uni", "quiz %s: раунд %d: слот %d поменялся: %q → %q", tag, rn, q.Slot, was, now)
					}
				}
			}
			final[q.Slot] = a
		}
		bad := validateAnswers(qs, final)
		logValidation(tag, rn, qs, bad)
		if fast {
			logx.Debugf("uni", "quiz %s: раунд %d: быстрый режим, без уточнений", tag, rn)
			break
		}
		if round >= quizMaxRounds {
			logx.Debugf("uni", "quiz %s: раунд %d: лимит раундов (%d), стоп", tag, rn, quizMaxRounds)
			break
		}
		if !opts.Deadline.IsZero() && time.Until(opts.Deadline) < quizFastModeRemaining {
			logx.Debugf("uni", "quiz %s: раунд %d: мало времени до дедлайна, без уточнений", tag, rn)
			break
		}
		progressed := len(bad) > 0
		pc.feedback = bad
		if ws != nil && s.followUp(ctx, tag, ws, pc, ask, &pl) {
			progressed = true
		}
		if !progressed {
			logx.Debugf("uni", "quiz %s: раунд %d: уточнять нечего, стоп", tag, rn)
			break
		}
		logx.Debugf("uni", "quiz %s: раунд %d: уточнение, непринятых слотов %d", tag, rn, len(bad))
	}
	// Отрицательная память: ответ, совпавший с известным неверным, — один повтор запроса
	// только для таких вопросов. Если и повтор даёт тот же ответ, отправляем его и пишем в журнал.
	if conf := conflictingSlots(qs, final, wrongAll); len(conf) > 0 {
		var again []moodle.QuizQuestion
		for _, q := range qs {
			for _, slot := range conf {
				if q.Slot == slot {
					again = append(again, q)
					pc.feedback[slot] = "этот ответ уже проверен как НЕВЕРНЫЙ — дай другой вариант"
				}
			}
		}
		logx.Infof("uni", "quiz %s: слоты %v совпали с известными неверными ответами, повторяю запрос для них", tag, conf)
		rn := rounds + 1
		ans2, ask2, r2, err := s.askPage(ctx, tag, rn, chain, course, materials, again, pc, opts)
		pl.Attempts = append(pl.Attempts, r2.Attempts...)
		s.noteConfidence(&pl, ask2)
		if err != nil {
			logx.Warnf("uni", "quiz %s: повтор для неверных ответов: %v", tag, err)
		} else {
			for slot, a := range ans2 {
				final[slot] = a
			}
			if still := conflictingSlots(again, final, wrongAll); len(still) > 0 {
				logx.Warnf("uni", "quiz %s: слоты %v снова дают известный неверный ответ, отправляю как есть", tag, still)
			}
		}
		rounds = rn
	}
	for slot := range refined {
		pl.Refined = append(pl.Refined, slot)
	}
	sort.Ints(pl.Refined)
	if len(pl.Refined) > 0 {
		logx.Debugf("uni", "quiz %s: на уточнении поменялись слоты %v", tag, pl.Refined)
	}

	if bad := validateAnswers(qs, final); len(bad) > 0 {
		var why []string
		for _, q := range qs {
			if w, ok := bad[q.Slot]; ok {
				why = append(why, fmt.Sprintf("вопрос %d — %s", q.Slot, w))
			}
		}
		logx.Debugf("uni", "quiz %s: страница не решена за %d раунд(ов): %s", tag, rounds, strings.Join(why, "; "))
		return final, res, pl, fmt.Errorf("нет годного ответа: %s", strings.Join(why, "; "))
	}
	logx.Debugf("uni", "quiz %s: страница решена за %d раунд(ов), ответов %d", tag, rounds, len(final))
	return final, res, pl, nil
}

// logValidation — подробный лог результата validateAnswers по раунду:
// какие слоты годны и почему остальные нет.
func logValidation(tag quizTag, round int, qs []moodle.QuizQuestion, bad map[int]string) {
	var ok []int
	var fails []string
	for _, q := range qs {
		if why, ko := bad[q.Slot]; ko {
			fails = append(fails, fmt.Sprintf("слот %d — %s", q.Slot, why))
		} else {
			ok = append(ok, q.Slot)
		}
	}
	none := "нет"
	if len(fails) > 0 {
		none = strings.Join(fails, "; ")
	}
	logx.Debugf("uni", "quiz %s: раунд %d: проверка: годны слоты %v; не годны: %s", tag, round, ok, none)
}

// followUp выполняет просьбы модели: новые запросы и чтение страниц.
// true — появилось что-то новое.
func (s *Syncer) followUp(ctx context.Context, tag quizTag, ws *websearch.Searcher, pc *pageCtx, ask pageAsk, pl *quizlog.Page) bool {
	got := false
	queries := map[int]string{}
	for slot, q := range ask.search {
		if !pc.queried[q] {
			queries[slot] = q
			pc.queried[q] = true
		} else {
			logx.Debugf("uni", "quiz %s: поиск slot=%d запрос=%q уже был, пропуск", tag, slot, q)
		}
	}
	if len(queries) > 0 {
		found, logs := s.research(ctx, tag, ws, queries)
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
			logx.Debugf("uni", "quiz %s: открыть %s — лимит %d страниц за раунд, пропуск", tag, u, quizMaxOpens)
			break
		}
		if !pc.allowed[u] {
			logx.Debugf("uni", "quiz %s: открыть %s — адреса нет в выдаче, пропуск", tag, u)
			continue // модель выдумала адрес или он не из выдачи
		}
		if _, done := pc.opened[u]; done {
			logx.Debugf("uni", "quiz %s: открыть %s — уже открывали, пропуск", tag, u)
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
			logx.Debugf("uni", "quiz %s: открыть %s: ошибка: %v", tag, u, err)
		} else {
			l.Hits = 1
			pc.opened[u] = text
			got = true
			logx.Debugf("uni", "quiz %s: открыта страница %s: %d симв.", tag, u, len([]rune(text)))
		}
		pl.Search = append(pl.Search, l)
	}
	logx.Debugf("uni", "quiz %s: дополнительный поиск/чтение, есть новое=%v", tag, got)
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
	"- Думай столько, сколько нужно: разбери условие, реши задачу по шагам и проверь результат обратным действием (декодируй код обратно, подставь ответ в формулу, пересчитай биты). В JSON попадает только итог, без рассуждений.\n" +
	"- \"confidence\" ставь честно: 0.9+ — только если проверил и сомнений не осталось; 0.5 — догадка; ниже — почти не уверен. Низкая уверенность лучше выдуманного ответа.\n" +
	"- Отвечай на КАЖДЫЙ вопрос. Отказ, «невозможно решить», «нет данных» — запрещены: если данных мало, реши сам по своим знаниям и дай самый вероятный ответ с низкой confidence.\n" +
	"- Задачи на коды (Шеннон-Фано, Хаффман, Элиас, декодирование) решай сам по шагам в уме и выдавай только результат.\n" +
	"- Материалы лекций главнее интернета. В найденных банках ответов варианты бывают в другом порядке или от другого варианта теста — сверяй по тексту, а не по букве.\n" +
	"- Если в условии есть раздел «Формат ответа» или «Пример» — ответ СТРОГО в этом формате, как в примере: те же разделители, та же запись, то же число элементов. Пример — образец формы, его числа не копируй.\n" +
	"- Если в вопросе указано, что уже проверено и НЕВЕРНО, такой ответ не предлагай: дай другой.\n" +
	"- Если ответ из прошлого раунда не принят, ниже указана причина — исправь."

// askPage — один раунд: промпт со всем, что известно, ответ модели,
// разбор JSON (с одной попыткой починки) и приведение ключей.
func (s *Syncer) askPageDirect(ctx context.Context, tag quizTag, round int, chain []llm.Target, course, materials string, qs []moodle.QuizQuestion, pc *pageCtx) (map[int]moodle.QuizAnswer, pageAsk, llm.Result, error) {
	var ask pageAsk
	user := buildQuizPrompt(course, materials, qs, pc)
	logx.Debugf("uni", "quiz %s: раунд %d: промпт %d симв. (вопросов %d, цепочка %d)", tag, round, len([]rune(user)), len(qs), len(chain))
	dumpPrompt(tag, round, "", user)
	res, err := llm.Ask(ctx, chain, quizSysPrompt, user)
	logLLM(tag, round, "", res)
	if err != nil {
		return nil, ask, res, fmt.Errorf("ask llm: %w", err)
	}
	dumpReply(tag, round, "", res.Text)
	parsed, perr := extractQuizJSON(res.Text)
	if perr != nil {
		// Модель ответила рассуждением — просим ещё раз только JSON.
		logx.Warnf("uni", "quiz %s: раунд %d: ответ не JSON (%v), переспрашиваю", tag, round, perr)
		repairUser := user +
			"\n\nВНИМАНИЕ: твой прошлый ответ был не JSON. Верни ТОЛЬКО JSON-объект {\"answers\": [...]} по схеме, без пояснений и без отказов."
		dumpPrompt(tag, round, "-repair", repairUser)
		res2, err2 := llm.Ask(ctx, chain, quizSysPrompt, repairUser)
		logLLM(tag, round, " (повтор «только JSON»)", res2)
		dumpReply(tag, round, "-repair", res2.Text)
		res2.Attempts = append(res.Attempts, res2.Attempts...)
		res2.Text = res.Text + "\n--- повтор: только JSON ---\n" + res2.Text
		res = res2
		if err2 != nil {
			return nil, ask, res, fmt.Errorf("ask llm: %w", err2)
		}
		if parsed, perr = extractQuizJSON(res2.Text); perr != nil {
			logx.Warnf("uni", "quiz %s: раунд %d: повтор тоже не JSON: %v", tag, round, perr)
			return nil, ask, res, fmt.Errorf("ответ ИИ не JSON: %w", perr)
		}
		logx.Debugf("uni", "quiz %s: раунд %d: JSON разобран после повтора, ответов %d", tag, round, len(parsed.Answers))
	} else {
		logx.Debugf("uni", "quiz %s: раунд %d: JSON разобран сразу, ответов %d", tag, round, len(parsed.Answers))
	}

	bySlot := map[int]moodle.QuizQuestion{}
	for _, q := range qs {
		bySlot[q.Slot] = q
	}
	out := map[int]moodle.QuizAnswer{}
	ask.search = map[int]string{}
	ask.conf = map[int]float64{}
	for _, a := range parsed.Answers {
		q, ok := bySlot[a.Slot]
		if !ok {
			logx.Debugf("uni", "quiz %s: раунд %d: ответ на несуществующий слот %d, пропуск", tag, round, a.Slot)
			continue
		}
		out[a.Slot] = normalizeAnswer(q, a)
		if a.Confidence != nil {
			ask.conf[a.Slot] = *a.Confidence
		}
		logAnswer(tag, round, q, a, out[a.Slot])
		if sq := strings.TrimSpace(a.Search); sq != "" {
			ask.search[a.Slot] = sq
		}
		for _, u := range a.Open {
			if u = strings.TrimSpace(u); u != "" {
				ask.open = append(ask.open, u)
			}
		}
	}
	for _, q := range qs {
		if sq, ok := ask.search[q.Slot]; ok {
			logx.Debugf("uni", "quiz %s: раунд %d: модель просит поиск slot=%d запрос=%q", tag, round, q.Slot, sq)
		}
	}
	for _, u := range ask.open {
		logx.Debugf("uni", "quiz %s: раунд %d: модель просит открыть %s", tag, round, u)
	}
	return out, ask, res, nil
}

// dumpPrompt — снимок промпта раунда (system + user) в подробный лог.
// stage — "" или "-repair" для повторного запроса «только JSON».
func dumpPrompt(tag quizTag, round int, stage, user string) {
	if !logx.Verbose() {
		return
	}
	logx.Dump("quiz-llm", fmt.Sprintf("%s-r%d%s-prompt", tag, round, stage),
		[]byte("=== system ===\n"+quizSysPrompt+"\n\n=== user ===\n"+user))
}

// dumpReply — снимок сырого ответа модели раунда в подробный лог.
func dumpReply(tag quizTag, round int, stage, reply string) {
	if !logx.Verbose() {
		return
	}
	logx.Dump("quiz-llm", fmt.Sprintf("%s-r%d%s-reply", tag, round, stage), []byte(reply))
}

// logLLM — какая модель ответила и что было с каждой попыткой цепочки.
// Ошибки попыток уже очищены от ключей в пакете llm.
func logLLM(tag quizTag, round int, stage string, res llm.Result) {
	used := "никто"
	if res.Used != nil {
		used = res.Used.String()
	}
	var b strings.Builder
	for _, a := range res.Attempts {
		fmt.Fprintf(&b, " [%s за %s", a.Target, a.Took.Round(time.Millisecond))
		if a.Err != "" {
			fmt.Fprintf(&b, " ошибка: %s", a.Err)
		}
		b.WriteString("]")
	}
	logx.Debugf("uni", "quiz %s: раунд %d%s: ответила модель %s, ответ %d симв., попыток %d:%s",
		tag, round, stage, used, len([]rune(res.Text)), len(res.Attempts), b.String())
}

// logAnswer — ответ модели по слоту до приведения к ключам Moodle и после.
func logAnswer(tag quizTag, round int, q moodle.QuizQuestion, raw llmAnswerItem, ans moodle.QuizAnswer) {
	rawJSON, _ := json.Marshal(raw)
	logx.Debugf("uni", "quiz %s: раунд %d: слот %d (%s): было %s → стало %q [choice=%q choices=%v matches=%v]",
		tag, round, q.Slot, q.QType, rawJSON, formatAnswerText(q, ans), ans.Choice, ans.Choices, ans.Matches)
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
		if fb := formatSection(q.Text); fb != "" {
			fmt.Fprintf(&b, "ФОРМАТ ОТВЕТА из условия (ответ строго в этом формате, как в примере):\n%s\n", fb)
		}
		if pc != nil {
			writeFindings(&b, pc.web[q.Slot])
			if list := pc.wrong[q.Slot]; len(list) > 0 {
				fmt.Fprintf(&b, "УЖЕ ПРОВЕРЕНО: неверно, такой ответ не предлагай: %s\n", strings.Join(list, " | "))
			}
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
// Картинки описываются параллельно пулом воркеров, а результаты записываются
// обратно в исходном детерминированном порядке.
func (s *Syncer) describeQuestionImages(ctx context.Context, tag quizTag, c *moodle.Client, qs []moodle.QuizQuestion, pl *quizlog.Page) []moodle.QuizQuestion {
	out := make([]moodle.QuizQuestion, len(qs))
	copy(out, qs)

	// Распознавание рисунка — переписывание содержимого, а не рассуждение:
	// глубокие размышления здесь только съедают таймер теста.
	ctx = llm.WithGenOpts(ctx, llm.GenOpts{ReasoningEffort: "low"})

	type imgJob struct {
		qIdx   int
		imgIdx int
		slot   int
		src    string
	}
	type imgResult struct {
		desc string
		err  error
	}

	var jobs []imgJob
	for i := range out {
		limit := min(len(out[i].Images), quizMaxImages)
		for n := 0; n < limit; n++ {
			jobs = append(jobs, imgJob{
				qIdx:   i,
				imgIdx: n,
				slot:   out[i].Slot,
				src:    out[i].Images[n],
			})
		}
	}
	if len(jobs) == 0 {
		return out
	}

	results := make([]imgResult, len(jobs))
	workers := min(quizImageWorkers, len(jobs))
	if workers == 1 {
		for k, j := range jobs {
			results[k].desc, results[k].err = s.describeImage(ctx, c, j.src)
		}
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					k := int(next.Add(1) - 1)
					if k >= len(jobs) {
						return
					}
					desc, err := s.describeImage(ctx, c, jobs[k].src)
					results[k] = imgResult{desc: desc, err: err}
				}
			}()
		}
		wg.Wait()
	}

	for k, j := range jobs {
		q := &out[j.qIdx]
		r := results[k]
		if r.err != nil {
			logx.Warnf("uni", "quiz %s: рисунок слота %d #%d: %v", tag, j.slot, j.imgIdx+1, r.err)
			logx.Debugf("uni", "quiz %s: рисунок слота %d #%d %s: не распознан: %v", tag, j.slot, j.imgIdx+1, j.src, r.err)
			pl.Notes = append(pl.Notes, fmt.Sprintf("вопрос %d, рисунок %d: %v", j.slot, j.imgIdx+1, r.err))
			q.Text += fmt.Sprintf("\nРисунок %d: (не удалось распознать)", j.imgIdx+1)
			continue
		}
		logx.Debugf("uni", "quiz %s: рисунок слота %d #%d %s: распознан, %d симв., начало: %q",
			tag, j.slot, j.imgIdx+1, j.src, len([]rune(r.desc)), truncRunes(r.desc, 300))
		pl.Notes = append(pl.Notes, fmt.Sprintf("вопрос %d, рисунок %d распознан (%d симв.)", j.slot, j.imgIdx+1, len([]rune(r.desc))))
		q.Text += fmt.Sprintf("\nРисунок %d:\n%s", j.imgIdx+1, r.desc)
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

// openPageLookupIP и openPageDialer — точки подмены в тестах для проверки
// DNS-резолва и редиректов без реальных сетевых запросов.
var (
	openPageLookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return net.DefaultResolver.LookupIPAddr(ctx, host)
	}
	openPageDialer = safeDialContext
)

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	// CGNAT 100.64.0.0/10 (RFC 6598)
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

func validatePageURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("плохой адрес")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return errors.New("плохой адрес")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return errors.New("внутренний адрес")
		}
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".arpa") {
		return errors.New("внутренний адрес")
	}
	return nil
}

func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IPAddr{{IP: ip}}
	} else {
		ips, err = openPageLookupIP(ctx, host)
		if err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("адрес не резолвится")
	}
	for _, ipa := range ips {
		if isBlockedIP(ipa.IP) {
			return nil, errors.New("внутренний адрес")
		}
	}
	dialer := &net.Dialer{Timeout: quizSearchTimeout}
	var lastErr error
	for _, ipa := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ipa.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("не удалось подключиться")
}

// openPage — текст страницы из выдачи. Только http(s) и не внутренние адреса:
// адрес приходит от модели, это не должен быть путь в локальную сеть VDS.
func openPage(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("плохой адрес")
	}
	if err := validatePageURL(u); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.6")
	client := &http.Client{
		Timeout: quizSearchTimeout,
		Transport: &http.Transport{
			DialContext: openPageDialer,
		},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) > quizMaxRedirects {
				return errors.New("слишком много редиректов")
			}
			if err := validatePageURL(r.URL); err != nil {
				return err
			}
			if host := r.URL.Hostname(); net.ParseIP(host) == nil {
				if ips, err := openPageLookupIP(r.Context(), host); err == nil {
					for _, ipa := range ips {
						if isBlockedIP(ipa.IP) {
							return errors.New("внутренний адрес")
						}
					}
				}
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
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

// noteConfidence переносит уверенность модели по слотам в отчёт страницы.
func (s *Syncer) noteConfidence(pl *quizlog.Page, ask pageAsk) {
	for slot, c := range ask.conf {
		if pl.Confidence == nil {
			pl.Confidence = map[int]float64{}
		}
		pl.Confidence[slot] = c
	}
}

// formatMarkerRe — начало раздела «Формат ответа» или «Пример» в условии: слово в начале или
// после разделителя и двоеточие/скобка/цифра/конец строки после него. «Например» и
// «приведите пример кодирования» не совпадают.
var formatMarkerRe = regexp.MustCompile(`(?i)(?:^|[\s.!?])(?:формат[а-я]*(?:\s+ответ[а-я]*)?|пример[а-я]*)\s*(?::|\(|\d|$)`)

// formatSection — раздел условия от «Формат …»/«Пример …» до конца: его модель должна выполнить
// буквально. Пустая строка, если раздела нет.
func formatSection(text string) string {
	loc := formatMarkerRe.FindStringIndex(text)
	if loc == nil {
		return ""
	}
	return strings.TrimSpace(text[loc[0]:])
}
