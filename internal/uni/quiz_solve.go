package uni

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizlog"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websearch"
)

const (
	quizPageWorkers      = 4
	quizTimeReserve      = 75 * time.Second
	quizFastModeTimeLeft = 3 * time.Minute
	quizWaveBudget       = 45 * time.Second
)

var (
	timeLeftColonRe = regexp.MustCompile(`\b(\d+):(\d{1,2})(?::(\d{1,2}))?\b`)
	timeLeftUnitRe  = regexp.MustCompile(`(\d+)\s*([a-zа-яё]+)`)
)

// parseTimeLeft разбирает остаток времени из таймера Moodle (#quiz-time-left / #quiz-timer).
func parseTimeLeft(raw string) (time.Duration, bool) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return 0, false
	}
	if m := timeLeftColonRe.FindStringSubmatch(s); len(m) == 4 {
		a, err1 := strconv.Atoi(m[1])
		b, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil {
			if m[3] != "" {
				sec, err3 := strconv.Atoi(m[3])
				if err3 == nil && b < 60 && sec < 60 {
					return time.Duration(a)*time.Hour + time.Duration(b)*time.Minute + time.Duration(sec)*time.Second, true
				}
			} else if b < 60 {
				return time.Duration(a)*time.Minute + time.Duration(b)*time.Second, true
			}
		}
	}
	var total time.Duration
	matched := false
	for _, m := range timeLeftUnitRe.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		unit := m[2]
		switch {
		case unit == "д" || unit == "d" || strings.HasPrefix(unit, "дн") || strings.HasPrefix(unit, "ден") || strings.HasPrefix(unit, "day"):
			total += time.Duration(n) * 24 * time.Hour
			matched = true
		case unit == "ч" || unit == "h" || strings.HasPrefix(unit, "час") || strings.HasPrefix(unit, "hr") || strings.HasPrefix(unit, "hour"):
			total += time.Duration(n) * time.Hour
			matched = true
		case unit == "м" || unit == "m" || strings.HasPrefix(unit, "мин") || strings.HasPrefix(unit, "min"):
			total += time.Duration(n) * time.Minute
			matched = true
		case unit == "с" || unit == "s" || strings.HasPrefix(unit, "сек") || strings.HasPrefix(unit, "sec"):
			total += time.Duration(n) * time.Second
			matched = true
		}
	}
	if !matched {
		return 0, false
	}
	return total, true
}

func (s *Syncer) getCfg() *config.Config {
	if s.Cfg != nil {
		return s.Cfg
	}
	cfg, err := config.Load()
	if err == nil {
		return cfg
	}
	return nil
}

// detectAndHandleOpenedQuizzes проверяет, какие тесты стали открытыми для попыток,
// уведомляет пользователя и запускает парсинг или авторешение через LLM.
func (s *Syncer) detectAndHandleOpenedQuizzes(ctx context.Context, acc model.Account, c *moodle.Client, prev, cur []model.MoodleItem) {
	wakeKicked := isWakeKicked(ctx)
	if !wakeKicked && s.Store != nil {
		wakeKickVal, ok, _ := s.Store.GetSetting(WakeKickKey)
		wakeKicked = ok && wakeKickVal != ""
	}

	// «Пройти» из панели: решаем сейчас, даже если тест просрочен или
	// по нашим эвристикам «не открыт» — пусть Moodle сам скажет, можно ли.
	forced := s.takeForced(acc.TelegramID, prev, cur)
	for _, it := range forced {
		s.processQuiz(ctx, acc, c, it, true)
	}

	// При первой инициализации аккаунта (когда в базе еще нет элементов и нет wake-кика)
	// уведомления не рассылаются, чтобы не спамить всей историей курсов.
	if len(prev) == 0 && !wakeKicked {
		return
	}
	done := map[int64]bool{}
	for _, it := range forced {
		done[it.CMID] = true
	}

	prevMap := make(map[int64]model.MoodleItem, len(prev))
	for _, p := range prev {
		prevMap[p.CMID] = p
	}

	now := s.now()

	for _, it := range cur {
		if it.Kind != model.MoodleQuiz || it.Finished() || done[it.CMID] {
			continue
		}

		isOpenNow := !strings.Contains(it.Todo, "не открыт для попыток") &&
			(it.Opens == nil || !it.Opens.After(now)) &&
			(it.Due == nil || it.Due.After(now))
		if !isOpenNow {
			continue
		}

		p, existed := prevMap[it.CMID]
		wasClosed := !existed || strings.Contains(p.Todo, "не открыт для попыток") || (p.Opens != nil && p.Opens.After(now))
		// Прошлая попытка решения упала (ИИ не ответил) — пробуем снова на
		// каждом обходе, без повторного «Открылся тест».
		failedBefore := false
		if v, ok, _ := s.Store.GetSetting(quizFailKey(acc.TelegramID, it.CMID)); ok && v != "" {
			failedBefore = true
		}
		if wasClosed || wakeKicked {
			s.notify(ctx, acc.TelegramID, "<b>Открылся тест в Moodle!</b>\n"+FormatItem(it, now, s.loc()))
		}
		if wasClosed || wakeKicked || failedBefore {
			s.processQuiz(ctx, acc, c, it, false)
		}
	}
}

// QuizForceKey — «пройти принудительно» (кнопка «Пройти» в панели).
func QuizForceKey(telegramID, cmid int64) string {
	return fmt.Sprintf("quiz_force:%d:%d", telegramID, cmid)
}

// takeForced — тесты с флагом «Пройти» (флаг снимается). Ищем только среди
// текущих и прошлых элементов: флаг на cmid, которого там нет, ждёт, пока тест
// появится в Moodle-списке.
func (s *Syncer) takeForced(id int64, prev, cur []model.MoodleItem) []model.MoodleItem {
	byID := map[int64]model.MoodleItem{}
	for _, it := range prev {
		byID[it.CMID] = it
	}
	for _, it := range cur {
		byID[it.CMID] = it
	}
	var out []model.MoodleItem
	for cmid, it := range byID {
		v, ok, err := s.Store.GetSetting(QuizForceKey(id, cmid))
		if err != nil || !ok || v == "" {
			continue
		}
		_ = s.Store.SetSetting(QuizForceKey(id, cmid), "")
		if it.Kind == "" {
			it.Kind = model.MoodleQuiz
		}
		out = append(out, it)
	}
	return out
}

// processQuiz обрабатывает тест: парсит вопросы, собирает материалы курса
// и при наличии ключа LLM решает тест и отправляет отчёт, либо присылает текст вопросов.
// forced — «Пройти» из панели: почему не вышло, человеку сообщаем всегда.
func (s *Syncer) processQuiz(ctx context.Context, acc model.Account, c *moodle.Client, it model.MoodleItem, forced bool) {
	tag := quizTag{TG: acc.TelegramID, CMID: it.CMID, Page: quizNoPage}
	quizLockID := fmt.Sprintf("%d", it.CMID)
	if !s.TryLockQuiz(acc.TelegramID, quizLockID) {
		logx.Infof("uni", "quiz %s: выход: тест уже решается в другой горутине, пропуск", tag)
		return
	}
	defer s.UnlockQuiz(acc.TelegramID, quizLockID)

	logx.Infof("uni", "quiz %s: вход: курс=%q название=%q forced=%v", tag, it.Course, it.Title, forced)
	solvedKey := fmt.Sprintf("quiz_solved:%d:%d", acc.TelegramID, it.CMID)
	if v, ok, err := s.Store.GetSetting(solvedKey); err == nil && ok && v != "" {
		logx.Infof("uni", "quiz %s: выход: тест уже решён ранее (%s), пропуск", tag, v)
		return
	}
	refuse := func(why string) {
		if !forced {
			return
		}
		title := it.Title
		if title == "" {
			title = fmt.Sprintf("тест %d", it.CMID)
		}
		s.notify(ctx, acc.TelegramID, fmt.Sprintf("<b>⚠️ Не получилось пройти тест</b>\n<b>Тест:</b> %s\n<b>Почему:</b> %s",
			html.EscapeString(title), html.EscapeString(why)))
	}

	// Временные сбои (страница теста, старт попытки) отмечаем в quiz_fail,
	// чтобы следующий обход повторил тихо; закрытый тест — не сбой.
	markRetry := func() {
		n := 0
		if v, ok, _ := s.Store.GetSetting(quizFailKey(acc.TelegramID, it.CMID)); ok {
			_, _ = fmt.Sscan(v, &n)
		}
		_ = s.Store.SetSetting(quizFailKey(acc.TelegramID, it.CMID), fmt.Sprint(n+1))
	}

	info, err := c.InspectQuiz(ctx, it.CMID)
	if err != nil {
		markRetry()
		logx.Infof("uni", "quiz %s: выход: ошибка — страница теста не открылась: %v", tag, err)
		refuse("не открылась страница теста: " + err.Error())
		return
	}
	if !info.CanStart && info.InProgressAttemptID == 0 {
		// Тест закрыт — повторять нечего.
		_ = s.Store.SetSetting(quizFailKey(acc.TelegramID, it.CMID), "")
		logx.Infof("uni", "quiz %s: выход: тест закрыт для попыток (can_start=false, незавершённой попытки нет)", tag)
		refuse("Moodle не даёт начать попытку (тест закрыт или попытки кончились)")
		return
	}

	attempt, err := c.StartOrResumeAttempt(ctx, it.CMID)
	if err != nil {
		markRetry()
		logx.Infof("uni", "quiz %s: выход: ошибка — попытка не началась: %v", tag, err)
		refuse("попытка не началась: " + err.Error())
		return
	}
	attemptStarted := time.Now()

	pages := []*moodle.AttemptPage{attempt}
	cur := attempt
	var pageErr error
	sequentialNav := false
	seenPages := map[int]bool{cur.Page: true}
	for cur.NextPage > 0 {
		next, err := c.GetAttemptPage(ctx, it.CMID, cur.AttemptID, cur.NextPage)
		if err != nil {
			logx.Warnf("uni", "quiz %s: страница попытки %d: %v", tag, cur.NextPage, err)
			pageErr = fmt.Errorf("не загрузилась страница %d попытки: %w", cur.NextPage+1, err)
			break
		}
		if next.Page != cur.NextPage || next.Page < 0 || seenPages[next.Page] {
			logx.Infof("uni", "quiz %s: последовательная навигация (ожидалась страница %d, пришла %d)", tag, cur.NextPage, next.Page)
			sequentialNav = true
			break
		}
		seenPages[next.Page] = true
		pages = append(pages, next)
		cur = next
	}

	cfg := s.getCfg()
	var chain []llm.Target
	if cfg != nil {
		chain = access.QuizChainWithVault(cfg, s.Store, s.Vault, acc.TelegramID)
	}
	hasLLM := len(chain) > 0
	run := &quizlog.Run{At: s.now(), TelegramID: acc.TelegramID, CMID: it.CMID, Course: it.Course, Title: it.Title}
	var chainNames []string
	for _, t := range chain {
		run.Chain = append(run.Chain, t.String())
		chainNames = append(chainNames, t.String())
	}
	questions := 0
	for _, p := range pages {
		questions += len(p.Questions)
	}
	logx.Infof("uni", "quiz %s: страниц %d (sequential=%v), вопросов %d, цепочка ИИ (%d): [%s], ошибка загрузки страниц: %v",
		tag, len(pages), sequentialNav, questions, len(chain), strings.Join(chainNames, " > "), pageErr)
	started := time.Now()
	saveRun := func() {
		run.TookMS = time.Since(started).Milliseconds()
		if cfg == nil || cfg.DBPath == "" {
			return
		}
		if err := quizlog.Write(quizlog.Dir(cfg.DBPath), run); err != nil {
			logx.Warnf("uni", "quiz %s: журнал: %v", tag, err)
		}
	}

	// Не все страницы попытки загрузились — не сдаём частично.
	if pageErr != nil {
		run.Status = quizlog.Failed
		run.Error = pageErr.Error()
		saveRun()
		logx.Infof("uni", "quiz %s: выход: ошибка — %v", tag, pageErr)
		s.reportQuizFailure(ctx, acc.TelegramID, it, pageErr, false)
		return
	}

	if !hasLLM {
		// Ключ ИИ не задан: отправляем полное содержание теста пользователю
		parsedKey := fmt.Sprintf("quiz_parsed:%d:%d", acc.TelegramID, it.CMID)
		if v, ok, err := s.Store.GetSetting(parsedKey); err == nil && ok && v != "" {
			logx.Infof("uni", "quiz %s: выход: ИИ не настроен, текст теста уже отправлялся (%s), пропуск", tag, v)
			return // уже отправляли текст вопросов, не спамим
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("<b>Открыт тест: %s</b>\n", html.EscapeString(it.Title)))
		b.WriteString(fmt.Sprintf("<b>Курс:</b> %s\n\n", html.EscapeString(it.Course)))
		b.WriteString("<b>Содержание теста:</b>\n")
		for _, p := range pages {
			for _, q := range p.Questions {
				b.WriteString(fmt.Sprintf("\n<b>Вопрос %d</b> (%s):\n%s\n", q.Slot, q.QType, html.EscapeString(q.Text)))
				if q.Prompt != "" {
					b.WriteString(fmt.Sprintf("<i>%s</i>\n", html.EscapeString(q.Prompt)))
				}
				if len(q.Choices) > 0 {
					for _, ch := range q.Choices {
						b.WriteString(fmt.Sprintf("- [%s] %s\n", ch.Value, html.EscapeString(ch.Text)))
					}
				}
				if len(q.MatchOptions) > 0 {
					for _, mo := range q.MatchOptions {
						b.WriteString(fmt.Sprintf("- %s -> [выбор из вариантов]\n", html.EscapeString(mo.Question)))
					}
				}
			}
		}
		if it.URL != "" {
			b.WriteString(fmt.Sprintf("\n<a href=\"%s\">Открыть тест в Moodle</a>", html.EscapeString(it.URL)))
		}
		s.notify(ctx, acc.TelegramID, b.String())
		if err := s.Store.SetSetting(parsedKey, s.now().Format(time.RFC3339)); err != nil {
			logx.Warnf("uni", "quiz %s: сохранить отметку «текст отправлен»: %v", tag, err)
		}
		logx.Infof("uni", "quiz %s: выход: ИИ не настроен, отправлено содержание теста (вопросов %d)", tag, questions)
		run.Status = quizlog.Parsed
		saveRun()
		return
	}

	// Бюджет времени по таймеру Moodle: оставляем резерв quizTimeReserve на сохранение ответов.
	timeLeft, hasTimeLeft := parseTimeLeft(attempt.TimeLeft)
	var deadline time.Time
	initialFast := false
	if hasTimeLeft {
		if timeLeft <= quizFastModeTimeLeft {
			initialFast = true
		}
		if timeLeft > quizTimeReserve {
			deadline = attemptStarted.Add(timeLeft - quizTimeReserve)
		} else {
			initialFast = true
		}
		logx.Infof("uni", "quiz %s: таймер %q (%s), быстрый_режим=%v, резерв=%s",
			tag, attempt.TimeLeft, timeLeft, initialFast, quizTimeReserve)
	}
	solveCtx := ctx
	var solveCancel context.CancelFunc = func() {}
	if !deadline.IsZero() {
		solveCtx, solveCancel = context.WithDeadline(ctx, deadline)
	}
	defer solveCancel()

	// Авторешение теста через LLM. В свободной навигации страницы решаются параллельно
	// и отправляются в Moodle по возрастанию индекса сразу по мере готовности, чтобы
	// при сбое или нехватке времени уже решённый прогресс не пропал.
	materials := s.collectCourseMaterials(tag, it.Course)
	ws := s.webSearcher()

	var (
		allAnswers map[int]moodle.QuizAnswer
		usedModels map[string]bool
		sentCount  int
		runErr     error
	)
	if sequentialNav {
		pages, allAnswers, usedModels, sentCount, runErr = s.solveAndSubmitSeqNav(
			ctx, solveCtx, tag, c, ws, chain, it.Course, materials, attempt, initialFast, deadline, run,
		)
	} else {
		allAnswers, usedModels, sentCount, runErr = s.solveAndSubmitFreeNav(
			ctx, solveCtx, solveCancel, tag, c, ws, chain, it.Course, materials, pages, initialFast, deadline, run,
		)
	}

	if runErr != nil {
		run.Status = quizlog.Failed
		run.Error = runErr.Error()
		saveRun()
		logx.Infof("uni", "quiz %s: выход: ошибка — %v", tag, runErr)
		s.reportQuizFailure(ctx, acc.TelegramID, it, runErr, sentCount > 0)
		return
	}

	result, err := c.FinishAttempt(ctx, it.CMID, attempt.AttemptID)
	if err != nil {
		logx.Warnf("uni", "quiz %s: завершение попытки: %v", tag, err)
		run.Status = quizlog.Failed
		run.Error = "завершение попытки: " + err.Error()
		saveRun()
		logx.Infof("uni", "quiz %s: выход: ошибка — попытку не удалось завершить: %v", tag, err)
		s.reportQuizFailure(ctx, acc.TelegramID, it, fmt.Errorf("ответы сохранены, но попытку не удалось завершить: %w", err), true)
		return
	}
	_ = s.Store.SetSetting(quizFailKey(acc.TelegramID, it.CMID), "")
	run.Status = quizlog.Solved
	run.Grade, run.Feedback = result.Grade, result.Feedback
	saveRun()

	if err := s.Store.SetSetting(solvedKey, s.now().Format(time.RFC3339)); err != nil {
		logx.Warnf("uni", "quiz %s: сохранить отметку «решён»: %v", tag, err)
	}
	logx.Infof("uni", "quiz %s: выход: успех — оценка=%q, модели=%v, за %s",
		tag, result.Grade, usedModelList(usedModels), time.Since(started).Round(time.Millisecond))

	// Формируем отчёт пользователю
	var b strings.Builder
	b.WriteString("<b>Тест Moodle автоматически решён</b>\n")
	b.WriteString(fmt.Sprintf("<b>Курс:</b> %s\n", html.EscapeString(it.Course)))
	b.WriteString(fmt.Sprintf("<b>Тест:</b> %s\n", html.EscapeString(it.Title)))
	if result.Grade != "" {
		b.WriteString(fmt.Sprintf("<b>Оценка:</b> %s\n", html.EscapeString(result.Grade)))
	}
	if result.Feedback != "" {
		b.WriteString(fmt.Sprintf("<b>Отзыв:</b> %s\n", html.EscapeString(result.Feedback)))
	}
	if len(usedModels) > 0 {
		var ms []string
		for m := range usedModels {
			ms = append(ms, m)
		}
		b.WriteString(fmt.Sprintf("<b>Модель:</b> %s\n", html.EscapeString(strings.Join(ms, ", "))))
	}
	b.WriteString("\n<b>Выбранные ответы:</b>\n")
	for _, p := range pages {
		for _, q := range p.Questions {
			ansStr := formatAnswerText(q, allAnswers[q.Slot])
			b.WriteString(fmt.Sprintf("- <b>Вопрос %d:</b> %s\n", q.Slot, html.EscapeString(ansStr)))
		}
	}
	if it.URL != "" {
		b.WriteString(fmt.Sprintf("\n<a href=\"%s\">Открыть тест в Moodle</a>", html.EscapeString(it.URL)))
	}
	s.notify(ctx, acc.TelegramID, b.String())
}

type pageSolveResult struct {
	idx     int
	answers map[int]moodle.QuizAnswer
	usedKey string
	pl      quizlog.Page
	err     error
}

func pageOptsForWave(initialFast bool, deadline time.Time, remPages, workers int) solvePageOpts {
	fast := initialFast
	if !fast && !deadline.IsZero() {
		left := time.Until(deadline)
		if workers < 1 {
			workers = 1
		}
		wavesLeft := (remPages + workers - 1) / workers
		if left < quizFastModeRemaining || left < time.Duration(wavesLeft)*quizWaveBudget {
			fast = true
		}
	}
	return solvePageOpts{
		FastMode: fast,
		Deadline: deadline,
	}
}

func (s *Syncer) solveSinglePage(ctx context.Context, tag quizTag, idx int, p *moodle.AttemptPage, c *moodle.Client, ws *websearch.Searcher, chain []llm.Target, course, materials string, opts solvePageOpts) pageSolveResult {
	ptag := tag
	ptag.Page = p.Page
	answers, res, pl, err := s.solvePageWithOpts(ctx, ptag, c, ws, chain, course, materials, p.Questions, opts)
	pl.Page = p.Page
	var usedKey string
	if res.Used != nil {
		pl.Used = res.Used.String()
		usedKey = res.Used.Provider + "/" + res.Used.Model
	}
	if err == nil && len(p.Questions) > 0 && len(answers) == 0 {
		err = fmt.Errorf("ИИ не дал ни одного ответа")
	}
	if err != nil {
		pl.Error = err.Error()
		logx.Warnf("uni", "quiz %s: решение страницы: %v", ptag, err)
	}
	logx.Debugf("uni", "quiz %s: страница решена: ответов %d из %d, модель %s, ошибка=%v",
		ptag, len(answers), len(p.Questions), pl.Used, err)
	pl.Answers = map[int]string{}
	for _, q := range p.Questions {
		if a, ok := answers[q.Slot]; ok {
			pl.Answers[q.Slot] = formatAnswerText(q, a)
		}
	}
	return pageSolveResult{
		idx:     idx,
		answers: answers,
		usedKey: usedKey,
		pl:      pl,
		err:     err,
	}
}

func submitPageAnswers(ctx context.Context, tag quizTag, c *moodle.Client, page *moodle.AttemptPage, answers map[int]moodle.QuizAnswer) (*moodle.AttemptPage, error) {
	var checkSlots []int
	for _, q := range page.Questions {
		if q.HasSubmitButton {
			checkSlots = append(checkSlots, q.Slot)
		}
	}
	for _, slot := range checkSlots {
		updated, err := c.SubmitAnswers(ctx, page, answers, slot)
		if err != nil {
			logx.Warnf("uni", "quiz %s: проверка вопроса slot=%d: %v", tag, slot, err)
			return nil, fmt.Errorf("проверка вопроса %d: %w", slot, err)
		}
		if updated != nil {
			page = updated
		}
	}
	updated, err := c.SubmitAnswers(ctx, page, answers, 0)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func collectRunPages(run *quizlog.Run, results []*pageSolveResult) {
	for _, r := range results {
		if r != nil {
			run.Pages = append(run.Pages, r.pl)
		}
	}
}

func (s *Syncer) solveAndSubmitFreeNav(
	ctx, solveCtx context.Context,
	solveCancel context.CancelFunc,
	tag quizTag,
	c *moodle.Client,
	ws *websearch.Searcher,
	chain []llm.Target,
	course, materials string,
	pages []*moodle.AttemptPage,
	initialFast bool,
	deadline time.Time,
	run *quizlog.Run,
) (map[int]moodle.QuizAnswer, map[string]bool, int, error) {
	allAnswers := make(map[int]moodle.QuizAnswer)
	usedModels := make(map[string]bool)
	n := len(pages)
	if n == 0 {
		return allAnswers, usedModels, 0, nil
	}

	workers := min(quizPageWorkers, n)
	jobs := make(chan int, n)
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)

	resultsCh := make(chan pageSolveResult, n)
	var remaining atomic.Int64
	remaining.Store(int64(n))

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if solveCtx.Err() != nil {
					resultsCh <- pageSolveResult{
						idx: idx,
						pl:  quizlog.Page{Page: pages[idx].Page, Error: solveCtx.Err().Error()},
						err: solveCtx.Err(),
					}
					continue
				}
				rem := int(remaining.Add(-1)) + 1
				opts := pageOptsForWave(initialFast, deadline, rem, workers)
				resultsCh <- s.solveSinglePage(solveCtx, tag, idx, pages[idx], c, ws, chain, course, materials, opts)
			}
		}()
	}

	results := make([]*pageSolveResult, n)
	drainAndCollect := func() {
		close(resultsCh)
		for r := range resultsCh {
			cp := r
			results[r.idx] = &cp
		}
		collectRunPages(run, results)
	}
	nextToSubmit := 0
	for nextToSubmit < n {
		if results[nextToSubmit] == nil {
			r := <-resultsCh
			cp := r
			results[r.idx] = &cp
			continue
		}
		r := results[nextToSubmit]
		if r.usedKey != "" {
			usedModels[r.usedKey] = true
		}
		for slot, ans := range r.answers {
			allAnswers[slot] = ans
		}
		if r.err != nil {
			solveCancel()
			wg.Wait()
			drainAndCollect()
			return allAnswers, usedModels, nextToSubmit, r.err
		}

		ptag := tag
		ptag.Page = pages[nextToSubmit].Page
		if _, subErr := submitPageAnswers(ctx, ptag, c, pages[nextToSubmit], r.answers); subErr != nil {
			logx.Warnf("uni", "quiz %s: сохранение ответов страницы: %v", ptag, subErr)
			solveCancel()
			wg.Wait()
			drainAndCollect()
			return allAnswers, usedModels, nextToSubmit, fmt.Errorf("ответы страницы %d не сохранились в Moodle: %w", nextToSubmit+1, subErr)
		}
		logx.Debugf("uni", "quiz %s: ответы страницы %d отправлены в Moodle", tag, pages[nextToSubmit].Page)
		nextToSubmit++
	}

	wg.Wait()
	drainAndCollect()
	return allAnswers, usedModels, nextToSubmit, nil
}

func (s *Syncer) solveAndSubmitSeqNav(
	ctx, solveCtx context.Context,
	tag quizTag,
	c *moodle.Client,
	ws *websearch.Searcher,
	chain []llm.Target,
	course, materials string,
	firstPage *moodle.AttemptPage,
	initialFast bool,
	deadline time.Time,
	run *quizlog.Run,
) ([]*moodle.AttemptPage, map[int]moodle.QuizAnswer, map[string]bool, int, error) {
	var pages []*moodle.AttemptPage
	allAnswers := make(map[int]moodle.QuizAnswer)
	usedModels := make(map[string]bool)
	seen := make(map[int]bool)

	cur := firstPage
	sentCount := 0
	for idx := 0; ; idx++ {
		if seen[cur.Page] {
			return pages, allAnswers, usedModels, sentCount, fmt.Errorf("повторная страница %d при последовательной навигации", cur.Page)
		}
		seen[cur.Page] = true
		pages = append(pages, cur)

		opts := pageOptsForWave(initialFast, deadline, 1, 1)
		r := s.solveSinglePage(solveCtx, tag, idx, cur, c, ws, chain, course, materials, opts)
		run.Pages = append(run.Pages, r.pl)
		if r.usedKey != "" {
			usedModels[r.usedKey] = true
		}
		for slot, ans := range r.answers {
			allAnswers[slot] = ans
		}
		if r.err != nil {
			return pages, allAnswers, usedModels, sentCount, r.err
		}

		ptag := tag
		ptag.Page = cur.Page
		updated, subErr := submitPageAnswers(ctx, ptag, c, cur, r.answers)
		if subErr != nil {
			logx.Warnf("uni", "quiz %s: сохранение ответов страницы: %v", ptag, subErr)
			return pages, allAnswers, usedModels, sentCount, fmt.Errorf("ответы страницы %d не сохранились в Moodle: %w", idx+1, subErr)
		}
		sentCount++
		logx.Debugf("uni", "quiz %s: ответы страницы %d отправлены в Moodle", tag, cur.Page)

		if updated == nil || updated.Page < 0 || cur.NextPage < 0 {
			break
		}
		cur = updated
	}
	return pages, allAnswers, usedModels, sentCount, nil
}

func (s *Syncer) collectCourseMaterials(tag quizTag, course string) string {
	if s.Store == nil {
		return ""
	}
	packs, err := s.Store.ListPacks()
	if err != nil {
		logx.Warnf("uni", "quiz %s: список лекций: %v", tag, err)
		return ""
	}

	recRoot := config.DefaultRecordings
	if cfg := s.getCfg(); cfg != nil && cfg.RecordingsDir != "" {
		recRoot = cfg.RecordingsDir
	}

	var b strings.Builder
	const maxTotalChars = 32000
	var used []string
	for _, p := range packs {
		if !matchDiscipline(p.Discipline, course) {
			logx.Debugf("uni", "quiz %s: материалы: пакет id=%d «%s» №%d не относится к курсу %q, пропуск", tag, p.ID, p.Discipline, p.Number, course)
			continue
		}
		notesRunes, transRunes := 0, 0
		if p.Dir != "" {
			packDir := p.Dir
			if !filepath.IsAbs(packDir) {
				packDir = filepath.Join(recRoot, packDir)
			}
			notesPath := filepath.Join(packDir, "notes.md")
			if data, err := os.ReadFile(notesPath); err == nil && len(data) > 0 {
				b.WriteString(fmt.Sprintf("\n--- Конспект лекции %s №%d ---\n", p.Discipline, p.Number))
				b.Write(data)
				b.WriteString("\n")
				notesRunes = utf8.RuneCount(data)
			}
			if utf8.RuneCountInString(b.String()) < maxTotalChars {
				transPath := filepath.Join(packDir, "transcript.txt")
				if data, err := os.ReadFile(transPath); err == nil && len(data) > 0 {
					b.WriteString(fmt.Sprintf("\n--- Транскрипт лекции %s №%d ---\n", p.Discipline, p.Number))
					b.Write(data)
					b.WriteString("\n")
					transRunes = utf8.RuneCount(data)
				}
			}
		}
		logx.Debugf("uni", "quiz %s: материалы: пакет id=%d «%s» №%d: конспект %d симв., транскрипт %d симв.",
			tag, p.ID, p.Discipline, p.Number, notesRunes, transRunes)
		if notesRunes+transRunes > 0 {
			used = append(used, fmt.Sprintf("%s №%d", p.Discipline, p.Number))
		}
		if utf8.RuneCountInString(b.String()) >= maxTotalChars {
			break
		}
	}
	res := b.String()
	if runes := []rune(res); len(runes) > maxTotalChars {
		res = string(runes[:maxTotalChars])
	}
	logx.Infof("uni", "quiz %s: материалы курса: %d симв. из пакетов [%s]", tag, utf8.RuneCountInString(res), strings.Join(used, ", "))
	return res
}

// usedModelList — модели, которые отвечали на тест, по алфавиту (для лога).
func usedModelList(m map[string]bool) string {
	var ms []string
	for k := range m {
		ms = append(ms, k)
	}
	sort.Strings(ms)
	return strings.Join(ms, ", ")
}

func matchDiscipline(discipline, course string) bool {
	d := strings.ToLower(cleanStr(discipline))
	c := strings.ToLower(cleanStr(course))
	if d == "" || c == "" {
		return false
	}
	if strings.Contains(c, d) || strings.Contains(d, c) {
		return true
	}
	words := strings.FieldsFunc(d, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	matched := 0
	for _, w := range words {
		if len([]rune(w)) >= 4 && strings.Contains(c, w) {
			matched++
		}
	}
	return matched >= 2
}

func cleanStr(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func formatAnswerText(q moodle.QuizQuestion, ans moodle.QuizAnswer) string {
	switch {
	case q.QType == "shortanswer" || q.QType == "numerical":
		if ans.Text != "" {
			return ans.Text
		}
		return "(пусто)"
	case len(q.Choices) > 0 && q.Choices[0].IsMulti:
		var labels []string
		for _, ch := range q.Choices {
			for _, selected := range ans.Choices {
				if selected == ch.Value || selected == ch.Name || selected == ch.Text {
					labels = append(labels, ch.Text)
					break
				}
			}
		}
		if len(labels) > 0 {
			return strings.Join(labels, ", ")
		}
		return "(не выбрано)"
	case len(q.Choices) > 0 && !q.Choices[0].IsMulti:
		for _, ch := range q.Choices {
			if ch.Value == ans.Choice || ch.Name == ans.Choice || ch.Text == ans.Choice {
				return ch.Text
			}
		}
		if ans.Choice != "" {
			return ans.Choice
		}
		return "(не выбрано)"
	case len(q.MatchOptions) > 0:
		var pairs []string
		for _, mo := range q.MatchOptions {
			if val, ok := ans.Matches[mo.Name]; ok {
				pairs = append(pairs, fmt.Sprintf("%s -> %s", mo.Question, val))
			}
		}
		if len(pairs) > 0 {
			return strings.Join(pairs, "; ")
		}
		return "(не сопоставлено)"
	}
	if ans.Text != "" {
		return ans.Text
	}
	return "(ответ сохранен)"
}

func quizFailKey(id, cmid int64) string { return fmt.Sprintf("quiz_fail:%d:%d", id, cmid) }

// reportQuizFailure — «проблема с тестом». Синк повторит попытку через 30 минут;
// пишем на первой неудаче и потом раз в 6 попыток (~3 часа), чтобы не спамить.
// sent — часть ответов уже ушла в Moodle (тогда текст «ничего не отправлял» неверен).
func (s *Syncer) reportQuizFailure(ctx context.Context, id int64, it model.MoodleItem, err error, sent bool) {
	n := 0
	if v, ok, _ := s.Store.GetSetting(quizFailKey(id, it.CMID)); ok {
		_, _ = fmt.Sscan(v, &n)
	}
	n++
	_ = s.Store.SetSetting(quizFailKey(id, it.CMID), fmt.Sprint(n))
	if n != 1 && n%6 != 0 {
		return
	}
	var b strings.Builder
	b.WriteString("<b>⚠️ Проблема с тестом</b>\n")
	b.WriteString(fmt.Sprintf("<b>Тест:</b> %s (%s)\n", html.EscapeString(it.Title), html.EscapeString(it.Course)))
	b.WriteString(fmt.Sprintf("<b>Что случилось:</b> %s\n", html.EscapeString(shortErr(err))))
	if sent {
		b.WriteString("Часть ответов уже сохранена в Moodle, попытку не завершал. Попробую ещё раз при следующем обновлении Moodle.")
	} else {
		b.WriteString("Попытку не завершал, пустые ответы не отправлял. Попробую ещё раз при следующем обновлении Moodle.")
	}
	if it.URL != "" {
		b.WriteString(fmt.Sprintf("\n<a href=\"%s\">Открыть тест в Moodle</a>", html.EscapeString(it.URL)))
	}
	s.notify(ctx, id, b.String())
}
