package uni

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/access"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/llm"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/quizlog"
)

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
	wakeKickVal, ok, _ := s.Store.GetSetting(WakeKickKey)
	wakeKicked := ok && wakeKickVal != ""

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
	solvedKey := fmt.Sprintf("quiz_solved:%d:%d", acc.TelegramID, it.CMID)
	if v, ok, err := s.Store.GetSetting(solvedKey); err == nil && ok && v != "" {
		return // тест уже успешно решён ранее
	}
	refuse := func(why string) {
		logx.Warnf("uni", "quiz %d tg=%d: %s", it.CMID, acc.TelegramID, why)
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
		refuse("не открылась страница теста: " + err.Error())
		return
	}
	if !info.CanStart && info.InProgressAttemptID == 0 {
		// Тест закрыт — повторять нечего.
		_ = s.Store.SetSetting(quizFailKey(acc.TelegramID, it.CMID), "")
		refuse("Moodle не даёт начать попытку (тест закрыт или попытки кончились)")
		return
	}

	attempt, err := c.StartOrResumeAttempt(ctx, it.CMID)
	if err != nil {
		markRetry()
		refuse("попытка не началась: " + err.Error())
		return
	}

	pages := []*moodle.AttemptPage{attempt}
	cur := attempt
	var pageErr error
	for cur.NextPage > 0 {
		next, err := c.GetAttemptPage(ctx, it.CMID, cur.AttemptID, cur.NextPage)
		if err != nil {
			logx.Warnf("uni", "quiz %d get page %d: %v", it.CMID, cur.NextPage, err)
			pageErr = fmt.Errorf("не загрузилась страница %d попытки: %w", cur.NextPage+1, err)
			break
		}
		pages = append(pages, next)
		cur = next
	}

	cfg := s.getCfg()
	var chain []llm.Target
	if cfg != nil {
		chain = access.QuizChain(cfg, s.Store, acc.TelegramID)
	}
	hasLLM := len(chain) > 0
	run := &quizlog.Run{At: s.now(), TelegramID: acc.TelegramID, CMID: it.CMID, Course: it.Course, Title: it.Title}
	for _, t := range chain {
		run.Chain = append(run.Chain, t.String())
	}
	started := time.Now()
	saveRun := func() {
		run.TookMS = time.Since(started).Milliseconds()
		if cfg == nil || cfg.DBPath == "" {
			return
		}
		if err := quizlog.Write(quizlog.Dir(cfg.DBPath), run); err != nil {
			logx.Warnf("uni", "quiz log: %v", err)
		}
	}

	// Не все страницы попытки загрузились — не сдаём частично.
	if pageErr != nil {
		run.Status = quizlog.Failed
		run.Error = pageErr.Error()
		saveRun()
		s.reportQuizFailure(ctx, acc.TelegramID, it, pageErr, false)
		return
	}

	if !hasLLM {
		// Ключ ИИ не задан: отправляем полное содержание теста пользователю
		parsedKey := fmt.Sprintf("quiz_parsed:%d:%d", acc.TelegramID, it.CMID)
		if v, ok, err := s.Store.GetSetting(parsedKey); err == nil && ok && v != "" {
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
			logx.Warnf("uni", "save parsed key: %v", err)
		}
		run.Status = quizlog.Parsed
		saveRun()
		return
	}

	// Авторешение теста через LLM. Сначала решаем все страницы и только если
	// на каждую пришли ответы — отправляем и завершаем попытку. Иначе попытка
	// остаётся открытой (пустые ответы в Moodle не уходят), человеку — сообщение.
	materials := s.collectCourseMaterials(it.Course)
	allAnswers := make(map[int]moodle.QuizAnswer)
	pageAnswers := make([]map[int]moodle.QuizAnswer, len(pages))
	usedModels := map[string]bool{}
	var solveErr error

	for i, p := range pages {
		answers, res, err := s.solveQuestionsWithLLM(ctx, chain, it.Course, materials, p.Questions)
		pl := quizlog.Page{Page: p.Page, Attempts: res.Attempts, Raw: res.Text}
		if res.Used != nil {
			pl.Used = res.Used.String()
			usedModels[res.Used.Provider+"/"+res.Used.Model] = true
		}
		if err == nil && len(p.Questions) > 0 && len(answers) == 0 {
			err = fmt.Errorf("ИИ не дал ни одного ответа")
		}
		if err != nil {
			pl.Error = err.Error()
			logx.Warnf("uni", "quiz %d solve page %d: %v", it.CMID, p.Page, err)
			if solveErr == nil {
				solveErr = err
			}
		}
		pl.Answers = map[int]string{}
		for _, q := range p.Questions {
			if a, ok := answers[q.Slot]; ok {
				pl.Answers[q.Slot] = formatAnswerText(q, a)
			}
		}
		run.Pages = append(run.Pages, pl)
		pageAnswers[i] = answers
		for slot, ans := range answers {
			allAnswers[slot] = ans
		}
	}

	if solveErr != nil {
		run.Status = quizlog.Failed
		run.Error = solveErr.Error()
		saveRun()
		s.reportQuizFailure(ctx, acc.TelegramID, it, solveErr, false)
		return
	}

	for i, p := range pages {
		answers := pageAnswers[i]

		// Если есть адаптивный вопрос с кнопкой проверки
		checkSlot := 0
		for _, q := range p.Questions {
			if q.HasSubmitButton {
				checkSlot = q.Slot
				break
			}
		}
		if checkSlot > 0 {
			if updated, err := c.SubmitAnswers(ctx, p, answers, checkSlot); err == nil {
				p = updated
			}
		}

		if _, err := c.SubmitAnswers(ctx, p, answers, 0); err != nil {
			logx.Warnf("uni", "quiz %d submit page %d: %v", it.CMID, p.Page, err)
			// Ответы страницы не сохранились — не завершаем попытку, иначе
			// Moodle засчитает её с пустыми ответами.
			run.Status = quizlog.Failed
			run.Error = fmt.Sprintf("ответы страницы %d не сохранились: %v", i+1, err)
			saveRun()
			s.reportQuizFailure(ctx, acc.TelegramID, it, fmt.Errorf("ответы страницы %d не сохранились в Moodle: %w", i+1, err), i > 0)
			return
		}
	}

	result, err := c.FinishAttempt(ctx, it.CMID, attempt.AttemptID)
	if err != nil {
		logx.Warnf("uni", "quiz %d finish attempt: %v", it.CMID, err)
		run.Status = quizlog.Failed
		run.Error = "завершение попытки: " + err.Error()
		saveRun()
		s.reportQuizFailure(ctx, acc.TelegramID, it, fmt.Errorf("ответы сохранены, но попытку не удалось завершить: %w", err), true)
		return
	}
	_ = s.Store.SetSetting(quizFailKey(acc.TelegramID, it.CMID), "")
	run.Status = quizlog.Solved
	run.Grade, run.Feedback = result.Grade, result.Feedback
	saveRun()

	if err := s.Store.SetSetting(solvedKey, s.now().Format(time.RFC3339)); err != nil {
		logx.Warnf("uni", "save solved key: %v", err)
	}

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

func (s *Syncer) collectCourseMaterials(course string) string {
	if s.Store == nil {
		return ""
	}
	packs, err := s.Store.ListPacks()
	if err != nil {
		logx.Warnf("uni", "list packs: %v", err)
		return ""
	}

	var b strings.Builder
	const maxTotalChars = 32000
	for _, p := range packs {
		if !matchDiscipline(p.Discipline, course) {
			continue
		}
		if p.Dir != "" {
			notesPath := filepath.Join(p.Dir, "notes.md")
			if data, err := os.ReadFile(notesPath); err == nil && len(data) > 0 {
				b.WriteString(fmt.Sprintf("\n--- Конспект лекции %s №%d ---\n", p.Discipline, p.Number))
				b.Write(data)
				b.WriteString("\n")
			}
			transPath := filepath.Join(p.Dir, "transcript.txt")
			if data, err := os.ReadFile(transPath); err == nil && len(data) > 0 {
				b.WriteString(fmt.Sprintf("\n--- Транскрипт лекции %s №%d ---\n", p.Discipline, p.Number))
				b.Write(data)
				b.WriteString("\n")
			}
		}
		if b.Len() >= maxTotalChars {
			break
		}
	}
	res := b.String()
	if len(res) > maxTotalChars {
		res = res[:maxTotalChars]
	}
	return res
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

type llmAnswerItem struct {
	Slot    int               `json:"slot"`
	Text    string            `json:"text,omitempty"`
	Choice  string            `json:"choice,omitempty"`
	Choices []string          `json:"choices,omitempty"`
	Matches map[string]string `json:"matches,omitempty"`
}

type llmSolveResponse struct {
	Answers []llmAnswerItem `json:"answers"`
}

func (s *Syncer) solveQuestionsWithLLM(ctx context.Context, chain []llm.Target, course string, materials string, questions []moodle.QuizQuestion) (map[int]moodle.QuizAnswer, llm.Result, error) {
	var res llm.Result
	if len(questions) == 0 {
		return nil, res, nil
	}

	sysPrompt := "Ты — отличник университета. Твоя задача — решить тест по дисциплине на основе предоставленных материалов лекций (конспектов и транскриптов) и условий задач.\n" +
		"Отвечай СТРОГО в формате JSON без какого-либо вступительного или пояснительного текста:\n" +
		"{\n" +
		"  \"answers\": [\n" +
		"    {\n" +
		"      \"slot\": 1,\n" +
		"      \"text\": \"значение\",\n" +
		"      \"choice\": \"0\",\n" +
		"      \"choices\": [\"choice0\", \"choice1\"],\n" +
		"      \"matches\": {\"q123:1_sub0\": \"1\"}\n" +
		"    }\n" +
		"  ]\n" +
		"}\n" +
		"Правила:\n" +
		"- Для multichoice (один ответ) укажи в \"choice\" значение value или имя выбранного варианта.\n" +
		"- Для multichoice (несколько ответов) укажи в \"choices\" список выбранных вариантов.\n" +
		"- Для shortanswer или numerical укажи точный ответ в поле \"text\" без лишних пробелов и точек.\n" +
		"- Для match укажи в \"matches\" словарь имя_поля -> значение_варианта."

	var userPrompt strings.Builder
	userPrompt.WriteString(fmt.Sprintf("Курс: %s\n\n", course))
	if materials != "" {
		userPrompt.WriteString("--- Материалы курса (конспекты/транскрипты) ---\n")
		userPrompt.WriteString(materials)
		userPrompt.WriteString("\n\n")
	}
	userPrompt.WriteString("--- Вопросы теста ---\n")
	for _, q := range questions {
		userPrompt.WriteString(fmt.Sprintf("Вопрос %d (тип: %s):\n%s\n", q.Slot, q.QType, q.Text))
		if q.Prompt != "" {
			userPrompt.WriteString(fmt.Sprintf("Подсказка: %s\n", q.Prompt))
		}
		if len(q.Choices) > 0 {
			userPrompt.WriteString("Варианты ответа:\n")
			for _, ch := range q.Choices {
				userPrompt.WriteString(fmt.Sprintf("- [%s] %s\n", ch.Value, ch.Text))
			}
		}
		if len(q.MatchOptions) > 0 {
			userPrompt.WriteString("Соответствия:\n")
			for _, mo := range q.MatchOptions {
				userPrompt.WriteString(fmt.Sprintf("- %s (поле: %s)\n", mo.Question, mo.Name))
				for _, opt := range mo.Choices {
					userPrompt.WriteString(fmt.Sprintf("    opt [%s]: %s\n", opt.Value, opt.Text))
				}
			}
		}
		userPrompt.WriteString("\n")
	}

	res, err := llm.Ask(ctx, chain, sysPrompt, userPrompt.String())
	if err != nil {
		return nil, res, fmt.Errorf("ask llm: %w", err)
	}
	respText := res.Text

	cleanJSON := strings.TrimSpace(respText)
	if strings.HasPrefix(cleanJSON, "```") {
		cleanJSON = strings.TrimPrefix(cleanJSON, "```json")
		cleanJSON = strings.TrimPrefix(cleanJSON, "```")
		cleanJSON = strings.TrimSuffix(cleanJSON, "```")
		cleanJSON = strings.TrimSpace(cleanJSON)
	}

	var parsed llmSolveResponse
	if err := json.Unmarshal([]byte(cleanJSON), &parsed); err != nil {
		return nil, res, fmt.Errorf("ответ ИИ не JSON: %w", err)
	}

	out := make(map[int]moodle.QuizAnswer, len(parsed.Answers))
	for _, a := range parsed.Answers {
		// Сопоставляем ответ с вопросом
		for _, q := range questions {
			if q.Slot == a.Slot {
				ans := moodle.QuizAnswer{
					Text:    strings.TrimSpace(a.Text),
					Choice:  strings.TrimSpace(a.Choice),
					Choices: a.Choices,
					Matches: a.Matches,
				}
				// Если для radio передан текст варианта вместо индекса
				if ans.Choice != "" && len(q.Choices) > 0 && !q.Choices[0].IsMulti {
					for _, ch := range q.Choices {
						if strings.EqualFold(strings.TrimSpace(ch.Text), ans.Choice) {
							ans.Choice = ch.Value
							break
						}
					}
				}
				out[a.Slot] = ans
				break
			}
		}
	}

	return out, res, nil
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
