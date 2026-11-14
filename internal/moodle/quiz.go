package moodle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"golang.org/x/net/html"
)

const logComponent = "moodle"

var (
	attemptIDRe    = regexp.MustCompile(`attempt=(\d+)`)
	qubaSlotIDRe   = regexp.MustCompile(`(?:question-|q)(\d+)[:-](\d+)`)
	seqCheckNameRe = regexp.MustCompile(`q(\d+):(\d+)_:sequencecheck`)
	// sesskeyJSRe — sesskey в инлайн-скрипте M.cfg; в снимки не пишем.
	sesskeyJSRe = regexp.MustCompile(`"sesskey":"[^"]*"`)
)

// dumpDoc сохраняет HTML страницы в снимки logx (только при подробном логе).
// sesskey и logintoken из снимка вырезаются; сам документ не меняется.
func dumpDoc(kind, name string, doc *goquery.Document) {
	if doc == nil || !logx.Verbose() {
		return
	}
	cp := doc.Clone()
	cp.Find("input[name=sesskey], input[name=logintoken]").SetAttr("value", "***")
	h, err := goquery.OuterHtml(cp)
	if err != nil {
		logx.Debugf(logComponent, "снимок %s/%s не собран: %v", kind, name, err)
		return
	}
	h = sesskeyJSRe.ReplaceAllString(h, `"sesskey":"***"`)
	logx.Dump("quiz-"+kind, name, []byte(h))
}

// clip обрезает строку до n рун для одной строки лога.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// QuizInfo — данные о тесте со страницы /mod/quiz/view.php?id=<cmid>.
type QuizInfo struct {
	CMID                int64         `json:"cmid"`
	CanStart            bool          `json:"can_start"`
	InProgressAttemptID int64         `json:"in_progress_attempt_id,omitempty"`
	Rules               []string      `json:"rules,omitempty"`
	Feedback            string        `json:"feedback,omitempty"`
	Attempts            []QuizAttempt `json:"attempts,omitempty"`
}

// QuizAttempt — запись из таблицы попыток на странице view.php.
type QuizAttempt struct {
	Number    int    `json:"number"`
	State     string `json:"state"`
	Grade     string `json:"grade,omitempty"`
	ReviewURL string `json:"review_url,omitempty"`
}

// AttemptPage — страница попытки прохождения теста /mod/quiz/attempt.php.
type AttemptPage struct {
	CMID      int64          `json:"cmid"`
	AttemptID int64          `json:"attempt_id"`
	Page      int            `json:"page"`
	NextPage  int            `json:"nextpage"` // -1 при переходе к сводке/завершению
	Slots     []int          `json:"slots"`
	TimeLeft  string         `json:"time_left,omitempty"`
	Questions []QuizQuestion `json:"questions"`
}

// QuizQuestion — разобранный блок вопроса .que.
type QuizQuestion struct {
	Slot          int    `json:"slot"`
	QubaID        int64  `json:"quba_id"`
	QType         string `json:"qtype"`     // multichoice, shortanswer, numerical, match, truefalse
	Behaviour     string `json:"behaviour"` // adaptive, deferredfeedback
	State         string `json:"state"`     // notyetanswered, complete, correct, incorrect
	SequenceCheck int    `json:"sequence_check"`
	Text          string `json:"text"`
	Prompt        string `json:"prompt,omitempty"`
	// Images — адреса рисунков условия по порядку («[рисунок N]» в Text).
	Images            []string      `json:"images,omitempty"`
	Choices           []QuizChoice  `json:"choices,omitempty"`
	InputName         string        `json:"input_name,omitempty"`
	InputValue        string        `json:"input_value,omitempty"`
	MatchOptions      []MatchOption `json:"match_options,omitempty"`
	HasSubmitButton   bool          `json:"has_submit_button"`
	SubmitButtonName  string        `json:"submit_button_name,omitempty"`
	SubmitButtonValue string        `json:"submit_button_value,omitempty"`
}

// QuizChoice — вариант ответа для multichoice и truefalse.
type QuizChoice struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Text    string `json:"text"`
	Checked bool   `json:"checked"`
	IsMulti bool   `json:"is_multi"`
}

// MatchOption — строка в вопросе на соответствие (que match).
type MatchOption struct {
	Name          string         `json:"name"`
	Question      string         `json:"question"`
	SelectedValue string         `json:"selected_value,omitempty"`
	Choices       []SelectOption `json:"choices"`
}

// SelectOption — опция выбора из <select>.
type SelectOption struct {
	Value string `json:"value"`
	Text  string `json:"text"`
}

// QuizAnswer — ответ на вопрос теста для отправки.
type QuizAnswer struct {
	Text    string            `json:"text,omitempty"`
	Choice  string            `json:"choice,omitempty"`
	Choices []string          `json:"choices,omitempty"`
	Matches map[string]string `json:"matches,omitempty"`
}

// QuizResult — итоговый результат завершённого теста.
type QuizResult struct {
	CMID          int64            `json:"cmid"`
	AttemptID     int64            `json:"attempt_id"`
	Status        string           `json:"status"`
	Grade         string           `json:"grade"`
	Feedback      string           `json:"feedback,omitempty"`
	ReviewAllowed bool             `json:"review_allowed"`
	Questions     []QuestionResult `json:"questions,omitempty"`
}

// QuestionResult — статус и балл за конкретный вопрос после завершения.
type QuestionResult struct {
	Slot   int    `json:"slot"`
	Status string `json:"status"`
	Grade  string `json:"grade,omitempty"`
	// RightAnswer — текст «Правильный ответ: …» со страницы review.
	RightAnswer string `json:"right_answer,omitempty"`
	// Feedback — отзыв к вопросу (.feedback / .specificfeedback без правильного ответа).
	Feedback string `json:"feedback,omitempty"`
}

// InspectQuiz парсит /mod/quiz/view.php?id=<cmid> и извлекает информацию о тесте.
func (c *Client) InspectQuiz(ctx context.Context, cmid int64) (*QuizInfo, error) {
	doc, _, err := c.PageWithURL(ctx, fmt.Sprintf("/mod/quiz/view.php?id=%d", cmid))
	if err != nil {
		return nil, fmt.Errorf("moodle: inspect quiz %d: %w", cmid, err)
	}
	dumpDoc("view", fmt.Sprintf("cmid%d", cmid), doc)

	info := &QuizInfo{CMID: cmid}

	// Ищем незавершённую попытку (в процессе)
	doc.Find("table.quizreviewsummary tr, table.quizattemptsummary tr").Each(func(_ int, tr *goquery.Selection) {
		t := strings.ToLower(clean(tr.Text()))
		if strings.Contains(t, "в процессе") || strings.Contains(t, "in progress") || strings.Contains(t, "не завершено") {
			if href := tr.Find("a[href*='attempt.php']").AttrOr("href", ""); href != "" {
				if m := attemptIDRe.FindStringSubmatch(href); len(m) == 2 {
					id, _ := strconv.ParseInt(m[1], 10, 64)
					if id > 0 {
						info.InProgressAttemptID = id
					}
				}
			}
		}
	})
	if info.InProgressAttemptID == 0 {
		if btnHref := doc.Find("a.btn[href*='attempt.php'], .quizstartbuttondiv a[href*='attempt.php']").AttrOr("href", ""); btnHref != "" {
			if m := attemptIDRe.FindStringSubmatch(btnHref); len(m) == 2 {
				id, _ := strconv.ParseInt(m[1], 10, 64)
				if id > 0 {
					info.InProgressAttemptID = id
				}
			}
		}
	}

	canStart := doc.Find(".quizstartbuttondiv").Length() > 0 ||
		doc.Find("form[action*='startattempt.php']").Length() > 0 ||
		doc.Find("form[action*='attempt.php'], a[href*='attempt.php']").Length() > 0 ||
		info.InProgressAttemptID > 0
	info.CanStart = canStart

	// Правила
	doc.Find(".quizinfo p").Each(func(_ int, p *goquery.Selection) {
		if t := clean(p.Text()); t != "" {
			info.Rules = append(info.Rules, t)
		}
	})

	// Отзыв / итоговая оценка
	info.Feedback = clean(doc.Find("#feedback").Text())

	// Таблица прошлых попыток
	doc.Find("table.quizreviewsummary tbody tr, table.quizattemptsummary tbody tr").Each(func(i int, tr *goquery.Selection) {
		if tr.Find("th").Length() > 0 && tr.Find("td").Length() == 0 {
			return
		}
		var state, grade, reviewURL string
		tr.Find("td").Each(func(_ int, td *goquery.Selection) {
			cl := td.AttrOr("class", "")
			txt := clean(td.Text())
			switch {
			case strings.Contains(cl, "state") || strings.Contains(strings.ToLower(txt), "заверш") || strings.Contains(strings.ToLower(txt), "процесс"):
				state = txt
			case strings.Contains(cl, "grade") || strings.Contains(txt, "из") || strings.Contains(txt, "/"):
				grade = txt
			}
			if a := td.Find("a[href*='review.php']"); a.Length() > 0 {
				reviewURL = a.AttrOr("href", "")
			}
		})
		if state != "" || grade != "" || reviewURL != "" {
			info.Attempts = append(info.Attempts, QuizAttempt{
				Number:    i + 1,
				State:     state,
				Grade:     grade,
				ReviewURL: reviewURL,
			})
		}
	})

	logx.Debugf(logComponent, "тест cmid=%d: can_start=%t, незавершённая попытка=%d, правил=%d, попыток=%d, feedback=%q",
		cmid, info.CanStart, info.InProgressAttemptID, len(info.Rules), len(info.Attempts), clip(info.Feedback, 300))
	for _, r := range info.Rules {
		logx.Debugf(logComponent, "тест cmid=%d правило: %q", cmid, r)
	}
	for _, a := range info.Attempts {
		logx.Debugf(logComponent, "тест cmid=%d попытка №%d: состояние=%q оценка=%q review=%t",
			cmid, a.Number, a.State, a.Grade, a.ReviewURL != "")
	}
	if !info.CanStart {
		page := doc.Find("#region-main")
		if page.Length() == 0 {
			page = doc.Find("body")
		}
		logx.Infof(logComponent, "тест cmid=%d сейчас нельзя начать, текст страницы: %q", cmid, clip(clean(page.Text()), 300))
	}

	return info, nil
}

// StartOrResumeAttempt начинает новую попытку либо возвращает страницу активной незавершённой попытки.
func (c *Client) StartOrResumeAttempt(ctx context.Context, cmid int64) (*AttemptPage, error) {
	info, err := c.InspectQuiz(ctx, cmid)
	if err != nil {
		return nil, err
	}

	if info.InProgressAttemptID > 0 {
		logx.Infof(logComponent, "тест cmid=%d: продолжаю незавершённую попытку attempt=%d (прошлых в таблице: %d)",
			cmid, info.InProgressAttemptID, len(info.Attempts))
		return c.GetAttemptPage(ctx, cmid, info.InProgressAttemptID, 0)
	}

	if !info.CanStart {
		return nil, fmt.Errorf("moodle: quiz %d is not available to start", cmid)
	}

	logx.Infof(logComponent, "тест cmid=%d: начинаю новую попытку (прошлых в таблице: %d)", cmid, len(info.Attempts))

	sesskey, err := c.Sesskey(ctx)
	if err != nil {
		return nil, err
	}

	vals := url.Values{
		"cmid":    []string{strconv.FormatInt(cmid, 10)},
		"sesskey": []string{sesskey},
		"_qf__mod_quiz_form_preflight_check_form": []string{"1"},
		"submitbutton": []string{"Начать попытку"},
	}

	doc, finalURL, err := c.PostForm(ctx, "/mod/quiz/startattempt.php", vals)
	if err != nil {
		return nil, fmt.Errorf("moodle: start attempt cmid=%d: %w", cmid, err)
	}

	attemptID := int64(0)
	if m := attemptIDRe.FindStringSubmatch(finalURL); len(m) == 2 {
		attemptID, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if attemptID == 0 {
		if raw := doc.Find("input[name=attempt]").AttrOr("value", ""); raw != "" {
			attemptID, _ = strconv.ParseInt(raw, 10, 64)
		}
	}
	if attemptID == 0 {
		return nil, fmt.Errorf("moodle: start attempt cmid=%d: attempt ID not found in %s", cmid, finalURL)
	}
	logx.Infof(logComponent, "тест cmid=%d: попытка начата, attempt=%d, url=%s", cmid, attemptID, finalURL)

	return parseAttemptPage(doc, cmid, attemptID)
}

// GetAttemptPage запрашивает страницу попытки attempt.php с номером page.
func (c *Client) GetAttemptPage(ctx context.Context, cmid, attemptID int64, page int) (*AttemptPage, error) {
	path := fmt.Sprintf("/mod/quiz/attempt.php?attempt=%d&cmid=%d", attemptID, cmid)
	if page > 0 {
		path += fmt.Sprintf("&page=%d", page)
	}
	doc, _, err := c.PageWithURL(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("moodle: get attempt page cmid=%d attempt=%d: %w", cmid, attemptID, err)
	}
	return parseAttemptPage(doc, cmid, attemptID)
}

func parseAttemptPage(doc *goquery.Document, cmid, attemptID int64) (*AttemptPage, error) {
	page := &AttemptPage{
		CMID:      cmid,
		AttemptID: attemptID,
		NextPage:  -1,
	}

	if tp := doc.Find("input[name=thispage]").AttrOr("value", ""); tp != "" {
		page.Page, _ = strconv.Atoi(tp)
	}
	if np := doc.Find("input[name=nextpage]").AttrOr("value", ""); np != "" {
		page.NextPage, _ = strconv.Atoi(np)
	}
	if slotsStr := doc.Find("input[name=slots]").AttrOr("value", ""); slotsStr != "" {
		for _, s := range strings.Split(slotsStr, ",") {
			s = strings.TrimSpace(s)
			if n, err := strconv.Atoi(s); err == nil {
				page.Slots = append(page.Slots, n)
			}
		}
	}
	page.TimeLeft = clean(doc.Find("#quiz-time-left, #quiz-timer").First().Text())

	dumpDoc("attempt", fmt.Sprintf("cmid%d-a%d-p%d", cmid, attemptID, page.Page), doc)

	ques := doc.Find(".que")
	if ques.Length() == 0 {
		logx.Warnf(logComponent, "попытка attempt=%d страница %d: на странице нет ни одного .que (снимок quiz-attempt)", attemptID, page.Page)
	}
	ques.Each(func(_ int, que *goquery.Selection) {
		q := parseQuestion(que)
		if q.Slot == 0 {
			logx.Warnf(logComponent, "попытка attempt=%d: .que без slot в id=%q пропущен, классы=%q",
				attemptID, que.AttrOr("id", ""), que.AttrOr("class", ""))
			return
		}
		page.Questions = append(page.Questions, q)
		logQuestion(que, q)
	})

	logx.Debugf(logComponent, "страница попытки attempt=%d: page=%d nextpage=%d slots=%v time_left=%q вопросов=%d",
		attemptID, page.Page, page.NextPage, page.Slots, page.TimeLeft, len(page.Questions))

	return page, nil
}

// logQuestion пишет подробности вопроса (Debugf) и предупреждает о типе,
// который решатель не умеет заполнять (Warnf с классами и полями .que).
func logQuestion(que *goquery.Selection, q QuizQuestion) {
	if len(q.Choices) == 0 && q.InputName == "" && len(q.MatchOptions) == 0 {
		var fields []string
		que.Find("input, select, textarea").Each(func(_ int, in *goquery.Selection) {
			tag := ""
			if len(in.Nodes) > 0 {
				tag = in.Nodes[0].Data
			}
			fields = append(fields, fmt.Sprintf("%s(name=%q,type=%q)", tag, in.AttrOr("name", ""), in.AttrOr("type", "")))
		})
		logx.Warnf(logComponent, "вопрос %d типа %q: решатель не умеет его заполнять, классы=%q, поля=[%s]",
			q.Slot, q.QType, que.AttrOr("class", ""), strings.Join(fields, " "))
	}
	if !logx.Verbose() {
		return
	}

	logx.Debugf(logComponent, "вопрос slot=%d quba=%d qtype=%s behaviour=%s state=%s sequencecheck=%d submit=%t(%s=%q) input=%s value=%q prompt=%q text=%q images=%q",
		q.Slot, q.QubaID, q.QType, q.Behaviour, q.State, q.SequenceCheck,
		q.HasSubmitButton, q.SubmitButtonName, q.SubmitButtonValue,
		q.InputName, q.InputValue, q.Prompt, clip(q.Text, 500), q.Images)
	for _, ch := range q.Choices {
		logx.Debugf(logComponent, "вопрос slot=%d вариант: name=%q value=%q text=%q checked=%t multi=%t",
			q.Slot, ch.Name, ch.Value, ch.Text, ch.Checked, ch.IsMulti)
	}
	for _, m := range q.MatchOptions {
		var opts []string
		for _, o := range m.Choices {
			opts = append(opts, fmt.Sprintf("%s=%s", o.Value, o.Text))
		}
		logx.Debugf(logComponent, "вопрос slot=%d match: name=%q вопрос=%q selected=%q варианты=[%s]",
			q.Slot, m.Name, m.Question, m.SelectedValue, strings.Join(opts, "; "))
	}
}

func parseQuestion(que *goquery.Selection) QuizQuestion {
	q := QuizQuestion{}

	idAttr := que.AttrOr("id", "")
	if m := qubaSlotIDRe.FindStringSubmatch(idAttr); len(m) == 3 {
		q.QubaID, _ = strconv.ParseInt(m[1], 10, 64)
		q.Slot, _ = strconv.Atoi(m[2])
	}

	classes := strings.Fields(que.AttrOr("class", ""))
	for _, cl := range classes {
		switch cl {
		case "multichoice", "shortanswer", "numerical", "match", "truefalse", "essay", "calculated", "ddwtos", "multianswer":
			q.QType = cl
		case "adaptive", "deferredfeedback", "immediatefeedback", "interactive", "adaptivenopenalty":
			q.Behaviour = cl
		case "notyetanswered", "complete", "correct", "incorrect", "partiallycorrect", "invalidanswer", "answersaved":
			q.State = cl
		}
	}
	if q.QType == "" && len(classes) >= 2 {
		q.QType = classes[1]
	}

	// Sequence check
	seqInput := que.Find("input[name$='_:sequencecheck']").First()
	if seqInput.Length() > 0 {
		if m := seqCheckNameRe.FindStringSubmatch(seqInput.AttrOr("name", "")); len(m) == 3 {
			if q.QubaID == 0 {
				q.QubaID, _ = strconv.ParseInt(m[1], 10, 64)
			}
			if q.Slot == 0 {
				q.Slot, _ = strconv.Atoi(m[2])
			}
		}
		q.SequenceCheck, _ = strconv.Atoi(seqInput.AttrOr("value", "1"))
	}

	// Текст вопроса со склейкой битовых строк
	q.Text, q.Images = extractQTextImages(que.Find(".qtext"), "")
	q.Prompt = clean(que.Find(".prompt").Text())

	// Варианты для multichoice и truefalse
	checkboxes := que.Find(".answer input[type=checkbox]")
	radios := que.Find(".answer input[type=radio]")
	if checkboxes.Length() > 0 {
		checkboxes.Each(func(_ int, in *goquery.Selection) {
			name := in.AttrOr("name", "")
			val := in.AttrOr("value", "1")
			_, checked := in.Attr("checked")
			txt := findChoiceLabel(in)
			q.Choices = append(q.Choices, QuizChoice{
				Name:    name,
				Value:   val,
				Text:    txt,
				Checked: checked,
				IsMulti: true,
			})
		})
	} else if radios.Length() > 0 {
		radios.Each(func(_ int, in *goquery.Selection) {
			name := in.AttrOr("name", "")
			val := in.AttrOr("value", "")
			_, checked := in.Attr("checked")
			txt := findChoiceLabel(in)
			q.InputName = name
			q.Choices = append(q.Choices, QuizChoice{
				Name:    name,
				Value:   val,
				Text:    txt,
				Checked: checked,
				IsMulti: false,
			})
		})
	}

	// Поля ввода для shortanswer / numerical
	if textInput := que.Find(".answer input[type=text], .answer input[type=number]").First(); textInput.Length() > 0 {
		q.InputName = textInput.AttrOr("name", "")
		q.InputValue = textInput.AttrOr("value", "")
	}

	// Вопросы на соответствие match
	que.Find(".answer table tr").Each(func(_ int, tr *goquery.Selection) {
		subQ := clean(tr.Find("td.text").First().Text())
		sel := tr.Find("select").First()
		if sel.Length() == 0 {
			return
		}
		opt := MatchOption{
			Name:          sel.AttrOr("name", ""),
			Question:      subQ,
			SelectedValue: sel.Find("option[selected]").AttrOr("value", ""),
		}
		sel.Find("option").Each(func(_ int, o *goquery.Selection) {
			val := o.AttrOr("value", "")
			txt := clean(o.Text())
			if val != "" && txt != "" {
				opt.Choices = append(opt.Choices, SelectOption{Value: val, Text: txt})
			}
		})
		if opt.Name != "" {
			q.MatchOptions = append(q.MatchOptions, opt)
		}
	})

	// Кнопка проверки в режиме adaptive
	if submitBtn := que.Find("button[name$='_-submit'], input[type=submit][name$='_-submit']").First(); submitBtn.Length() > 0 {
		q.HasSubmitButton = true
		q.SubmitButtonName = submitBtn.AttrOr("name", "")
		q.SubmitButtonValue = submitBtn.AttrOr("value", "1")
	}

	return q
}

func findChoiceLabel(in *goquery.Selection) string {
	id := in.AttrOr("id", "")
	if id != "" {
		if lbl := in.Closest(".que").Find(fmt.Sprintf("label[for='%s']", id)).First(); lbl.Length() > 0 {
			if r := lbl.Find("[data-region='answer-label']").First(); r.Length() > 0 {
				return clean(r.Text())
			}
			lblClone := lbl.Clone()
			lblClone.Find(".answernumber, .accesshide").Remove()
			if t := clean(lblClone.Text()); t != "" {
				return t
			}
			if alt := clean(lblClone.Find("img").AttrOr("alt", "")); alt != "" {
				return "[рисунок: " + alt + "]"
			}
			return ""
		}
	}
	parent := in.Closest(".r0, .r1, div, p")
	if r := parent.Find("[data-region='answer-label']").First(); r.Length() > 0 {
		return clean(r.Text())
	}
	parentClone := parent.Clone()
	parentClone.Find("input, .answernumber, .accesshide").Remove()
	return clean(parentClone.Text())
}

// isBitString определяет, состоит ли строка исключительно из 0 и 1 (с возможными пробелами).
func isBitString(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 3 {
		return false
	}
	for _, r := range s {
		if r != '0' && r != '1' && r != ' ' {
			return false
		}
	}
	return true
}

// extractQText — текст вопроса .qtext по порядку блоков: абзацы, списки,
// таблицы (строками «a | b | c») и рисунки (заглушка «[рисунок N]», адрес —
// в images). Соседние строки из одних битов склеиваются без разделителя:
// длинные кодовые последовательности Moodle режет на абзацы.
func extractQText(sel *goquery.Selection) string {
	t, _ := extractQTextImages(sel, "")
	return t
}

func extractQTextImages(sel *goquery.Selection, base string) (string, []string) {
	if sel.Length() == 0 {
		return "", nil
	}
	w := &qtextWalker{base: base}
	for _, n := range sel.Nodes {
		w.walk(n)
	}
	w.flush()

	var b strings.Builder
	prevIsBits := false
	for i, line := range w.lines {
		currIsBits := isBitString(line)
		if i > 0 && !(prevIsBits && currIsBits) {
			b.WriteString("\n")
		}
		b.WriteString(line)
		prevIsBits = currIsBits
	}
	return b.String(), w.images
}

type qtextWalker struct {
	base   string
	cur    strings.Builder
	lines  []string
	images []string
}

func (w *qtextWalker) flush() {
	if t := clean(w.cur.String()); t != "" {
		w.lines = append(w.lines, t)
	}
	w.cur.Reset()
}

var qtextBlocks = map[string]bool{
	"p": true, "div": true, "li": true, "ul": true, "ol": true, "pre": true, "br": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "blockquote": true,
}

func (w *qtextWalker) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		w.cur.WriteString(n.Data)
		return
	case html.ElementNode:
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			w.walk(c)
		}
		return
	}
	switch n.Data {
	case "script", "style":
		return
	case "table":
		w.flush()
		w.table(n)
		return
	case "img":
		w.image(n)
		return
	}
	block := qtextBlocks[n.Data]
	if block {
		w.flush()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c)
	}
	if block {
		w.flush()
	}
}

func (w *qtextWalker) image(n *html.Node) {
	sel := goquery.NewDocumentFromNode(n).Selection
	src := strings.TrimSpace(sel.AttrOr("src", ""))
	alt := clean(sel.AttrOr("alt", ""))
	if src == "" || strings.HasPrefix(src, "data:") {
		if alt != "" {
			fmt.Fprintf(&w.cur, " [рисунок: %s] ", alt)
		}
		return
	}
	if w.base != "" {
		if bu, err := url.Parse(w.base); err == nil {
			if u, err := bu.Parse(src); err == nil {
				src = u.String()
			}
		}
	}
	w.images = append(w.images, src)
	if alt != "" && !strings.EqualFold(alt, "image") {
		fmt.Fprintf(&w.cur, " [рисунок %d: %s] ", len(w.images), alt)
	} else {
		fmt.Fprintf(&w.cur, " [рисунок %d] ", len(w.images))
	}
}

// table — каждая строка таблицы отдельной строкой «ячейка | ячейка».
func (w *qtextWalker) table(n *html.Node) {
	goquery.NewDocumentFromNode(n).Find("tr").Each(func(_ int, tr *goquery.Selection) {
		var cells []string
		tr.Children().Each(func(_ int, td *goquery.Selection) {
			if td.Is("td, th") {
				// Общая нумерация рисунков с остальным текстом.
				sub := &qtextWalker{base: w.base, images: w.images}
				for _, c := range td.Nodes {
					sub.walk(c)
				}
				sub.flush()
				w.images = sub.images
				cells = append(cells, strings.Join(sub.lines, " "))
			}
		})
		if len(cells) > 0 {
			w.lines = append(w.lines, strings.Join(cells, " | "))
		}
	})
}

// FetchFile скачивает файл Moodle (рисунок вопроса из pluginfile.php) в
// сессии пользователя. Возвращает тело и Content-Type.
func (c *Client) FetchFile(ctx context.Context, src string) ([]byte, string, error) {
	resp, body, err := c.s.Get(ctx, src, "image/*,*/*")
	if err != nil {
		return nil, "", err
	}
	if onLoginPage(resp) {
		return nil, "", ErrUnauthorized
	}
	if resp.StatusCode/100 != 2 {
		logx.Debugf(logComponent, "файл %s: статус %d", src, resp.StatusCode)
		return nil, "", fmt.Errorf("moodle: file status %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	logx.Debugf(logComponent, "файл %s: тип %q, %d байт", src, ct, len(body))
	return body, ct, nil
}

// SubmitAnswers отправляет ответы формы #responseform на /mod/quiz/processattempt.php?cmid=<cmid>.
// При checkSlot > 0 добавляет кнопку q<qubaid>:<slot>_-submit=1 для проверки конкретного вопроса.
func (c *Client) SubmitAnswers(ctx context.Context, page *AttemptPage, answers map[int]QuizAnswer, checkSlot int) (*AttemptPage, error) {
	sesskey, err := c.Sesskey(ctx)
	if err != nil {
		return nil, err
	}

	fields := []FormField{
		{Name: "attempt", Value: strconv.FormatInt(page.AttemptID, 10)},
		{Name: "thispage", Value: strconv.Itoa(page.Page)},
		{Name: "timeup", Value: "0"},
		{Name: "sesskey", Value: sesskey},
		{Name: "mdlscrollto", Value: "0"},
	}

	nextPage := page.NextPage
	if checkSlot > 0 {
		nextPage = page.Page
	}
	fields = append(fields, FormField{Name: "nextpage", Value: strconv.Itoa(nextPage)})

	slotStrs := make([]string, len(page.Slots))
	for i, s := range page.Slots {
		slotStrs[i] = strconv.Itoa(s)
	}
	fields = append(fields, FormField{Name: "slots", Value: strings.Join(slotStrs, ",")})

	for _, q := range page.Questions {
		fields = append(fields, FormField{
			Name:  fmt.Sprintf("q%d:%d_:flagged", q.QubaID, q.Slot),
			Value: "0",
		})
		fields = append(fields, FormField{
			Name:  fmt.Sprintf("q%d:%d_:sequencecheck", q.QubaID, q.Slot),
			Value: strconv.Itoa(q.SequenceCheck),
		})

		ans, hasAns := answers[q.Slot]

		switch {
		case len(q.Choices) > 0 && q.Choices[0].IsMulti:
			// multichoice checkbox
			for _, ch := range q.Choices {
				// Всегда отправляется hidden=0
				fields = append(fields, FormField{Name: ch.Name, Value: "0"})

				checked := false
				if hasAns {
					for _, c := range ans.Choices {
						if c == ch.Value || c == ch.Name || c == ch.Text {
							checked = true
							break
						}
					}
				} else {
					checked = ch.Checked
				}
				if checked {
					fields = append(fields, FormField{Name: ch.Name, Value: "1"})
				}
			}

		case len(q.Choices) > 0 && !q.Choices[0].IsMulti:
			// multichoice radio / truefalse
			val := ""
			if hasAns {
				val = ans.Choice
				if val == "" && len(ans.Choices) > 0 {
					val = ans.Choices[0]
				}
				if val == "" && ans.Text != "" {
					for _, ch := range q.Choices {
						if ch.Text == ans.Text {
							val = ch.Value
							break
						}
					}
				}
			}
			if val == "" {
				for _, ch := range q.Choices {
					if ch.Checked {
						val = ch.Value
						break
					}
				}
			}
			if val != "" {
				fields = append(fields, FormField{Name: q.InputName, Value: val})
			}

		case q.InputName != "":
			// shortanswer / numerical
			val := q.InputValue
			if hasAns && ans.Text != "" {
				val = ans.Text
			}
			fields = append(fields, FormField{Name: q.InputName, Value: val})

		case len(q.MatchOptions) > 0:
			// match
			for _, opt := range q.MatchOptions {
				val := opt.SelectedValue
				if hasAns && ans.Matches != nil {
					if v, ok := ans.Matches[opt.Name]; ok {
						val = v
					}
				}
				fields = append(fields, FormField{Name: opt.Name, Value: val})
			}
		}
	}

	if checkSlot > 0 {
		for _, q := range page.Questions {
			if q.Slot == checkSlot && q.HasSubmitButton {
				fields = append(fields, FormField{
					Name:  q.SubmitButtonName,
					Value: q.SubmitButtonValue,
				})
				break
			}
		}
	} else {
		fields = append(fields, FormField{Name: "next", Value: "Следующая страница"})
	}

	slots := make([]int, 0, len(answers))
	for s := range answers {
		slots = append(slots, s)
	}
	sort.Ints(slots)
	for _, s := range slots {
		b, _ := json.Marshal(answers[s])
		logx.Debugf(logComponent, "отправляю ответ attempt=%d page=%d slot=%d: %s", page.AttemptID, page.Page, s, b)
	}
	logx.Debugf(logComponent, "отправка attempt=%d page=%d: checkSlot=%d nextpage=%d полей=%d",
		page.AttemptID, page.Page, checkSlot, nextPage, len(fields))

	path := fmt.Sprintf("/mod/quiz/processattempt.php?cmid=%d", page.CMID)
	doc, finalURL, err := c.PostMultipart(ctx, path, fields)
	if err != nil {
		logx.Warnf(logComponent, "отправка ответов attempt=%d page=%d не удалась: %v", page.AttemptID, page.Page, err)
		return nil, fmt.Errorf("moodle: submit answers cmid=%d attempt=%d: %w", page.CMID, page.AttemptID, err)
	}

	if strings.Contains(finalURL, "summary.php") {
		logx.Debugf(logComponent, "после отправки attempt=%d page=%d: редирект на summary, url=%s", page.AttemptID, page.Page, finalURL)
		return &AttemptPage{
			CMID:      page.CMID,
			AttemptID: page.AttemptID,
			Page:      -1,
			NextPage:  -1,
		}, nil
	}

	res, err := parseAttemptPage(doc, page.CMID, page.AttemptID)
	if err == nil {
		logx.Debugf(logComponent, "после отправки attempt=%d: вернулась страница %d (nextpage=%d), вопросов=%d, url=%s",
			page.AttemptID, res.Page, res.NextPage, len(res.Questions), finalURL)
	}
	return res, err
}

// FinishAttempt завершает попытку теста и извлекает итоговый балл и статус.
func (c *Client) FinishAttempt(ctx context.Context, cmid, attemptID int64) (*QuizResult, error) {
	// 1. Запрашиваем summary.php для обновления sesskey
	sumDoc, _, err := c.PageWithURL(ctx, fmt.Sprintf("/mod/quiz/summary.php?attempt=%d&cmid=%d", attemptID, cmid))
	if err != nil {
		return nil, fmt.Errorf("moodle: finish attempt summary cmid=%d attempt=%d: %w", cmid, attemptID, err)
	}
	dumpDoc("summary", fmt.Sprintf("cmid%d-a%d", cmid, attemptID), sumDoc)

	sesskey, err := c.Sesskey(ctx)
	if err != nil {
		return nil, err
	}

	vals := url.Values{
		"attempt":       []string{strconv.FormatInt(attemptID, 10)},
		"finishattempt": []string{"1"},
		"timeup":        []string{"0"},
		"slots":         []string{""},
		"cmid":          []string{strconv.FormatInt(cmid, 10)},
		"sesskey":       []string{sesskey},
	}

	doc, finalURL, err := c.PostForm(ctx, "/mod/quiz/processattempt.php", vals)
	if err != nil {
		return nil, fmt.Errorf("moodle: finish attempt cmid=%d attempt=%d: %w", cmid, attemptID, err)
	}

	res := &QuizResult{
		CMID:      cmid,
		AttemptID: attemptID,
		Status:    "Завершено",
	}

	if strings.Contains(finalURL, "review.php") {
		res.ReviewAllowed = true
		dumpDoc("review", fmt.Sprintf("cmid%d-a%d", cmid, attemptID), doc)
		doc.Find("table.quizreviewsummary tr").Each(func(_ int, tr *goquery.Selection) {
			th := strings.ToLower(clean(tr.Find("th").Text()))
			td := clean(tr.Find("td").Text())
			switch {
			case strings.Contains(th, "состояние") || strings.Contains(th, "state"):
				if td != "" {
					res.Status = td
				}
			case strings.Contains(th, "оценка") || strings.Contains(th, "grade") || strings.Contains(th, "баллы"):
				if td != "" {
					res.Grade = td
				}
			}
		})
		res.Feedback = clean(doc.Find("#feedback").Text())
		res.Questions = parseReview(doc)
	} else {
		// Редирект на view.php (просмотр запрещён)
		res.ReviewAllowed = false
		dumpDoc("view", fmt.Sprintf("cmid%d-a%d", cmid, attemptID), doc)
		res.Feedback = clean(doc.Find("#feedback").Text())

		doc.Find("table.quizreviewsummary tbody tr, table.quizattemptsummary tbody tr").Each(func(_ int, tr *goquery.Selection) {
			tr.Find("td").Each(func(_ int, td *goquery.Selection) {
				txt := clean(td.Text())
				if strings.Contains(txt, "из") || strings.Contains(txt, "%") {
					res.Grade = txt
				}
			})
		})
		if res.Grade == "" && res.Feedback != "" {
			res.Grade = res.Feedback
		}
	}

	logx.Infof(logComponent, "тест cmid=%d попытка attempt=%d завершена: review_allowed=%t status=%q grade=%q feedback=%q",
		cmid, attemptID, res.ReviewAllowed, res.Status, res.Grade, clip(res.Feedback, 300))
	for _, q := range res.Questions {
		logx.Debugf(logComponent, "итог attempt=%d slot=%d: состояние=%q балл=%q правильный=%q отзыв=%q",
			attemptID, q.Slot, q.Status, q.Grade, clip(q.RightAnswer, 300), clip(q.Feedback, 300))
	}

	return res, nil
}

// parseReview разбирает вопросы со страницы review.php: состояние и балл из
// .info, правильный ответ из .rightanswer, отзыв из .feedback/.specificfeedback
// (без повтора текста правильного ответа и вложенных блоков).
func parseReview(doc *goquery.Document) []QuestionResult {
	var out []QuestionResult
	doc.Find(".que").Each(func(_ int, que *goquery.Selection) {
		slot := 0
		if m := qubaSlotIDRe.FindStringSubmatch(que.AttrOr("id", "")); len(m) == 3 {
			slot, _ = strconv.Atoi(m[2])
		}
		if slot == 0 {
			return
		}

		var right []string
		que.Find(".rightanswer").Each(func(_ int, r *goquery.Selection) {
			if t := clean(r.Text()); t != "" {
				right = append(right, t)
			}
		})

		var fb []string
		que.Find(".feedback, .specificfeedback").Each(func(_ int, f *goquery.Selection) {
			if f.ParentsFiltered(".feedback, .specificfeedback").Length() > 0 {
				return // уже учтён во внешнем блоке
			}
			cl := f.Clone()
			cl.Find(".rightanswer").Remove()
			if t := clean(cl.Text()); t != "" {
				fb = append(fb, t)
			}
		})

		out = append(out, QuestionResult{
			Slot:        slot,
			Status:      clean(que.Find(".info .state").Text()),
			Grade:       clean(que.Find(".info .grade").Text()),
			RightAnswer: strings.Join(right, " | "),
			Feedback:    strings.Join(fb, " | "),
		})
	})
	return out
}
