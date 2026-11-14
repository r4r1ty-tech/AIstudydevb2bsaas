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

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/notes"
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

	// При первой инициализации аккаунта (когда в базе еще нет элементов и нет wake-кика)
	// уведомления не рассылаются, чтобы не спамить всей историей курсов.
	if len(prev) == 0 && !wakeKicked {
		return
	}

	prevMap := make(map[int64]model.MoodleItem, len(prev))
	for _, p := range prev {
		prevMap[p.CMID] = p
	}

	now := s.now()

	for _, it := range cur {
		if it.Kind != model.MoodleQuiz || it.Finished() {
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
		if wasClosed || wakeKicked {
			s.notify(ctx, acc.TelegramID, "<b>Открылся тест в Moodle!</b>\n"+FormatItem(it, now, s.loc()))
			s.processOpenedQuiz(ctx, acc, c, it)
		}
	}
}

// processOpenedQuiz обрабатывает открытый тест: парсит вопросы, собирает материалы курса
// и при наличии ключа LLM решает тест и отправляет отчёт, либо присылает текст вопросов.
func (s *Syncer) processOpenedQuiz(ctx context.Context, acc model.Account, c *moodle.Client, it model.MoodleItem) {
	solvedKey := fmt.Sprintf("quiz_solved:%d:%d", acc.TelegramID, it.CMID)
	if v, ok, err := s.Store.GetSetting(solvedKey); err == nil && ok && v != "" {
		return // тест уже успешно решён ранее
	}

	info, err := c.InspectQuiz(ctx, it.CMID)
	if err != nil {
		logx.Warnf("uni", "quiz %d inspect: %v", it.CMID, err)
		return
	}
	if !info.CanStart && info.InProgressAttemptID == 0 {
		return
	}

	attempt, err := c.StartOrResumeAttempt(ctx, it.CMID)
	if err != nil {
		logx.Warnf("uni", "quiz %d start: %v", it.CMID, err)
		return
	}

	pages := []*moodle.AttemptPage{attempt}
	cur := attempt
	for cur.NextPage > 0 {
		next, err := c.GetAttemptPage(ctx, it.CMID, cur.AttemptID, cur.NextPage)
		if err != nil {
			logx.Warnf("uni", "quiz %d get page %d: %v", it.CMID, cur.NextPage, err)
			break
		}
		pages = append(pages, next)
		cur = next
	}

	cfg := s.getCfg()
	hasLLM := cfg != nil && strings.TrimSpace(cfg.LLMAPIKey) != "" && strings.TrimSpace(cfg.LLMModel) != ""

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
		return
	}

	// Авторешение теста через LLM
	materials := s.collectCourseMaterials(it.Course)
	allAnswers := make(map[int]moodle.QuizAnswer)

	for _, p := range pages {
		answers, err := s.solveQuestionsWithLLM(ctx, cfg, it.Course, materials, p.Questions)
		if err != nil {
			logx.Warnf("uni", "quiz %d solve page %d: %v", it.CMID, p.Page, err)
		}
		for slot, ans := range answers {
			allAnswers[slot] = ans
		}

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
		}
	}

	result, err := c.FinishAttempt(ctx, it.CMID, attempt.AttemptID)
	if err != nil {
		logx.Warnf("uni", "quiz %d finish attempt: %v", it.CMID, err)
		return
	}

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

func (s *Syncer) solveQuestionsWithLLM(ctx context.Context, cfg *config.Config, course string, materials string, questions []moodle.QuizQuestion) (map[int]moodle.QuizAnswer, error) {
	if len(questions) == 0 {
		return nil, nil
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

	respText, err := notes.Ask(ctx, cfg, sysPrompt, userPrompt.String())
	if err != nil {
		return nil, fmt.Errorf("ask llm: %w", err)
	}

	cleanJSON := strings.TrimSpace(respText)
	if strings.HasPrefix(cleanJSON, "```") {
		cleanJSON = strings.TrimPrefix(cleanJSON, "```json")
		cleanJSON = strings.TrimPrefix(cleanJSON, "```")
		cleanJSON = strings.TrimSuffix(cleanJSON, "```")
		cleanJSON = strings.TrimSpace(cleanJSON)
	}

	var parsed llmSolveResponse
	if err := json.Unmarshal([]byte(cleanJSON), &parsed); err != nil {
		return nil, fmt.Errorf("decode llm response: %w (raw: %s)", err, cleanJSON)
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

	return out, nil
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
