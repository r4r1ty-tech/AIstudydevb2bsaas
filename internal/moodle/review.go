package moodle

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
)

// ErrReviewClosed — просмотр попытки закрыт настройками теста: review.php перенаправил на view.php.
var ErrReviewClosed = errors.New("moodle: review of attempt is not allowed")

// Verdict — отметка Moodle о правильности ответа на вопрос.
type Verdict string

const (
	VerdictUnknown   Verdict = ""          // отметки нет: скрыта настройками или не распознана
	VerdictCorrect   Verdict = "correct"   // «Верно», «Ваш ответ верный», баллы из максимума
	VerdictPartial   Verdict = "partial"   // «Частично правильно»
	VerdictIncorrect Verdict = "incorrect" // «Неверно», 0 баллов
)

// reviewEps — допуск при сравнении баллов с максимумом (Moodle пишет два знака после запятой).
const reviewEps = 1e-6

// gradeFractionRe — дробь «N из M» или «N/M» в баллах Moodle («Баллов: 1,00 из 1,00», «10,00/10,00»).
var gradeFractionRe = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*(?:из|/)\s*(\d+(?:[.,]\d+)?)`)

// ReviewQuestion — вопрос со страницы просмотра попытки: условие, варианты, ответ студента и отметка.
type ReviewQuestion struct {
	QuizQuestion
	// Verdict — отметка Moodle по вопросу (VerdictUnknown, если её не показано).
	Verdict Verdict `json:"verdict"`
	// Grade — строка баллов из блока вопроса («Баллов: 1,00 из 1,00»), пусто если скрыта.
	Grade string `json:"grade,omitempty"`
	// ByTotal — отметки по вопросам скрыты настройками, а итог попытки максимальный:
	// ответ считается правильным.
	ByTotal bool `json:"by_total,omitempty"`
	// RightAnswer — текст блока «Правильный ответ: …» (пусто, если Moodle его не показал).
	RightAnswer string `json:"right_answer,omitempty"`
}

// Correct — ответ подтверждён: отметкой Moodle или итогом попытки при скрытой правильности.
func (q ReviewQuestion) Correct() bool {
	return q.Verdict == VerdictCorrect || q.ByTotal
}

// ReviewPage — разобранная страница просмотра попытки review.php.
type ReviewPage struct {
	CMID      int64            `json:"cmid"`
	AttemptID int64            `json:"attempt_id"`
	Grade     string           `json:"grade,omitempty"`
	TotalMax  bool             `json:"total_max"`
	Questions []ReviewQuestion `json:"questions"`
}

// GetReview запрашивает /mod/quiz/review.php (все вопросы на одной странице) и разбирает их.
// Если Moodle перенаправил на view.php — просмотр закрыт, возвращается ErrReviewClosed.
func (c *Client) GetReview(ctx context.Context, cmid, attemptID int64) (*ReviewPage, error) {
	path := fmt.Sprintf("/mod/quiz/review.php?attempt=%d&cmid=%d&showall=1", attemptID, cmid)
	doc, finalURL, err := c.PageWithURL(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("moodle: review cmid=%d attempt=%d: %w", cmid, attemptID, err)
	}
	if !strings.Contains(finalURL, "review.php") {
		logx.Infof(logComponent, "попытка attempt=%d теста cmid=%d: просмотр закрыт настройками, url=%s", attemptID, cmid, finalURL)
		return nil, ErrReviewClosed
	}
	dumpDoc("review", fmt.Sprintf("cmid%d-a%d-show", cmid, attemptID), doc)
	rp := parseReviewPage(doc, cmid, attemptID)

	var right, wrong, partial, none int
	for _, q := range rp.Questions {
		switch q.Verdict {
		case VerdictCorrect:
			right++
		case VerdictIncorrect:
			wrong++
		case VerdictPartial:
			partial++
		default:
			none++
		}
	}
	logx.Infof(logComponent, "просмотр attempt=%d теста cmid=%d: вопросов %d, итог %q (максимум=%t), отметки: верно %d, неверно %d, частично %d, без отметки %d",
		attemptID, cmid, len(rp.Questions), rp.Grade, rp.TotalMax, right, wrong, partial, none)
	return rp, nil
}

// parseReviewPage разбирает страницу review.php: итог попытки и каждый вопрос .que.
// Вопрос с отметкой по итогу (ByTotal) — когда отметки скрыты, а итог равен максимуму.
func parseReviewPage(doc *goquery.Document, cmid, attemptID int64) *ReviewPage {
	rp := &ReviewPage{CMID: cmid, AttemptID: attemptID}
	rp.Grade = reviewTotalGrade(doc)
	rp.TotalMax = gradeIsMax(rp.Grade)

	doc.Find(".que").Each(func(_ int, que *goquery.Selection) {
		q := parseQuestion(que)
		if q.Slot == 0 {
			return
		}
		rq := ReviewQuestion{QuizQuestion: q}
		rq.Grade = clean(que.Find(".info .grade").First().Text())
		rq.RightAnswer = clean(que.Find(".rightanswer").First().Text())
		rq.Verdict = reviewVerdict(que, q.Slot, rq.Grade)
		if rq.Verdict == VerdictUnknown && rp.TotalMax {
			rq.ByTotal = true
		}
		logx.Debugf(logComponent, "проверка attempt=%d slot=%d: отметка=%q баллы=%q по_итогу=%t",
			attemptID, q.Slot, rq.Verdict, rq.Grade, rq.ByTotal)
		rp.Questions = append(rp.Questions, rq)
	})
	return rp
}

// reviewTotalGrade — итог попытки: строка «Оценка» сводки, иначе блок #feedback со словом «оценка».
func reviewTotalGrade(doc *goquery.Document) string {
	var grade string
	doc.Find("table.quizreviewsummary tr").Each(func(_ int, tr *goquery.Selection) {
		th := strings.ToLower(clean(tr.Find("th").Text()))
		td := clean(tr.Find("td").Text())
		if td != "" && (strings.Contains(th, "оценка") || strings.Contains(th, "grade")) {
			grade = td
		}
	})
	if grade == "" {
		fb := clean(doc.Find("#feedback").Text())
		if l := strings.ToLower(fb); strings.Contains(l, "оценка") || strings.Contains(l, "grade") {
			grade = fb
		}
	}
	return grade
}

// reviewVerdict — отметка вопроса: по классам и тексту блока, при их отсутствии по баллам.
// Если отметка и баллы расходятся, отметки нет: в базу такой ответ не пойдёт.
func reviewVerdict(que *goquery.Selection, slot int, grade string) Verdict {
	mark := marksVerdict(que)
	byGrade := gradeVerdict(grade)
	switch {
	case mark == VerdictUnknown:
		return byGrade
	case byGrade == VerdictUnknown || byGrade == mark:
		return mark
	default:
		logx.Warnf(logComponent, "вопрос slot=%d: отметка %q расходится с баллами %q, отметку не используем", slot, mark, grade)
		return VerdictUnknown
	}
}

// marksVerdict — отметка из класса .que, блока состояния и фразы «Ваш ответ …».
func marksVerdict(que *goquery.Selection) Verdict {
	for _, cl := range strings.Fields(que.AttrOr("class", "")) {
		switch cl {
		case "correct":
			return VerdictCorrect
		case "partiallycorrect":
			return VerdictPartial
		case "incorrect":
			return VerdictIncorrect
		}
	}
	state := strings.ToLower(clean(que.Find(".info .state").First().Text()))
	if v := textVerdict(state); v != VerdictUnknown {
		return v
	}
	outcome := strings.ToLower(clean(que.Find(".outcome, .feedback").Text()))
	if i := strings.Index(outcome, "ваш ответ"); i >= 0 {
		seg := outcome[i:]
		if j := strings.IndexAny(seg, ".!\n"); j > 0 {
			seg = seg[:j]
		}
		return textVerdict(seg)
	}
	return VerdictUnknown
}

// textVerdict — отметка по слову. Порядок важен: «неверно» содержит «верно», «incorrect» — «correct».
func textVerdict(s string) Verdict {
	switch {
	case strings.Contains(s, "частично"), strings.Contains(s, "partially"):
		return VerdictPartial
	case strings.Contains(s, "неверн"), strings.Contains(s, "incorrect"):
		return VerdictIncorrect
	case strings.Contains(s, "верн"), strings.Contains(s, "correct"):
		return VerdictCorrect
	}
	return VerdictUnknown
}

// gradeVerdict — отметка по баллам «N из M»: полный балл — верно, ноль — неверно, между — частично.
func gradeVerdict(grade string) Verdict {
	n, d, ok := parseGradeFraction(grade)
	if !ok {
		return VerdictUnknown
	}
	switch {
	case n >= d-reviewEps:
		return VerdictCorrect
	case n <= reviewEps:
		return VerdictIncorrect
	default:
		return VerdictPartial
	}
}

// gradeIsMax — итог «N из M» с N равным M.
func gradeIsMax(grade string) bool {
	n, d, ok := parseGradeFraction(grade)
	return ok && n >= d-reviewEps
}

func parseGradeFraction(s string) (num, den float64, ok bool) {
	m := gradeFractionRe.FindStringSubmatch(s)
	if len(m) != 3 {
		return 0, 0, false
	}
	n, err1 := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64)
	d, err2 := strconv.ParseFloat(strings.Replace(m[2], ",", ".", 1), 64)
	if err1 != nil || err2 != nil || d <= 0 {
		return 0, 0, false
	}
	return n, d, true
}

// GradeFraction разбирает «N из M» (или «N/M») из строки баллов Moodle.
func GradeFraction(s string) (num, den float64, ok bool) {
	return parseGradeFraction(s)
}
