package uni

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
)

// Консенсус: один вопрос решают несколько моделей на РАЗНЫХ API-ключах из
// общего пула. Ответ выбирается, только когда за него отдано >= 50% голосов
// и он строго лидирует. Если консенсуса нет — добавляем следующий ключ
// (4-й, 5-й…), пока не кончится пул.
// consensusWinner — зафиксированный победитель слота. Ответ и ключ
// замораживаются в момент консенсуса: последующие голоса 4-го/5-го ключа
// не должны переписывать уже принятое решение.
type consensusWinner struct {
	target llm.Target
	key    string
	answer moodle.QuizAnswer
	conf   float64
	n      int
	total  int
}

type consensusVote struct {
	total   int
	counts  map[string]int
	answers map[string]moodle.QuizAnswer
	targets map[string]llm.Target
	conf    map[string]float64
	// weights — сумма весов голосов за вариант: вес = max(conf, floor),
	// при отсутствии confidence вес 1. Именно она решает победителя.
	weights map[string]float64
}

func newConsensusVote() *consensusVote {
	return &consensusVote{
		counts:  map[string]int{},
		answers: map[string]moodle.QuizAnswer{},
		targets: map[string]llm.Target{},
		conf:    map[string]float64{},
		weights: map[string]float64{},
	}
}

// askPage — обёртка: если в общем пуле достаточно разных ключей, решает
// консенсусом, иначе работает прежняя одиночная цепочка.
func (s *Syncer) askPage(ctx context.Context, tag quizTag, round int, chain []llm.Target, course, materials string, qs []moodle.QuizQuestion, pc *pageCtx, opts solvePageOpts) (map[int]moodle.QuizAnswer, pageAsk, llm.Result, error) {
	// Глубина размышлений задаётся здесь: через эту функцию идут все запросы
	// решателя (консенсус, обычная цепочка и аварийный добор).
	ctx = llm.WithGenOpts(ctx, llm.GenOpts{ReasoningEffort: quizEffort(opts)})
	cfg := s.getCfg()
	minVotes, maxKeys := consensusLimits(cfg)
	minVotes = consensusVotesForBudget(minVotes, opts.Budget)
	directChain := chain
	if opts.Budget != 0 && opts.Budget < quizConsensusFastBudget {
		// На коротком тесте перебирать 17 моделей нельзя: ставим быстрые
		// (flash-lite → flash → hydrogen) и ограничиваем хвост.
		directChain = quickTargets(chain, 6)
	}
	if minVotes <= 0 {
		if cfg != nil && cfg.QuizConsensus > 0 && opts.Budget > 0 {
			logx.Infof("uni", "quiz %s: раунд %d: бюджет страницы %s < %s — консенсус пропускаю, одна быстрая цепочка",
				tag, round, opts.Budget.Round(time.Second), quizConsensusMinBudget)
		}
		return s.askPageDirectBudget(ctx, tag, round, directChain, course, materials, qs, pc, opts.Budget)
	}
	keys := len(llm.DistinctKeyTargets(chain, 0))
	if keys < minVotes {
		logx.Warnf("uni", "quiz %s: консенсус выключен: в пуле %d ключей, нужно %d — решаю одной цепочкой", tag, keys, minVotes)
		return s.askPageDirectBudget(ctx, tag, round, directChain, course, materials, qs, pc, opts.Budget)
	}
	start := time.Now()
	answers, ask, res, err := s.askPageConsensus(ctx, tag, round, chain, course, materials, qs, pc, minVotes, maxKeys, opts)
	// Консенсус не дал ответов на все слоты — добираем одной обычной цепочкой.
	// При Budget == 0 (тест без таймера) отката раньше не было вовсе: страница
	// оставалась без ответа и не сдавалась. Ограничиваем такой добор по времени,
	// чтобы одна страница не заняла весь прогон.
	if err != nil || len(answers) < len(qs) {
		fallback := quizFallbackBudget
		if opts.Budget > 0 {
			fallback = opts.Budget - time.Since(start)
			if fallback <= 15*time.Second {
				fallback = 0
			}
		}
		if fallback != 0 || opts.Budget == 0 {
			logx.Warnf("uni", "quiz %s: раунд %d: консенсус дал %d/%d ответов за %s — добираю одной цепочкой (бюджет %s)",
				tag, round, len(answers), len(qs), time.Since(start).Round(time.Second), fallback.Round(time.Second))
			dAns, dAsk, dRes, derr := s.askPageDirectBudget(ctx, tag, round, directChain, course, materials, qs, pc, fallback)
			merged := mergeFallbackAnswers(answers, dAns)
			if len(merged) > len(answers) || (derr == nil && len(dAns) > 0) {
				return merged, dAsk, dRes, nil
			}
			logx.Warnf("uni", "quiz %s: раунд %d: аварийная цепочка тоже не добила ответы: %v", tag, round, derr)
		}
	}
	return answers, ask, res, err
}

// quizFallbackBudget — предел добора одной цепочкой, когда у страницы нет
// собственного бюджета (тест без таймера). Без предела перебор 17 моделей
// мог бы идти очень долго.
const quizFallbackBudget = 3 * time.Minute

// Взвешивание по уверенности и проверка спорных слотов.
const (
	// quizVoteWeightFloor — нижняя граница веса голоса: даже «не уверен»
	// (confidence 0.05) не должен полностью исчезать из подсчёта.
	quizVoteWeightFloor = 0.25
	// quizVerifyMaxSlots — сколько спорных слотов за раунд отправляем
	// верификатору. Больше — лишние вызовы и риск не успеть к дедлайну.
	quizVerifyMaxSlots = 3
	// quizVerifyMinMargin — если доля лидера минус доля второго меньше,
	// слот считается спорным, даже когда формально победитель уже есть.
	quizVerifyMinMargin = 0.2
	// quizVerifyMinBudget — минимальный остаток бюджета страницы, чтобы
	// вообще звать верификатора.
	quizVerifyMinBudget = 45 * time.Second
	// quizVerifyCallCap — предел одного вызова верификатора.
	quizVerifyCallCap = 90 * time.Second
)

// verifySysPrompt — системная инструкция независимого проверяющего.
const verifySysPrompt = "Ты — независимый проверяющий ответов на вопросы теста. Тебе дают вопрос и 2–3 кандидата, за которых уже проголосовали другие модели.\n" +
	"Реши вопрос сам, затем выбери ОДИН кандидат, наиболее близкий к верному ответу. Учитывай текст вариантов, а не их номера.\n" +
	"Отвечай СТРОГО одним JSON-объектом без текста до и после:\n" +
	"{\"pick\": 1, \"confidence\": 0.8, \"why\": \"кратко, почему\"}\n" +
	"\"pick\" — номер кандидата из списка (с 1), \"confidence\" — от 0 до 1, \"why\" — одно предложение."

// askPageDirectBudget ограничивает обычную цепочку бюджетом страницы, чтобы
// при откате от консенсуса одна страница не съела время остальных волн.
func (s *Syncer) askPageDirectBudget(ctx context.Context, tag quizTag, round int, chain []llm.Target, course, materials string, qs []moodle.QuizQuestion, pc *pageCtx, budget time.Duration) (map[int]moodle.QuizAnswer, pageAsk, llm.Result, error) {
	if budget <= 0 {
		return s.askPageDirect(ctx, tag, round, chain, course, materials, qs, pc)
	}
	dctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return s.askPageDirect(dctx, tag, round, chain, course, materials, qs, pc)
}

// mergeFallbackAnswers: консенсусные ответы главнее, direct заполняет пропуски.
func mergeFallbackAnswers(consensus, direct map[int]moodle.QuizAnswer) map[int]moodle.QuizAnswer {
	out := make(map[int]moodle.QuizAnswer, len(consensus)+len(direct))
	for slot, a := range direct {
		out[slot] = a
	}
	for slot, a := range consensus {
		out[slot] = a
	}
	return out
}

// quickTargets — копия цепочки, отсортированная по ожидаемой скорости модели.
// Нужна, когда до дедлайна мало времени: lite/flash успевают, hydrogen — не
// всегда, cheaper/muse — почти никогда.
func quickTargets(chain []llm.Target, max int) []llm.Target {
	out := append([]llm.Target(nil), chain...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := targetSpeed(out[i]), targetSpeed(out[j])
		if ri != rj {
			return ri < rj
		}
		return out[i].String() < out[j].String()
	})
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

func targetSpeed(t llm.Target) int {
	m := strings.ToLower(t.Model)
	switch {
	case strings.Contains(m, "flash-lite"), strings.Contains(m, "lite"):
		return 0
	case strings.Contains(m, "flash"):
		return 1
	case strings.Contains(m, "hydrogen"):
		return 2
	case strings.Contains(m, "muse"), strings.Contains(m, "cheaper"):
		return 9
	default:
		return 5
	}
}

// consensusVotesForBudget: на коротком тесте 3 параллельных запроса могут не
// успеть. Ниже порога возвращаем 0 — это команда «решай обычной цепочкой».
func consensusVotesForBudget(configured int, budget time.Duration) int {
	if configured <= 0 {
		return 0
	}
	if budget != 0 && budget < quizConsensusMinBudget {
		return 0
	}
	return configured
}

func consensusLimits(cfg *config.Config) (int, int) {
	if cfg == nil {
		return 0, 0
	}
	return cfg.QuizConsensus, cfg.QuizConsensusMax
}

// askPageConsensus — один раунд голосования. Возвращает выбранные ответы,
// объединённые запросы поиска/страниц и общий Result для журнала.
func (s *Syncer) askPageConsensus(ctx context.Context, tag quizTag, round int, chain []llm.Target, course, materials string, qs []moodle.QuizQuestion, pc *pageCtx, minVotes, maxKeys int, opts solvePageOpts) (map[int]moodle.QuizAnswer, pageAsk, llm.Result, error) {
	var outAsk pageAsk
	var res llm.Result
	var pageDeadline time.Time
	callCap := quizConsensusCallCap
	if opts.Budget > 0 {
		pageDeadline = time.Now().Add(opts.Budget)
		if opts.Budget < 2*time.Minute {
			callCap = quizConsensusShortCall
		}
	}
	if minVotes < 3 {
		minVotes = 3
	}
	if maxKeys < minVotes {
		maxKeys = minVotes
	}
	selection := chain
	if opts.Budget > 0 && opts.Budget < quizConsensusFastBudget {
		// На коротком/среднем тесте берём самые быстрые модели, иначе
		// hydrogen/muse могут съесть весь дедлайн.
		selection = quickTargets(chain, 0)
	}
	cands := llm.DistinctKeyTargets(selection, maxKeys)
	if len(cands) < minVotes {
		return s.askPageDirect(ctx, tag, round, quickTargets(chain, 6), course, materials, qs, pc)
	}

	user := buildQuizPrompt(course, materials, qs, pc)
	names := make([]string, 0, len(cands))
	for _, t := range cands {
		names = append(names, t.String())
	}
	logx.Infof("uni", "quiz %s: раунд %d: консенсус: промпт %d симв., вопросов %d, минимум %d ключей, бюджет %s, кандидаты [%s]",
		tag, round, len([]rune(user)), len(qs), minVotes, opts.Budget.Round(time.Second), strings.Join(names, " > "))
	dumpPrompt(tag, round, "-consensus", user)

	bySlot := map[int]moodle.QuizQuestion{}
	for _, q := range qs {
		bySlot[q.Slot] = q
	}
	votes := map[int]*consensusVote{}
	for _, q := range qs {
		votes[q.Slot] = newConsensusVote()
	}
	outAsk = pageAsk{search: map[int]string{}, conf: map[int]float64{}}
	seenOpen := map[string]bool{}
	used := 0
	okSamples := 0
	throttled := 0
	var lastErr error
	winners := map[int]consensusWinner{}
	resolved := func() bool {
		for _, q := range qs {
			if _, ok := winners[q.Slot]; !ok {
				return false
			}
		}
		return true
	}
	addSample := func(sample consensusSample, idx int) {
		if sample.err != nil {
			lastErr = sample.err
			if sampleThrottled(sample.err) {
				throttled++
			}
			logx.Warnf("uni", "quiz %s: раунд %d: консенсус-модель %s: %v", tag, round, sample.target, sample.err)
			return
		}
		if len(sample.answers) > 0 {
			okSamples++
		}
		res.Attempts = append(res.Attempts, sample.res.Attempts...)
		res.InputTokens += sample.res.InputTokens
		res.OutputTokens += sample.res.OutputTokens
		if sample.res.Text != "" {
			res.Text += fmt.Sprintf("--- %s ---\n%s\n", sample.target, sample.res.Text)
		}
		for slot, a := range sample.answers {
			q, ok := bySlot[slot]
			if !ok {
				continue
			}
			key := answerVoteKey(q, a)
			if key == "" {
				continue
			}
			v := votes[slot]
			if v == nil {
				v = newConsensusVote()
				votes[slot] = v
			}
			v.total++
			v.counts[key]++
			v.answers[key] = a
			v.targets[key] = sample.target
			v.weights[key] += sampleVoteWeight(sample, slot)
			if c, ok := sample.conf[slot]; ok && c > v.conf[key] {
				v.conf[key] = c
			}
		}
		for slot, q := range sample.ask.search {
			if _, ok := outAsk.search[slot]; !ok && q != "" {
				outAsk.search[slot] = q
			}
		}
		for _, u := range sample.ask.open {
			if u != "" && !seenOpen[u] {
				seenOpen[u] = true
				outAsk.open = append(outAsk.open, u)
			}
		}
		logx.Debugf("uni", "quiz %s: раунд %d: консенсус-модель #%d %s ответила на %d слотов", tag, round, idx+1, sample.target, len(sample.answers))
	}

	// Первая тройка — параллельно, чтобы не ждать последовательно.
	batch := minVotes
	if batch > len(cands) {
		batch = len(cands)
	}
	samples := make([]consensusSample, batch)
	var wg sync.WaitGroup
	for i := 0; i < batch; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			samples[i] = s.askOneConsensus(ctx, tag, round, i, cands[i], user, qs, bySlot, pageDeadline, callCap)
		}(i)
	}
	wg.Wait()
	for i := range samples {
		addSample(samples[i], i)
		used++
	}
	s.resolveConsensus(qs, votes, winners, minVotes)

	// Если кто-то не отдал голос или консенсуса нет — добавляем по одному
	// ключу (4-й, 5-й…), а не начинаем всё заново.
	for !resolved() && used < len(cands) {
		if err := ctx.Err(); err != nil {
			return nil, outAsk, res, err
		}
		if okSamples == 0 && throttled == used {
			logx.Warnf("uni", "quiz %s: раунд %d: все %d первых ключей упёрлись в 429/cooldown — не перебираю весь пул", tag, round, used)
			break
		}
		if opts.Budget > 0 && opts.Budget < 2*time.Minute {
			logx.Warnf("uni", "quiz %s: раунд %d: короткий бюджет %s — 4-й ключ и далее не добираю", tag, round, opts.Budget.Round(time.Second))
			break
		}
		if !pageDeadline.IsZero() && time.Until(pageDeadline) < quizConsensusEscalateMin {
			logx.Warnf("uni", "quiz %s: раунд %d: до дедлайна страницы меньше %s — 4-й ключ и далее не добираю", tag, round, quizConsensusEscalateMin)
			break
		}
		i := used
		sample := s.askOneConsensus(ctx, tag, round, i, cands[i], user, qs, bySlot, pageDeadline, callCap)
		addSample(sample, i)
		used++
		s.resolveConsensus(qs, votes, winners, minVotes)
	}

	// Пул кончился. Спорные слоты (нет победителя или слишком тонкий
	// перевес) отдаём независимому верификатору другого семейства моделей:
	// простое большинство на трудных вопросах часто ошибается одинаково.
	s.verifyDisputed(ctx, tag, round, chain, qs, bySlot, votes, winners, &res, pageDeadline)

	// Пул кончился, а порога нет — слот остаётся нерешённым. Это честнее,
	// чем отправить ответ, за который не проголосовало большинство.
	for _, q := range qs {
		if _, ok := winners[q.Slot]; ok {
			continue
		}
		v := votes[q.Slot]
		if v == nil || v.total == 0 {
			logx.Warnf("uni", "quiz %s: раунд %d: слот %d: ни одна консенсус-модель не ответила — пропуск", tag, round, q.Slot)
			continue
		}
		key, n, second := leader(v)
		logx.Warnf("uni", "quiz %s: раунд %d: слот %d: консенсуса нет после %d ключей (лидер %q %d/%d, второй %d) — оставляю без ответа",
			tag, round, q.Slot, used, formatAnswerText(q, v.answers[key]), n, v.total, second)
	}

	final := map[int]moodle.QuizAnswer{}
	slots := make([]int, 0, len(winners))
	for slot := range winners {
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	for _, slot := range slots {
		w := winners[slot]
		final[slot] = w.answer
		if _, ok := outAsk.conf[slot]; !ok && w.conf > 0 {
			outAsk.conf[slot] = w.conf
		}
		if res.Used == nil {
			tt := w.target
			res.Used = &tt
		}
		logx.Debugf("uni", "quiz %s: раунд %d: консенсус слот %d: %q (%d/%d голосов, модель %s)",
			tag, round, slot, formatAnswerText(bySlot[slot], w.answer), w.n, w.total, w.target)
	}
	if len(final) == 0 && okSamples == 0 && used > 0 {
		if lastErr != nil {
			return nil, outAsk, res, fmt.Errorf("консенсус: ни одна модель не ответила: %w", lastErr)
		}
		return nil, outAsk, res, errors.New("консенсус: ни одна модель не ответила")
	}
	return final, outAsk, res, nil
}

// askOneConsensus — один голос: один ключ/модель, JSON и, при необходимости,
// один повтор «верни только JSON». Ошибка не роняет весь консенсус.
func (s *Syncer) askOneConsensus(ctx context.Context, tag quizTag, round, idx int, t llm.Target, user string, qs []moodle.QuizQuestion, bySlot map[int]moodle.QuizQuestion, pageDeadline time.Time, callCap time.Duration) consensusSample {
	sample := consensusSample{target: t, answers: map[int]moodle.QuizAnswer{}, conf: map[int]float64{}}
	stage := fmt.Sprintf("-m%d", idx+1)
	callCtx := ctx
	if !pageDeadline.IsZero() {
		dl := pageDeadline
		if cap := time.Now().Add(callCap); cap.Before(dl) {
			dl = cap
		}
		var cancel context.CancelFunc
		callCtx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}
	res, err := llm.Ask(callCtx, []llm.Target{t}, quizSysPrompt, user)
	logLLM(tag, round, stage, res)
	if err != nil {
		sample.err = err
		sample.res = res
		return sample
	}
	dumpReply(tag, round, stage, res.Text)
	parsed, perr := extractQuizJSON(res.Text)
	if perr != nil {
		logx.Warnf("uni", "quiz %s: раунд %d: консенсус-модель %s: ответ не JSON (%v), прошу только JSON", tag, round, t, perr)
		repairUser := user + "\n\nВНИМАНИЕ: твой прошлый ответ был не JSON. Верни ТОЛЬКО JSON-объект {\"answers\": [...]} по схеме, без пояснений и без отказов."
		dumpPrompt(tag, round, stage+"-repair", repairUser)
		res2, err2 := llm.Ask(callCtx, []llm.Target{t}, quizSysPrompt, repairUser)
		logLLM(tag, round, stage+"-repair", res2)
		res2.Attempts = append(res.Attempts, res2.Attempts...)
		res2.InputTokens += res.InputTokens
		res2.OutputTokens += res.OutputTokens
		res2.Text = res.Text + "\n--- повтор: только JSON ---\n" + res2.Text
		res = res2
		if err2 != nil {
			sample.err = err2
			sample.res = res
			return sample
		}
		dumpReply(tag, round, stage+"-repair", res.Text)
		parsed, perr = extractQuizJSON(res.Text)
		if perr != nil {
			sample.err = fmt.Errorf("ответ ИИ не JSON: %w", perr)
			sample.res = res
			return sample
		}
	}
	sample.res = res
	sample.ask = pageAsk{search: map[int]string{}, conf: map[int]float64{}}
	for _, a := range parsed.Answers {
		q, ok := bySlot[a.Slot]
		if !ok {
			continue
		}
		answer := normalizeAnswer(q, a)
		sample.answers[a.Slot] = answer
		if a.Confidence != nil {
			sample.conf[a.Slot] = *a.Confidence
			sample.ask.conf[a.Slot] = *a.Confidence
		}
		if sq := strings.TrimSpace(a.Search); sq != "" {
			sample.ask.search[a.Slot] = sq
		}
		for _, u := range a.Open {
			if u = strings.TrimSpace(u); u != "" {
				sample.ask.open = append(sample.ask.open, u)
			}
		}
		logAnswer(tag, round, q, a, answer)
	}
	return sample
}

type consensusSample struct {
	target  llm.Target
	answers map[int]moodle.QuizAnswer
	conf    map[int]float64
	ask     pageAsk
	res     llm.Result
	err     error
}

// resolveConsensus фиксирует слоты, где взвешенный по уверенности лидер
// набрал долю не ниже QUIZ_CONSENSUS_SHARE и строго больше любого другого
// ответа. Уже выбранные слоты не переголосуются.
func (s *Syncer) resolveConsensus(qs []moodle.QuizQuestion, votes map[int]*consensusVote, winners map[int]consensusWinner, minVotes int) {
	for _, q := range qs {
		if _, ok := winners[q.Slot]; ok {
			continue
		}
		v := votes[q.Slot]
		if v == nil || v.total == 0 {
			continue
		}
		key, weight, second := leaderWeighted(v)
		if key == "" {
			continue
		}
		totalWeight := voteWeightTotal(v)
		n := v.counts[key]
		// Порог: минимум N голосов, доля веса не ниже QUIZ_CONSENSUS_SHARE
		// (по умолчанию 2/3) и строгий лидер. Исключение: кто-то из моделей
		// упал, но все ответившие (>=2) согласны — это тоже консенсус.
		unanimous := v.total >= 2 && n == v.total
		if v.total < minVotes && !unanimous {
			continue
		}
		if totalWeight <= 0 || weight*100 < float64(s.consensusShare())*totalWeight || weight <= second {
			continue
		}
		if !unanimous && !s.confidentEnough(v, key) {
			continue
		}
		winners[q.Slot] = consensusWinner{
			target: v.targets[key],
			key:    key,
			answer: v.answers[key],
			conf:   v.conf[key],
			n:      n,
			total:  v.total,
		}
	}
}

// sampleVoteWeight — вес одного голоса: уверенность модели, но не ниже
// floor. Модель не сообщила confidence — считаем голос полноценным (1).
func sampleVoteWeight(sample consensusSample, slot int) float64 {
	c, ok := sample.conf[slot]
	if !ok {
		return 1
	}
	return voteWeight(c)
}

func voteWeight(conf float64) float64 {
	switch {
	case conf > 1:
		return 1
	case conf < quizVoteWeightFloor:
		return quizVoteWeightFloor
	default:
		return conf
	}
}

// voteWeights — веса вариантов. Если их нет (тесты и старые данные задают
// только counts), веса равны числу голосов.
func voteWeights(v *consensusVote) map[string]float64 {
	out := make(map[string]float64, len(v.counts))
	for k := range v.counts {
		out[k] = float64(v.counts[k])
	}
	var sum float64
	for k, w := range v.weights {
		if w <= 0 {
			continue
		}
		out[k] = w
		sum += w
	}
	if sum <= 0 {
		out = make(map[string]float64, len(v.counts))
		for k, c := range v.counts {
			out[k] = float64(c)
		}
	}
	return out
}

func voteWeightTotal(v *consensusVote) float64 {
	var sum float64
	for _, w := range voteWeights(v) {
		sum += w
	}
	return sum
}

// leaderWeighted — вариант с максимальной суммой весов и вес второго места.
// Обход по отсортированным ключам, чтобы логи и проверки были стабильны.
func leaderWeighted(v *consensusVote) (key string, weight, second float64) {
	ws := voteWeights(v)
	keys := make([]string, 0, len(ws))
	for k := range ws {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := ws[k]
		if c > weight {
			second = weight
			key, weight = k, c
			continue
		}
		if c > second {
			second = c
		}
	}
	return key, weight, second
}

// verifyCandidate — вариант ответа, предложенный верификатору.
type verifyCandidate struct {
	key    string
	answer moodle.QuizAnswer
	weight float64
}

// disputedSlot — слот, который стоит перепроверить: либо победителя нет,
// либо перевес слишком тонкий.
type disputedSlot struct {
	q      moodle.QuizQuestion
	v      *consensusVote
	leader string
	margin float64
	cands  []verifyCandidate
}

// verifyDisputed отправляет до quizVerifyMaxSlots спорных слотов одному
// верификатору из ДРУГОГО семейства моделей. Верификатор выбирает один из
// 2–3 кандидатов; при ошибке или низкой уверенности остаётся взвешенный
// победитель (или слот остаётся нерешённым, как раньше).
func (s *Syncer) verifyDisputed(ctx context.Context, tag quizTag, round int, chain []llm.Target, qs []moodle.QuizQuestion, bySlot map[int]moodle.QuizQuestion, votes map[int]*consensusVote, winners map[int]consensusWinner, res *llm.Result, pageDeadline time.Time) {
	slots := disputedSlots(qs, votes, winners)
	if len(slots) == 0 {
		return
	}
	if len(slots) > quizVerifyMaxSlots {
		slots = slots[:quizVerifyMaxSlots]
	}
	minConf := s.confidenceMin()
	verified := 0
	for _, d := range slots {
		if err := ctx.Err(); err != nil {
			return
		}
		if !pageDeadline.IsZero() && time.Until(pageDeadline) < quizVerifyMinBudget {
			logx.Warnf("uni", "quiz %s: раунд %d: слот %d: до дедлайна меньше %s — верификацию пропускаю",
				tag, round, d.q.Slot, quizVerifyMinBudget)
			return
		}
		verifier, ok := pickVerifier(chain, d.v.targets[d.leader].Provider)
		if !ok {
			logx.Warnf("uni", "quiz %s: раунд %d: слот %d: в цепочке нет модели другого провайдера — верификация невозможна", tag, round, d.q.Slot)
			return
		}
		verified++
		s.verifySlot(ctx, tag, round, verifier, d, bySlot[d.q.Slot], winners, res, pageDeadline, minConf)
	}
	if verified > 0 {
		logx.Infof("uni", "quiz %s: раунд %d: верификатор вызван для %d спорных слотов", tag, round, verified)
	}
}

// disputedSlots — нерешённые слоты (лидер не добрал долю/уверенность) и
// решённые с тонким перевесом. Сначала самые спорные: меньший перевес.
func disputedSlots(qs []moodle.QuizQuestion, votes map[int]*consensusVote, winners map[int]consensusWinner) []disputedSlot {
	var out []disputedSlot
	for _, q := range qs {
		v := votes[q.Slot]
		if v == nil || v.total == 0 {
			continue
		}
		key, weight, second := leaderWeighted(v)
		total := voteWeightTotal(v)
		if key == "" || total <= 0 {
			continue
		}
		cands := topCandidates(v, 3)
		if len(cands) < 2 {
			continue
		}
		_, resolved := winners[q.Slot]
		margin := (weight - second) / total
		if resolved && margin >= quizVerifyMinMargin {
			continue
		}
		out = append(out, disputedSlot{
			q:      q,
			v:      v,
			leader: key,
			margin: margin,
			cands:  cands,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].margin != out[j].margin {
			return out[i].margin < out[j].margin
		}
		return out[i].q.Slot < out[j].q.Slot
	})
	return out
}

// topCandidates — до max вариантов с наибольшим весом.
func topCandidates(v *consensusVote, max int) []verifyCandidate {
	ws := voteWeights(v)
	keys := make([]string, 0, len(ws))
	for k := range ws {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if ws[keys[i]] != ws[keys[j]] {
			return ws[keys[i]] > ws[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if max > 0 && len(keys) > max {
		keys = keys[:max]
	}
	out := make([]verifyCandidate, 0, len(keys))
	for _, k := range keys {
		out = append(out, verifyCandidate{key: k, answer: v.answers[k], weight: ws[k]})
	}
	return out
}

// pickVerifier — самая быстрая модель цепочки, чей провайдер отличается от
// провайдера лидера: независимость важнее скорости, но внутри неё берём
// самую дешёвую по времени.
func pickVerifier(chain []llm.Target, exclude string) (llm.Target, bool) {
	var best llm.Target
	found := false
	for _, t := range chain {
		if t.Provider == exclude || t.Model == "" {
			continue
		}
		if !found || targetSpeed(t) < targetSpeed(best) {
			best, found = t, true
		}
	}
	return best, found
}

// verifySlot — один вызов верификатора на слот. Ошибка не роняет страницу:
// остаётся прежний (взвешенный) победитель.
func (s *Syncer) verifySlot(ctx context.Context, tag quizTag, round int, verifier llm.Target, d disputedSlot, q moodle.QuizQuestion, winners map[int]consensusWinner, res *llm.Result, pageDeadline time.Time, minConf float64) {
	stage := fmt.Sprintf("-verify-%d", d.q.Slot)
	user := buildVerifyPrompt(q, d.cands)
	callCtx := ctx
	if !pageDeadline.IsZero() {
		dl := pageDeadline
		if cap := time.Now().Add(quizVerifyCallCap); cap.Before(dl) {
			dl = cap
		}
		var cancel context.CancelFunc
		callCtx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}
	dumpPrompt(tag, round, stage, user)
	vres, err := llm.Ask(callCtx, []llm.Target{verifier}, verifySysPrompt, user)
	logLLM(tag, round, stage, vres)
	if res != nil {
		res.Attempts = append(res.Attempts, vres.Attempts...)
		res.InputTokens += vres.InputTokens
		res.OutputTokens += vres.OutputTokens
	}
	names := make([]string, 0, len(d.cands))
	for i, c := range d.cands {
		names = append(names, fmt.Sprintf("%d:%s", i+1, formatAnswerText(q, c.answer)))
	}
	if err != nil {
		logx.Warnf("uni", "quiz %s: раунд %d: слот %d: верификатор %s: ошибка (%v) — оставляю взвешенного лидера",
			tag, round, d.q.Slot, verifier, err)
		return
	}
	dumpReply(tag, round, stage, vres.Text)
	reply, perr := extractVerifyReply(vres.Text)
	if perr != nil || reply.Pick < 1 || reply.Pick > len(d.cands) {
		logx.Warnf("uni", "quiz %s: раунд %d: слот %d: верификатор %s: негодный ответ (%v, pick=%d) — оставляю взвешенного лидера",
			tag, round, d.q.Slot, verifier, perr, reply.Pick)
		return
	}
	if res != nil && res.Text != "" {
		res.Text += fmt.Sprintf("--- %s ---\n%s\n", verifier, vres.Text)
	}
	conf := 0.0
	if reply.Confidence != nil {
		conf = *reply.Confidence
	}
	pick := d.cands[reply.Pick-1]
	accepted := conf >= minConf
	old, hadWinner := winners[d.q.Slot]
	if accepted {
		winners[d.q.Slot] = consensusWinner{
			target: verifier,
			key:    pick.key,
			answer: pick.answer,
			conf:   conf,
			n:      d.v.counts[pick.key],
			total:  d.v.total,
		}
		if hadWinner && old.key == pick.key {
			accepted = true
		}
	}
	logx.Infof("uni", "quiz %s: раунд %d: слот %d: верификатор %s: кандидаты [%s], выбор %d (%s, уверенность %.2f), принят=%v: %s",
		tag, round, d.q.Slot, verifier, strings.Join(names, " | "), reply.Pick,
		formatAnswerText(q, pick.answer), conf, accepted, strings.TrimSpace(reply.Why))
}

// verifyReply — ответ верификатора.
type verifyReply struct {
	Pick       int      `json:"pick"`
	Confidence *float64 `json:"confidence"`
	Why        string   `json:"why"`
}

// extractVerifyReply достаёт первый корректный JSON верификатора: модели
// любят обернуть его в ```json``` или дописать рассуждение.
func extractVerifyReply(text string) (verifyReply, error) {
	t := strings.TrimSpace(text)
	var lastErr error
	for start := 0; start < len(t); {
		i := strings.IndexByte(t[start:], '{')
		if i < 0 {
			break
		}
		i += start
		dec := json.NewDecoder(strings.NewReader(t[i:]))
		var cand verifyReply
		if err := dec.Decode(&cand); err == nil && (cand.Pick > 0 || cand.Confidence != nil) {
			return cand, nil
		} else if err != nil {
			lastErr = err
		}
		start = i + 1
	}
	if lastErr == nil {
		lastErr = errors.New("в ответе нет JSON-объекта")
	}
	return verifyReply{}, fmt.Errorf("ответ верификатора не JSON: %w", lastErr)
}

// buildVerifyPrompt — вопрос, его варианты и пронумерованные кандидаты.
func buildVerifyPrompt(q moodle.QuizQuestion, cands []verifyCandidate) string {
	var b strings.Builder
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
	b.WriteString("\nКандидаты (выбери один):\n")
	for i, c := range cands {
		fmt.Fprintf(&b, "%d) %s\n", i+1, formatAnswerText(q, c.answer))
	}
	b.WriteString("\nВерни только JSON: {\"pick\": <номер кандидата>, \"confidence\": <0..1>, \"why\": \"<кратко>\"}\n")
	return b.String()
}

// consensusShare — какую долю голосов (в процентах) должен собрать ответ,
// чтобы считаться принятым. 66 — две трети: 2 из 3 достаточно, 3 из 5 нет.
func (s *Syncer) consensusShare() int {
	cfg := s.getCfg()
	if cfg == nil || cfg.QuizConsensusShare < 50 || cfg.QuizConsensusShare > 100 {
		return 66
	}
	return cfg.QuizConsensusShare
}

// confidenceMin — минимальная уверенность модели в ответе (0..1).
func (s *Syncer) confidenceMin() float64 {
	cfg := s.getCfg()
	if cfg == nil || cfg.QuizConfidenceMin <= 0 || cfg.QuizConfidenceMin > 1 {
		return 0.5
	}
	return cfg.QuizConfidenceMin
}

// confidentEnough — модель сама оценила ответ не ниже порога. Нулевая
// уверенность значит «модель не сообщила»: тогда решает только доля голосов.
func (s *Syncer) confidentEnough(v *consensusVote, key string) bool {
	c := v.conf[key]
	return c <= 0 || c >= s.confidenceMin()
}

func leader(v *consensusVote) (key string, n, second int) {
	for k, c := range v.counts {
		if c > n {
			second = n
			key, n = k, c
			continue
		}
		// Ничья за первое место: второй = столько же, консенсуса не будет.
		if c > second {
			second = c
		}
	}
	return key, n, second
}

func sampleThrottled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, llm.ErrAllCooldown) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") || strings.Contains(msg, "cooldown") || strings.Contains(msg, "rate limit")
}

func answerVoteKey(q moodle.QuizQuestion, a moodle.QuizAnswer) string {
	switch {
	case len(q.Choices) > 0 && q.Choices[0].IsMulti:
		if len(a.Choices) == 0 {
			return ""
		}
		xs := append([]string(nil), a.Choices...)
		sort.Strings(xs)
		return "m:" + strings.Join(xs, ",")
	case len(q.Choices) > 0:
		if a.Choice == "" {
			return ""
		}
		return "c:" + a.Choice
	case len(q.MatchOptions) > 0:
		if len(a.Matches) == 0 {
			return ""
		}
		var xs []string
		for k, v := range a.Matches {
			xs = append(xs, k+"="+v)
		}
		sort.Strings(xs)
		return "match:" + strings.Join(xs, "|")
	default:
		t := normQuizText(a.Text)
		if t == "" {
			return ""
		}
		return "t:" + t
	}
}
