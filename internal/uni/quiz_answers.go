package uni

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

// База проверенных ответов решателя.
//
// Тест решён → Moodle на странице просмотра показал, какие ответы верны → верные ответы
// пишутся в quiz_answers по хэшу условия и вариантов; неверные — в quiz_wrong_answers.
// Следующее решение сначала ищет ответ в базе и сопоставляет его с текущими вариантами по
// тексту; ИИ зовётся только для вопросов, которых в базе нет или которые не сопоставились.
// Ответ, который Moodle позже отметил неверным, из базы удаляется.

var quizTagRe = regexp.MustCompile(`<[^>]*>`)

// rightPrefixRe — префикс блока «Правильный ответ: …» на странице просмотра.
var rightPrefixRe = regexp.MustCompile(`(?i)^\s*правильный ответ\s*:\s*`)

// normQuizText — нормализация текста для хэша и сопоставления: без HTML, регистр, ё→е,
// пробелы схлопнуты. Одна функция для записи и для поиска.
func normQuizText(s string) string {
	s = quizTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "ё", "е")
	return strings.Join(strings.Fields(s), " ")
}

// quizHasImages — в условии есть рисунок. Такие вопросы в базу не пишем и по ней не ищем:
// текст рисунка в хэш не входит, два разных рисунка дали бы один ключ.
func quizHasImages(q moodle.QuizQuestion) bool {
	return len(q.Images) > 0
}

// quizQuestionHash — qhash вопроса: sha256 от нормализованного условия, типа и отсортированного
// набора вариантов. Варианты берутся по тексту, поэтому порядок перемешивания на хэш не влияет.
// Для соответствия: подвопросы учитываются как «q:», варианты выбора — как «v:».
func quizQuestionHash(q moodle.QuizQuestion) string {
	seen := map[string]bool{}
	var variants []string
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}
	for _, ch := range q.Choices {
		add("v:" + normQuizText(ch.Text))
	}
	for _, mo := range q.MatchOptions {
		add("q:" + normQuizText(mo.Question))
		for _, o := range mo.Choices {
			add("v:" + normQuizText(o.Text))
		}
	}
	sort.Strings(variants)

	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x1f%s\x1f%s", normQuizText(q.Text), q.QType, strings.Join(variants, "\x1e"))
	return hex.EncodeToString(h.Sum(nil))
}

// quizStoredAnswer — ответ, как он лежит в answer_json: тексты вариантов, а не их номера.
type quizStoredAnswer struct {
	Choice  string            `json:"choice,omitempty"`  // один вариант
	Choices []string          `json:"choices,omitempty"` // несколько вариантов (отсортированы)
	Text    string            `json:"text,omitempty"`    // ввод с клавиатуры
	Matches map[string]string `json:"matches,omitempty"` // подвопрос → текст варианта
}

// countNorm — сколько вариантов с таким нормализованным текстом.
func countNorm(choices []moodle.QuizChoice, text string) int {
	want := normQuizText(text)
	n := 0
	for _, ch := range choices {
		if normQuizText(ch.Text) == want {
			n++
		}
	}
	return n
}

// answerFromReview — ответ студента из разобранного вопроса страницы просмотра, в виде текстов.
// false — ответа нет, он не читается или варианты неоднозначны (одинаковый текст у двух вариантов).
func answerFromReview(q moodle.QuizQuestion) (quizStoredAnswer, bool) {
	switch {
	case len(q.Choices) > 0 && q.Choices[0].IsMulti:
		var sa quizStoredAnswer
		for _, ch := range q.Choices {
			if !ch.Checked {
				continue
			}
			t := strings.TrimSpace(ch.Text)
			if t == "" || countNorm(q.Choices, t) != 1 {
				return quizStoredAnswer{}, false
			}
			sa.Choices = append(sa.Choices, t)
		}
		sort.Strings(sa.Choices)
		return sa, len(sa.Choices) > 0
	case len(q.Choices) > 0:
		var sa quizStoredAnswer
		for _, ch := range q.Choices {
			if ch.Checked {
				sa.Choice = strings.TrimSpace(ch.Text)
				break
			}
		}
		if sa.Choice == "" || countNorm(q.Choices, sa.Choice) != 1 {
			return quizStoredAnswer{}, false
		}
		return sa, true
	case len(q.MatchOptions) > 0:
		sa := quizStoredAnswer{Matches: map[string]string{}}
		for _, mo := range q.MatchOptions {
			key := strings.TrimSpace(mo.Question)
			text := ""
			for _, o := range mo.Choices {
				if o.Value == mo.SelectedValue {
					text = strings.TrimSpace(o.Text)
					break
				}
			}
			if key == "" || text == "" || countNorm(optionsAsChoices(mo.Choices), text) != 1 {
				return quizStoredAnswer{}, false
			}
			if _, dup := sa.Matches[key]; dup {
				return quizStoredAnswer{}, false
			}
			sa.Matches[key] = text
		}
		return sa, true
	case q.InputName != "":
		t := strings.TrimSpace(q.InputValue)
		return quizStoredAnswer{Text: t}, t != ""
	}
	return quizStoredAnswer{}, false
}

// optionsAsChoices — варианты соответствия в виде вариантов выбора (для проверки уникальности).
func optionsAsChoices(opts []moodle.SelectOption) []moodle.QuizChoice {
	out := make([]moodle.QuizChoice, 0, len(opts))
	for _, o := range opts {
		out = append(out, moodle.QuizChoice{Value: o.Value, Text: o.Text})
	}
	return out
}

// applyStoredAnswer сопоставляет сохранённый ответ с текущими вариантами страницы по тексту и
// возвращает ответ в формате Moodle (значения и имена полей). false — вариант не нашёлся
// или текст неоднозначен.
func applyStoredAnswer(q moodle.QuizQuestion, sa quizStoredAnswer) (moodle.QuizAnswer, bool) {
	var ans moodle.QuizAnswer
	switch {
	case len(q.Choices) > 0 && q.Choices[0].IsMulti:
		if len(sa.Choices) == 0 {
			return ans, false
		}
		seen := map[string]bool{}
		for _, t := range sa.Choices {
			ch, ok := findChoiceByText(q.Choices, t)
			if !ok {
				return moodle.QuizAnswer{}, false
			}
			if !seen[ch.Name] {
				seen[ch.Name] = true
				ans.Choices = append(ans.Choices, ch.Name)
			}
		}
		return ans, true
	case len(q.Choices) > 0:
		if sa.Choice == "" {
			return ans, false
		}
		ch, ok := findChoiceByText(q.Choices, sa.Choice)
		if !ok {
			return ans, false
		}
		ans.Choice = ch.Value
		return ans, true
	case len(q.MatchOptions) > 0:
		if len(sa.Matches) != len(q.MatchOptions) {
			return ans, false
		}
		ans.Matches = map[string]string{}
		for _, mo := range q.MatchOptions {
			text, ok := matchTextFor(sa.Matches, mo.Question)
			if !ok {
				return moodle.QuizAnswer{}, false
			}
			val, ok := optionValueByText(mo.Choices, text)
			if !ok {
				return moodle.QuizAnswer{}, false
			}
			ans.Matches[mo.Name] = val
		}
		return ans, true
	case q.InputName != "":
		if sa.Text == "" {
			return ans, false
		}
		ans.Text = sa.Text
		return ans, true
	}
	return ans, false
}

// findChoiceByText — вариант с этим текстом. Неоднозначно (текст повторяется) — false.
func findChoiceByText(choices []moodle.QuizChoice, text string) (moodle.QuizChoice, bool) {
	if countNorm(choices, text) != 1 {
		return moodle.QuizChoice{}, false
	}
	want := normQuizText(text)
	for _, ch := range choices {
		if normQuizText(ch.Text) == want {
			return ch, true
		}
	}
	return moodle.QuizChoice{}, false
}

// optionValueByText — значение варианта соответствия по тексту. Неоднозначно — false.
func optionValueByText(opts []moodle.SelectOption, text string) (string, bool) {
	want := normQuizText(text)
	if want == "" || countNorm(optionsAsChoices(opts), text) != 1 {
		return "", false
	}
	for _, o := range opts {
		if normQuizText(o.Text) == want {
			return o.Value, true
		}
	}
	return "", false
}

func matchTextFor(m map[string]string, question string) (string, bool) {
	want := normQuizText(question)
	for k, v := range m {
		if normQuizText(k) == want {
			return v, true
		}
	}
	return "", false
}

// rightAnswerSA — правильный ответ из блока «Правильный ответ: …», если он однозначно
// сопоставляется с вариантами: один вариант выбора или короткий ответ. Соответствия и
// несколько вариантов не разбираем: формат блока для них неоднозначен.
func rightAnswerSA(q moodle.QuizQuestion, raw string) (quizStoredAnswer, bool) {
	t := strings.TrimSpace(rightPrefixRe.ReplaceAllString(raw, ""))
	if t == "" || strings.Contains(t, "|") {
		return quizStoredAnswer{}, false
	}
	switch {
	case len(q.Choices) > 0 && !q.Choices[0].IsMulti:
		ch, ok := findChoiceByText(q.Choices, t)
		if !ok {
			return quizStoredAnswer{}, false
		}
		return quizStoredAnswer{Choice: strings.TrimSpace(ch.Text)}, true
	case len(q.Choices) == 0 && len(q.MatchOptions) == 0 && q.InputName != "":
		return quizStoredAnswer{Text: t}, true
	}
	return quizStoredAnswer{}, false
}

// knownWrong — совпадает ли ответ с одним из известных неверных (answer_json из базы).
func knownWrong(q moodle.QuizQuestion, ans moodle.QuizAnswer, wrong []string) bool {
	for _, raw := range wrong {
		var sa quizStoredAnswer
		if json.Unmarshal([]byte(raw), &sa) != nil {
			continue
		}
		if w, ok := applyStoredAnswer(q, sa); ok && sameAnswer(q, ans, w) {
			return true
		}
	}
	return false
}

// resolveStoredAnswer переводит запись базы в ответ Moodle для вопроса страницы.
// Пустая причина — ответ годен. wrong — известные неверные ответы на этот вопрос: если запись
// совпадает с одним из них, она считается ненайденной (страховка от отравленной базы).
func resolveStoredAnswer(q moodle.QuizQuestion, rec store.VerifiedAnswer, wrong []string) (moodle.QuizAnswer, string) {
	var sa quizStoredAnswer
	if err := json.Unmarshal([]byte(rec.AnswerJSON), &sa); err != nil {
		return moodle.QuizAnswer{}, "ответ в базе не читается: " + err.Error()
	}
	ans, ok := applyStoredAnswer(q, sa)
	if !ok {
		return moodle.QuizAnswer{}, "варианты не сопоставились по тексту"
	}
	if bad := validateAnswers([]moodle.QuizQuestion{q}, map[int]moodle.QuizAnswer{q.Slot: ans}); len(bad) > 0 {
		return moodle.QuizAnswer{}, "не проходит проверку (" + bad[q.Slot] + ")"
	}
	if knownWrong(q, ans, wrong) {
		return moodle.QuizAnswer{}, "совпадает с ответом, который Moodle отметил неверным"
	}
	return ans, ""
}

// lookupQuestionAnswer — годный ответ из базы на вопрос (попадание считается).
func lookupQuestionAnswer(st *store.Store, tag quizTag, q moodle.QuizQuestion) (moodle.QuizAnswer, bool) {
	if st == nil || quizHasImages(q) {
		return moodle.QuizAnswer{}, false
	}
	hash := quizQuestionHash(q)
	rec, ok, err := st.PeekAnswer(hash)
	if err != nil {
		logx.Warnf("uni", "quiz %s: слот %d: поиск в базе ответов: %v", tag, q.Slot, err)
		return moodle.QuizAnswer{}, false
	}
	if !ok {
		logx.Debugf("uni", "quiz %s: слот %d: в базе нет ответа (qhash %s)", tag, q.Slot, hash[:12])
		return moodle.QuizAnswer{}, false
	}
	wrong, err := st.ListWrongAnswers(hash)
	if err != nil {
		logx.Warnf("uni", "quiz %s: слот %d: неверные ответы из базы: %v", tag, q.Slot, err)
		return moodle.QuizAnswer{}, false
	}
	ans, reason := resolveStoredAnswer(q, rec, wrong)
	if reason != "" {
		logx.Infof("uni", "quiz %s: слот %d: ответ из базы (qhash %s) не годится: %s, спрошу ИИ", tag, q.Slot, hash[:12], reason)
		return moodle.QuizAnswer{}, false
	}
	if _, _, err := st.LookupAnswer(hash); err != nil {
		logx.Warnf("uni", "quiz %s: слот %d: счётчик попаданий: %v", tag, q.Slot, err)
	}
	logx.Debugf("uni", "quiz %s: слот %d: ответ из базы (qhash %s)", tag, q.Slot, hash[:12])
	return ans, true
}

// answersFromBase ищет ответы в базе для каждого вопроса страницы. Найденные и сопоставленные
// ответы возвращаются готовыми; остальные вопросы — для ИИ.
func (s *Syncer) answersFromBase(tag quizTag, qs []moodle.QuizQuestion) (map[int]moodle.QuizAnswer, []moodle.QuizQuestion) {
	known := make(map[int]moodle.QuizAnswer)
	var rest []moodle.QuizQuestion
	for _, q := range qs {
		if ans, ok := lookupQuestionAnswer(s.Store, tag, q); ok {
			known[q.Slot] = ans
			continue
		}
		rest = append(rest, q)
	}
	if len(qs) > 0 {
		logx.Infof("uni", "quiz %s: из базы проверенных ответов %d из %d вопросов, через ИИ %d", tag, len(known), len(qs), len(rest))
	}
	return known, rest
}

// baseCoversPages — есть ли в базе годный ответ на каждый вопрос всех страниц попытки.
// Попаданий не считает: проверка до решения, сама страница решится через solvePageWithOpts.
func (s *Syncer) baseCoversPages(pages []*moodle.AttemptPage) bool {
	if s.Store == nil {
		return false
	}
	total := 0
	for _, p := range pages {
		for _, q := range p.Questions {
			total++
			if quizHasImages(q) {
				return false
			}
			hash := quizQuestionHash(q)
			rec, ok, err := s.Store.PeekAnswer(hash)
			if err != nil || !ok {
				return false
			}
			wrong, err := s.Store.ListWrongAnswers(hash)
			if err != nil {
				return false
			}
			if _, reason := resolveStoredAnswer(q, rec, wrong); reason != "" {
				return false
			}
		}
	}
	return total > 0
}

// wrongAnswer — ранее проверенный как неверный ответ на вопрос.
type wrongAnswer struct {
	text  string            // как показать модели: тексты вариантов
	ans   moodle.QuizAnswer // в формате Moodle; valid=false — варианты изменились
	valid bool
}

// wrongAnswersFor — неверные ответы на вопросы страницы по qhash. Вопросы — исходные (без
// описаний рисунков): хэш считается по тем же текстам, что и при записи.
func (s *Syncer) wrongAnswersFor(tag quizTag, qs []moodle.QuizQuestion) map[int][]wrongAnswer {
	out := map[int][]wrongAnswer{}
	if s.Store == nil {
		return out
	}
	for _, q := range qs {
		if quizHasImages(q) {
			continue
		}
		list, err := s.Store.ListWrongAnswers(quizQuestionHash(q))
		if err != nil {
			logx.Warnf("uni", "quiz %s: слот %d: неверные ответы из базы: %v", tag, q.Slot, err)
			continue
		}
		for _, raw := range list {
			var sa quizStoredAnswer
			if err := json.Unmarshal([]byte(raw), &sa); err != nil {
				continue
			}
			ans, valid := applyStoredAnswer(q, sa)
			out[q.Slot] = append(out[q.Slot], wrongAnswer{text: storedAnswerText(sa), ans: ans, valid: valid})
		}
	}
	return out
}

// storedAnswerText — ответ из базы текстом для промпта.
func storedAnswerText(sa quizStoredAnswer) string {
	switch {
	case len(sa.Choices) > 0:
		return strings.Join(sa.Choices, ", ")
	case sa.Choice != "":
		return sa.Choice
	case len(sa.Matches) > 0:
		keys := make([]string, 0, len(sa.Matches))
		for k := range sa.Matches {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, k+" → "+sa.Matches[k])
		}
		return strings.Join(pairs, "; ")
	}
	return sa.Text
}

// sameAnswer — совпадают ли два ответа на вопрос по смыслу (наборы вариантов, значения полей, текст).
func sameAnswer(q moodle.QuizQuestion, a, b moodle.QuizAnswer) bool {
	switch {
	case len(q.Choices) > 0 && q.Choices[0].IsMulti:
		if len(a.Choices) == 0 || len(a.Choices) != len(b.Choices) {
			return false
		}
		want := map[string]bool{}
		for _, c := range b.Choices {
			want[c] = true
		}
		for _, c := range a.Choices {
			if !want[c] {
				return false
			}
		}
		return true
	case len(q.Choices) > 0:
		return a.Choice != "" && a.Choice == b.Choice
	case len(q.MatchOptions) > 0:
		if len(a.Matches) == 0 || len(a.Matches) != len(b.Matches) {
			return false
		}
		for k, v := range a.Matches {
			if b.Matches[k] != v {
				return false
			}
		}
		return true
	}
	ta := normQuizText(a.Text)
	return ta != "" && ta == normQuizText(b.Text)
}

// conflictingSlots — слоты, где ответ модели совпадает с ранее проверенным неверным.
func conflictingSlots(qs []moodle.QuizQuestion, answers map[int]moodle.QuizAnswer, wrong map[int][]wrongAnswer) []int {
	var out []int
	for _, q := range qs {
		a, ok := answers[q.Slot]
		if !ok {
			continue
		}
		for _, w := range wrong[q.Slot] {
			if w.valid && sameAnswer(q, a, w.ans) {
				out = append(out, q.Slot)
				break
			}
		}
	}
	sort.Ints(out)
	return out
}

// recordVerifiedAnswers — после FinishAttempt читает страницу просмотра и пишет в базу:
//   - верные ответы (отметка Moodle или итог попытки при скрытых отметках) — в quiz_answers;
//     такой же ответ удаляется из quiz_wrong_answers;
//   - неверные (отметка «Неверно») — в quiz_wrong_answers; если такой же ответ был верным
//     в quiz_answers, верная запись удаляется (отравленная база);
//   - правильный ответ из блока «Правильный ответ» для неверного вопроса — как верный, если
//     он однозначен.
//
// Возвращает число новых знаний: записей, которых не было или которые изменились, и удалений.
// Ошибки только логируются: запись в базу не должна ломать решение теста.
// ErrReviewClosed и ошибка страницы — (0, 0): знания нет, повтора не будет.
func recordVerifiedAnswers(ctx context.Context, st *store.Store, c *moodle.Client, cmid, attemptID, tg int64, course string) (newRight, newWrong int) {
	if st == nil || c == nil {
		return 0, 0
	}
	tag := quizTag{TG: tg, CMID: cmid, Page: quizNoPage}
	rp, err := c.GetReview(ctx, cmid, attemptID)
	if err != nil {
		if errors.Is(err, moodle.ErrReviewClosed) {
			logx.Infof("uni", "quiz %s: база ответов: просмотр попытки закрыт настройками, записывать нечего", tag)
			return 0, 0
		}
		logx.Warnf("uni", "quiz %s: база ответов: страница просмотра не загрузилась: %v", tag, err)
		return 0, 0
	}

	byTotal, skipped := 0, 0
	for _, q := range rp.Questions {
		if quizHasImages(q.QuizQuestion) {
			skipped++
			logx.Debugf("uni", "quiz %s: база ответов: слот %d с рисунком не пишем", tag, q.Slot)
			continue
		}
		if q.Verdict == moodle.VerdictIncorrect && !q.ByTotal {
			r, w := recordIncorrect(st, tag, q, cmid, tg)
			newRight += r
			newWrong += w
			continue
		}
		if !q.Correct() {
			skipped++
			continue
		}
		sa, ok := answerFromReview(q.QuizQuestion)
		if !ok {
			skipped++
			logx.Warnf("uni", "quiz %s: база ответов: слот %d подтверждён, но ответ студента не разобран", tag, q.Slot)
			continue
		}
		if isNew, ok := saveVerified(st, tag, q.QuizQuestion, sa, cmid, tg, course, q.Text, q.QType); ok {
			if isNew {
				newRight++
			}
			if q.ByTotal {
				byTotal++
			}
			newWrong += dropWrongEquivalents(st, tag, q.QuizQuestion, sa)
		} else {
			skipped++
		}
	}
	logx.Infof("uni", "quiz %s: база ответов: новых верных %d (из них по итогу попытки %d), новых неверных %d, пропущено %d",
		tag, newRight, byTotal, newWrong, skipped)
	return newRight, newWrong
}

// recordIncorrect — вопрос с отметкой «Неверно»: удаляет отравленную верную запись, запоминает
// неверный ответ, а правильный ответ из блока «Правильный ответ» пишет как верный.
// Возвращает (новых верных, новых знаний о неверных).
func recordIncorrect(st *store.Store, tag quizTag, q moodle.ReviewQuestion, cmid, tg int64) (newRight, newWrong int) {
	hash := quizQuestionHash(q.QuizQuestion)
	sa, ok := answerFromReview(q.QuizQuestion)
	if !ok {
		logx.Warnf("uni", "quiz %s: база ответов: слот %d неверен, ответ студента не разобран", tag, q.Slot)
		return 0, 0
	}
	studentAns, studentOK := applyStoredAnswer(q.QuizQuestion, sa)

	// Отравленная база: верная запись, совпадающая с этим неверным ответом, удаляется.
	if rec, found, err := st.PeekAnswer(hash); err != nil {
		logx.Warnf("uni", "quiz %s: слот %d: поиск верного ответа: %v", tag, q.Slot, err)
	} else if found && studentOK {
		var stored quizStoredAnswer
		if json.Unmarshal([]byte(rec.AnswerJSON), &stored) == nil {
			if storedAns, ok := applyStoredAnswer(q.QuizQuestion, stored); ok && sameAnswer(q.QuizQuestion, studentAns, storedAns) {
				if removed, err := st.DeleteVerifiedAnswer(hash); err != nil {
					logx.Warnf("uni", "quiz %s: слот %d: удалить отравленную запись: %v", tag, q.Slot, err)
				} else if removed {
					logx.Infof("uni", "quiz %s: слот %d: ответ из базы отмечен неверным, запись удалена", tag, q.Slot)
					newWrong++
				}
			}
		}
	}

	raw, err := json.Marshal(sa)
	if err != nil {
		return 0, newWrong
	}
	if !containsString(wrongList(st, hash), string(raw)) {
		if err := st.SaveWrongAnswer(hash, string(raw), cmid, tg); err != nil {
			logx.Warnf("uni", "quiz %s: база ответов: слот %d неверный ответ не записан: %v", tag, q.Slot, err)
		} else {
			newWrong++
		}
	}
	logx.Debugf("uni", "quiz %s: база ответов: слот %d — неверный ответ запомнен", tag, q.Slot)

	if right, ok := rightAnswerSA(q.QuizQuestion, q.RightAnswer); ok {
		if isNew, ok := saveVerified(st, tag, q.QuizQuestion, right, cmid, tg, "", q.Text, q.QType); ok {
			if isNew {
				newRight++
			}
			newWrong += dropWrongEquivalents(st, tag, q.QuizQuestion, right)
			logx.Infof("uni", "quiz %s: слот %d: правильный ответ из блока «Правильный ответ» записан", tag, q.Slot)
		}
	}
	return newRight, newWrong
}

// saveVerified пишет верный ответ. isNew — записи не было или ответ изменился.
func saveVerified(st *store.Store, tag quizTag, q moodle.QuizQuestion, sa quizStoredAnswer, cmid, tg int64, course, text, qtype string) (isNew, ok bool) {
	raw, err := json.Marshal(sa)
	if err != nil {
		logx.Warnf("uni", "quiz %s: база ответов: слот %d: %v", tag, q.Slot, err)
		return false, false
	}
	hash := quizQuestionHash(q)
	prev, found, err := st.PeekAnswer(hash)
	if err != nil {
		logx.Warnf("uni", "quiz %s: база ответов: слот %d: %v", tag, q.Slot, err)
		return false, false
	}
	if err := st.SaveVerifiedAnswer(store.VerifiedAnswer{
		Course: course, CMID: cmid, QHash: hash, QType: qtype,
		QuestionText: text, AnswerJSON: string(raw), SourceTG: tg,
	}); err != nil {
		logx.Warnf("uni", "quiz %s: база ответов: слот %d не записан: %v", tag, q.Slot, err)
		return false, false
	}
	logx.Debugf("uni", "quiz %s: база ответов: слот %d записан", tag, q.Slot)
	return !found || prev.AnswerJSON != string(raw), true
}

// dropWrongEquivalents удаляет из quiz_wrong_answers ответы, совпадающие с верным.
// Возвращает число удалённых записей.
func dropWrongEquivalents(st *store.Store, tag quizTag, q moodle.QuizQuestion, right quizStoredAnswer) int {
	hash := quizQuestionHash(q)
	rightAns, ok := applyStoredAnswer(q, right)
	if !ok {
		return 0
	}
	removed := 0
	for _, raw := range wrongList(st, hash) {
		var sa quizStoredAnswer
		if json.Unmarshal([]byte(raw), &sa) != nil {
			continue
		}
		w, ok := applyStoredAnswer(q, sa)
		if !ok || !sameAnswer(q, rightAns, w) {
			continue
		}
		if del, err := st.DeleteWrongAnswer(hash, raw); err != nil {
			logx.Warnf("uni", "quiz %s: удалить неверный ответ: %v", tag, err)
		} else if del {
			removed++
		}
	}
	return removed
}

// wrongList — неверные ответы на вопрос (пусто при ошибке).
func wrongList(st *store.Store, hash string) []string {
	list, err := st.ListWrongAnswers(hash)
	if err != nil {
		logx.Warnf("uni", "неверные ответы из базы: %v", err)
	}
	return list
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// sortedSlots — номера слотов карты ответов по возрастанию (для журнала).
func sortedSlots(m map[int]moodle.QuizAnswer) []int {
	out := make([]int, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}
