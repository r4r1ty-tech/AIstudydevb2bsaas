package mcpapp

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/moodle"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/websess"
)

const (
	// QuizInfoResourceTemplate — JSON-ресурс с QuizInfo по cmid теста.
	QuizInfoResourceTemplate = "moodle://quiz/{cmid}/info"

	// quizImagesDir — подкаталог StateDir для рисунков вопросов.
	quizImagesDir = "images"
	// maxInlineImageBytes — предел рисунка, который встраивается в ответ
	// base64. Больше — только файл в StateDir/images, чтобы не раздувать JSON-RPC.
	maxInlineImageBytes = 8 << 20
)

var (
	// ErrWritesDisabled — запись в Moodle выключена настройкой.
	ErrWritesDisabled = errors.New("mcpapp: запись в Moodle выключена (MOODLE_MCP_ALLOW_WRITES=0)")
	// ErrConfirmationRequired — write-действие без явного confirm=true.
	ErrConfirmationRequired = errors.New("mcpapp: действие меняет Moodle, нужен confirm=true")
	// ErrQuizBusy — по этому cmid уже выполняется start/submit/finish.
	ErrQuizBusy = errors.New("mcpapp: по этому cmid уже идёт запись")
	// ErrUnsupportedQuestion — тип вопроса, который сервер не умеет заполнять.
	ErrUnsupportedQuestion = errors.New("mcpapp: тип вопроса не поддерживается")
	// ErrQuizBadInput — ответ не соответствует странице попытки.
	ErrQuizBadInput = errors.New("mcpapp: некорректный ответ или слот")
)

// QuizInfoInput — аргументы moodle_quiz_info.
type QuizInfoInput struct {
	CMID int64 `json:"cmid" jsonschema:"id модуля теста (cmid) в курсе Moodle"`
}

// QuizInfoOutput — компактная информация о тесте. Секретов здесь нет.
type QuizInfoOutput struct {
	CMID                int64                `json:"cmid"`
	CanStart            bool                 `json:"can_start"`
	InProgressAttemptID int64                `json:"in_progress_attempt_id,omitempty"`
	Rules               []string             `json:"rules,omitempty"`
	Feedback            string               `json:"feedback,omitempty"`
	Attempts            []moodle.QuizAttempt `json:"attempts,omitempty"`
	Allowed             int                  `json:"allowed,omitempty"`
	Used                int                  `json:"used,omitempty"`
	AttemptsLeft        int                  `json:"attempts_left"`
	GradingMethod       string               `json:"grading_method,omitempty"`
}

// QuizStartInput — аргументы moodle_quiz_start.
type QuizStartInput struct {
	CMID    int64 `json:"cmid" jsonschema:"id модуля теста (cmid)"`
	Confirm bool  `json:"confirm,omitempty" jsonschema:"обязательно true: начать или продолжить попытку"`
}

// QuizPageInput — аргументы moodle_quiz_page.
type QuizPageInput struct {
	CMID      int64 `json:"cmid" jsonschema:"id модуля теста (cmid)"`
	AttemptID int64 `json:"attempt_id" jsonschema:"id попытки из moodle_quiz_start"`
	Page      int   `json:"page,omitempty" jsonschema:"номер страницы попытки, по умолчанию 0"`
}

// QuizAnswerInput — ответ на один вопрос. Поля зависят от типа вопроса.
type QuizAnswerInput struct {
	Text    string            `json:"text,omitempty" jsonschema:"текст для shortanswer/numerical"`
	Choice  string            `json:"choice,omitempty" jsonschema:"значение, name или текст варианта для multichoice/truefalse"`
	Choices []string          `json:"choices,omitempty" jsonschema:"несколько вариантов для multichoice с чекбоксами"`
	Matches map[string]string `json:"matches,omitempty" jsonschema:"match: имя select или текст строки → значение варианта"`
}

// QuizSubmitInput — аргументы moodle_quiz_submit.
type QuizSubmitInput struct {
	CMID      int64                      `json:"cmid" jsonschema:"id модуля теста (cmid)"`
	AttemptID int64                      `json:"attempt_id" jsonschema:"id попытки"`
	Page      int                        `json:"page,omitempty" jsonschema:"номер страницы, которую отправляем"`
	Answers   map[string]QuizAnswerInput `json:"answers" jsonschema:"ответы: ключ — номер слота строкой, значение — ответ по типу вопроса"`
	CheckSlot int                        `json:"check_slot,omitempty" jsonschema:"необязательно: слот adaptive-вопроса, который нужно проверить без перехода дальше"`
	Confirm   bool                       `json:"confirm,omitempty" jsonschema:"обязательно true: отправить ответы в Moodle"`
}

// QuizFinishInput — аргументы moodle_quiz_finish.
type QuizFinishInput struct {
	CMID      int64 `json:"cmid" jsonschema:"id модуля теста (cmid)"`
	AttemptID int64 `json:"attempt_id" jsonschema:"id попытки"`
	Confirm   bool  `json:"confirm,omitempty" jsonschema:"обязательно true: завершить попытку и получить оценку"`
}

// QuizReviewInput — аргументы moodle_quiz_review.
type QuizReviewInput struct {
	CMID      int64 `json:"cmid" jsonschema:"id модуля теста (cmid)"`
	AttemptID int64 `json:"attempt_id" jsonschema:"id завершённой попытки"`
}

// QuizImageInput — аргументы moodle_quiz_image.
type QuizImageInput struct {
	CMID      int64 `json:"cmid" jsonschema:"id модуля теста (cmid)"`
	AttemptID int64 `json:"attempt_id" jsonschema:"id попытки"`
	Slot      int   `json:"slot" jsonschema:"номер слота вопроса"`
	Index     int   `json:"index" jsonschema:"номер рисунка в вопросе, начиная с 0"`
}

// QuizPageOutput — страница попытки и её вопросы.
type QuizPageOutput struct {
	CMID        int64                 `json:"cmid"`
	AttemptID   int64                 `json:"attempt_id"`
	Page        int                   `json:"page"`
	NextPage    int                   `json:"next_page"`
	NeedsFinish bool                  `json:"needs_finish"`
	TimeLeft    string                `json:"time_left,omitempty"`
	Slots       []int                 `json:"slots,omitempty"`
	Questions   []moodle.QuizQuestion `json:"questions"`
}

// QuizImageOutput — скачанный рисунок вопроса.
type QuizImageOutput struct {
	CMID      int64  `json:"cmid"`
	AttemptID int64  `json:"attempt_id"`
	Slot      int    `json:"slot"`
	Index     int    `json:"index"`
	MIMEType  string `json:"mime_type"`
	Size      int64  `json:"size"`
	Path      string `json:"path,omitempty"`
	Inline    bool   `json:"inline"`
}

// quizGuard сериализует write-действия по cmid: две попытки одного теста
// одновременно не начинаются и не отправляются.
type quizGuard struct {
	mu   sync.Mutex
	busy map[int64]string
}

func newQuizGuard() *quizGuard {
	return &quizGuard{busy: make(map[int64]string)}
}

// acquire занимает cmid под действие action. Если cmid занят, возвращает
// ErrQuizBusy, а не ждёт: второй параллельный вызов — ошибка вызывающего.
func (g *quizGuard) acquire(cmid int64, action string) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cur, ok := g.busy[cmid]; ok {
		return nil, fmt.Errorf("%w: cmid=%d занят действием %s", ErrQuizBusy, cmid, cur)
	}
	g.busy[cmid] = action
	return func() {
		g.mu.Lock()
		delete(g.busy, cmid)
		g.mu.Unlock()
	}, nil
}

// registerQuizTools добавляет сценарий теста: информация, попытка, страницы,
// отправка, завершение, просмотр и рисунки. Write-инструменты выключены, пока
// не задан MOODLE_MCP_ALLOW_WRITES, и требуют confirm=true.
func registerQuizTools(s *mcp.Server, sess *Session) {
	guard := newQuizGuard()

	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_quiz_info",
		Title:       "Информация о тесте Moodle",
		Description: "Показывает тест Moodle по cmid: можно ли начать, есть ли незавершённая попытка, правила, число и метод оценивания попыток. Только чтение.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in QuizInfoInput) (*mcp.CallToolResult, QuizInfoOutput, error) {
		out, err := quizInfo(ctx, sess, in)
		if err != nil {
			return nil, QuizInfoOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_quiz_start",
		Title:       "Начать попытку теста",
		Description: "Начинает новую попытку теста или продолжает незавершённую и возвращает первую страницу с вопросами. Меняет Moodle: нужен MOODLE_MCP_ALLOW_WRITES=1 и confirm=true. Автоматически не вызывается.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in QuizStartInput) (*mcp.CallToolResult, QuizPageOutput, error) {
		out, err := startQuiz(ctx, sess, guard, in)
		if err != nil {
			return nil, QuizPageOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_quiz_page",
		Title:       "Страница попытки теста",
		Description: "Возвращает страницу попытки: вопросы, варианты, поля ввода, слоты, оставшееся время и номер следующей страницы. Только чтение.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in QuizPageInput) (*mcp.CallToolResult, QuizPageOutput, error) {
		out, err := pageQuiz(ctx, sess, in)
		if err != nil {
			return nil, QuizPageOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_quiz_submit",
		Title:       "Отправить ответы страницы",
		Description: "Отправляет ответы текущей страницы попытки. Сначала перечитывает страницу и проверяет слоты и типы вопросов; неизвестный тип (essay, drag&drop и т.п.) отклоняется без отправки. Нужен MOODLE_MCP_ALLOW_WRITES=1 и confirm=true.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in QuizSubmitInput) (*mcp.CallToolResult, QuizPageOutput, error) {
		out, err := submitQuiz(ctx, sess, guard, in)
		if err != nil {
			return nil, QuizPageOutput{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_quiz_finish",
		Title:       "Завершить попытку теста",
		Description: "Завершает попытку и возвращает итоговую оценку, состояние и результат по вопросам, если просмотр разрешён. Нужен MOODLE_MCP_ALLOW_WRITES=1 и confirm=true.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in QuizFinishInput) (*mcp.CallToolResult, moodle.QuizResult, error) {
		out, err := finishQuiz(ctx, sess, guard, in)
		if err != nil {
			return nil, moodle.QuizResult{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_quiz_review",
		Title:       "Просмотр попытки теста",
		Description: "Возвращает разбор завершённой попытки: вопросы, ответы, правильные ответы, баллы и отметки. Если просмотр закрыт настройками теста — ошибка review_closed. Только чтение.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in QuizReviewInput) (*mcp.CallToolResult, moodle.ReviewPage, error) {
		out, err := reviewQuiz(ctx, sess, in)
		if err != nil {
			return nil, moodle.ReviewPage{}, err
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "moodle_quiz_image",
		Title:       "Рисунок вопроса теста",
		Description: "Скачивает рисунок из условия вопроса (index с 0) через файловый allowlist Moodle, не больше 25 МБ. Небольшой рисунок возвращается как image content, любой — сохраняется в StateDir/images и отдаётся путём. Только чтение.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in QuizImageInput) (*mcp.CallToolResult, QuizImageOutput, error) {
		return quizImage(ctx, sess, in)
	})
}

// registerQuizResources добавляет шаблон moodle://quiz/{cmid}/info.
func registerQuizResources(s *mcp.Server, sess *Session) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: QuizInfoResourceTemplate,
		Name:        "quiz_info",
		Title:       "Информация о тесте Moodle",
		Description: "QuizInfo теста Moodle в JSON по cmid: правила, попытки, can_start.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := requestURI(req)
		id, err := parseTemplateID(uri, "moodle://quiz/", "/info")
		if err != nil {
			return nil, err
		}
		out, err := quizInfo(ctx, sess, QuizInfoInput{CMID: id})
		if err != nil {
			return nil, err
		}
		return jsonResource(uri, out)
	})
}

// quizInfo читает view.php теста и не делает ни одного POST.
func quizInfo(ctx context.Context, sess *Session, in QuizInfoInput) (QuizInfoOutput, error) {
	if in.CMID <= 0 {
		return QuizInfoOutput{}, toolFailure("bad_input", "укажите cmid теста из структуры курса", nil)
	}
	started := time.Now()
	var info *moodle.QuizInfo
	err := sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		var ierr error
		info, ierr = c.InspectQuiz(ctx, in.CMID)
		return ierr
	})
	if err != nil {
		return QuizInfoOutput{}, quizToolError(err, "проверьте cmid и сессию (moodle_status)")
	}
	out := QuizInfoOutput{
		CMID:                info.CMID,
		CanStart:            info.CanStart,
		InProgressAttemptID: info.InProgressAttemptID,
		Rules:               info.Rules,
		Feedback:            info.Feedback,
		Attempts:            info.Attempts,
		Allowed:             info.Allowed,
		Used:                info.Used,
		AttemptsLeft:        info.AttemptsLeft(),
		GradingMethod:       info.GradingMethod,
	}
	logx.Infof("mcpapp", "тест cmid=%d: info, can_start=%t, попытка=%d, попыток осталось=%d, %s",
		in.CMID, out.CanStart, out.InProgressAttemptID, out.AttemptsLeft, time.Since(started).Round(time.Millisecond))
	return out, nil
}

// startQuiz начинает или продолжает попытку под блокировкой cmid.
func startQuiz(ctx context.Context, sess *Session, guard *quizGuard, in QuizStartInput) (QuizPageOutput, error) {
	if in.CMID <= 0 {
		return QuizPageOutput{}, toolFailure("bad_input", "укажите cmid теста", nil)
	}
	if err := requireQuizWrite(sess, in.Confirm); err != nil {
		return QuizPageOutput{}, err
	}
	release, err := guard.acquire(in.CMID, "start")
	if err != nil {
		return QuizPageOutput{}, toolFailure("quiz_busy", "дождитесь завершения текущего действия по этому тесту", err)
	}
	defer release()

	started := time.Now()
	var page *moodle.AttemptPage
	err = sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		var serr error
		page, serr = c.StartOrResumeAttempt(ctx, in.CMID)
		return serr
	})
	if err != nil {
		return QuizPageOutput{}, quizToolError(err, "проверьте доступность теста (moodle_quiz_info)")
	}
	out := pageOutput(page)
	logx.Infof("mcpapp", "тест cmid=%d: start, attempt=%d, page=%d, next=%d, %s",
		in.CMID, out.AttemptID, out.Page, out.NextPage, time.Since(started).Round(time.Millisecond))
	return out, nil
}

// pageQuiz читает страницу попытки. Это GET, запись не требуется.
func pageQuiz(ctx context.Context, sess *Session, in QuizPageInput) (QuizPageOutput, error) {
	if err := validateQuizRefs(in.CMID, in.AttemptID, in.Page); err != nil {
		return QuizPageOutput{}, err
	}
	started := time.Now()
	var page *moodle.AttemptPage
	err := sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		var perr error
		page, perr = c.GetAttemptPage(ctx, in.CMID, in.AttemptID, in.Page)
		return perr
	})
	if err != nil {
		return QuizPageOutput{}, quizToolError(err, "проверьте attempt_id и номер страницы")
	}
	if err := ensureAttemptPage(page); err != nil {
		return QuizPageOutput{}, quizToolError(err, "проверьте, что попытка не завершена и страница существует")
	}
	out := pageOutput(page)
	logx.Infof("mcpapp", "тест cmid=%d: page, attempt=%d, page=%d, вопросов=%d, %s",
		in.CMID, out.AttemptID, out.Page, len(out.Questions), time.Since(started).Round(time.Millisecond))
	return out, nil
}

// ensureAttemptPage отличает страницу попытки от редиректа на review/view:
// у настоящей страницы есть и вопросы, и слоты. Пустая страница означает, что
// попытка завершена, просмотр закрыт или Moodle отдал чужую страницу.
func ensureAttemptPage(page *moodle.AttemptPage) error {
	if page == nil || len(page.Questions) == 0 || len(page.Slots) == 0 {
		return fmt.Errorf("%w: Moodle вернул не страницу попытки (возможно, она завершена или просмотр закрыт)", ErrQuizBadInput)
	}
	return nil
}

// requireQuizWrite проверяет разрешение на запись и явное подтверждение.
func requireQuizWrite(sess *Session, confirm bool) error {
	if !sess.cfg.AllowWrites {
		return toolFailure("writes_disabled", "включите MOODLE_MCP_ALLOW_WRITES=1 и перезапустите сервер", ErrWritesDisabled)
	}
	if !confirm {
		return toolFailure("confirmation_required", "повторите вызов с confirm=true, только если действие осознанно", ErrConfirmationRequired)
	}
	return nil
}

// validateQuizRefs проверяет базовые идентификаторы до запроса в Moodle.
func validateQuizRefs(cmid, attemptID int64, page int) error {
	if cmid <= 0 {
		return toolFailure("bad_input", "укажите cmid теста", nil)
	}
	if attemptID <= 0 {
		return toolFailure("bad_input", "укажите attempt_id из moodle_quiz_start", nil)
	}
	if page < 0 {
		return toolFailure("bad_input", "номер страницы не может быть отрицательным", nil)
	}
	return nil
}

// pageOutput приводит AttemptPage к ответу инструмента и добавляет needs_finish.
func pageOutput(page *moodle.AttemptPage) QuizPageOutput {
	questions := page.Questions
	if questions == nil {
		questions = []moodle.QuizQuestion{}
	}
	slots := page.Slots
	if slots == nil {
		slots = []int{}
	}
	return QuizPageOutput{
		CMID:        page.CMID,
		AttemptID:   page.AttemptID,
		Page:        page.Page,
		NextPage:    page.NextPage,
		NeedsFinish: page.NextPage < 0,
		TimeLeft:    page.TimeLeft,
		Slots:       slots,
		Questions:   questions,
	}
}

// submitQuiz перечитывает страницу, проверяет ответы и отправляет их.
func submitQuiz(ctx context.Context, sess *Session, guard *quizGuard, in QuizSubmitInput) (QuizPageOutput, error) {
	if err := validateQuizRefs(in.CMID, in.AttemptID, in.Page); err != nil {
		return QuizPageOutput{}, err
	}
	if err := requireQuizWrite(sess, in.Confirm); err != nil {
		return QuizPageOutput{}, err
	}
	release, err := guard.acquire(in.CMID, "submit")
	if err != nil {
		return QuizPageOutput{}, toolFailure("quiz_busy", "дождитесь завершения текущего действия по этому тесту", err)
	}
	defer release()

	started := time.Now()
	var submitted *moodle.AttemptPage
	err = sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		page, perr := c.GetAttemptPage(ctx, in.CMID, in.AttemptID, in.Page)
		if perr != nil {
			return perr
		}
		if uerr := ensureAttemptPage(page); uerr != nil {
			return uerr
		}
		if page.Page != in.Page {
			return fmt.Errorf("%w: запрошена страница %d, Moodle вернул %d", ErrQuizBadInput, in.Page, page.Page)
		}
		if uerr := ensureSupportedQuestions(page); uerr != nil {
			return uerr
		}
		if cerr := validateCheckSlot(page, in.CheckSlot); cerr != nil {
			return cerr
		}
		answers, aerr := mapQuizAnswers(page, in.Answers)
		if aerr != nil {
			return aerr
		}
		var serr error
		submitted, serr = c.SubmitAnswers(ctx, page, answers, in.CheckSlot)
		return serr
	})
	if err != nil {
		return QuizPageOutput{}, quizToolError(err, "проверьте слоты и типы вопросов через moodle_quiz_page")
	}
	out := pageOutput(submitted)
	logx.Infof("mcpapp", "тест cmid=%d: submit, attempt=%d, page=%d, check_slot=%d, next=%d, %s",
		in.CMID, out.AttemptID, out.Page, in.CheckSlot, out.NextPage, time.Since(started).Round(time.Millisecond))
	return out, nil
}

// ensureSupportedQuestions не даёт отправить страницу с вопросом, который
// сервер не умеет заполнять (essay, drag&drop, multianswer и т.п.).
func ensureSupportedQuestions(page *moodle.AttemptPage) error {
	for _, q := range page.Questions {
		switch q.QType {
		case "multichoice", "truefalse", "shortanswer", "numerical", "match":
		default:
			return fmt.Errorf("%w: slot=%d тип %q", ErrUnsupportedQuestion, q.Slot, q.QType)
		}
		if len(q.Choices) == 0 && q.InputName == "" && len(q.MatchOptions) == 0 {
			return fmt.Errorf("%w: slot=%d типа %q без полей ответа", ErrUnsupportedQuestion, q.Slot, q.QType)
		}
	}
	return nil
}

// validateCheckSlot проверяет, что проверяемый слот есть на странице и у его
// вопроса действительно есть кнопка проверки.
func validateCheckSlot(page *moodle.AttemptPage, slot int) error {
	if slot == 0 {
		return nil
	}
	if !slices.Contains(page.Slots, slot) {
		return fmt.Errorf("%w: check_slot %d отсутствует на странице %d", ErrQuizBadInput, slot, page.Page)
	}
	for _, q := range page.Questions {
		if q.Slot != slot {
			continue
		}
		if !q.HasSubmitButton {
			return fmt.Errorf("%w: вопрос слота %d не поддерживает проверку", ErrQuizBadInput, slot)
		}
		return nil
	}
	return fmt.Errorf("%w: вопрос слота %d не разобран", ErrQuizBadInput, slot)
}

// mapQuizAnswers приводит ответы инструмента к форме Moodle, проверяя слоты,
// варианты и строки соответствия. Неизвестный слот — ошибка, а не молчание.
func mapQuizAnswers(page *moodle.AttemptPage, in map[string]QuizAnswerInput) (map[int]moodle.QuizAnswer, error) {
	bySlot := make(map[int]moodle.QuizQuestion, len(page.Questions))
	for _, q := range page.Questions {
		bySlot[q.Slot] = q
	}
	out := make(map[int]moodle.QuizAnswer, len(in))
	for rawSlot, raw := range in {
		slot, err := strconv.Atoi(strings.TrimSpace(rawSlot))
		if err != nil || slot <= 0 {
			return nil, fmt.Errorf("%w: ключ %q не номер слота", ErrQuizBadInput, rawSlot)
		}
		if !slices.Contains(page.Slots, slot) {
			return nil, fmt.Errorf("%w: слота %d нет на странице %d", ErrQuizBadInput, slot, page.Page)
		}
		q, ok := bySlot[slot]
		if !ok {
			return nil, fmt.Errorf("%w: вопрос слота %d не разобран", ErrQuizBadInput, slot)
		}
		ans, err := mapQuizAnswer(q, raw)
		if err != nil {
			return nil, err
		}
		out[slot] = ans
	}
	return out, nil
}

// mapQuizAnswer выбирает поле ответа по типу вопроса и нормализует значения.
func mapQuizAnswer(q moodle.QuizQuestion, in QuizAnswerInput) (moodle.QuizAnswer, error) {
	switch {
	case len(q.Choices) > 0 && q.Choices[0].IsMulti:
		choices, err := multiChoiceValues(q, in.Choices)
		if err != nil {
			return moodle.QuizAnswer{}, err
		}
		return moodle.QuizAnswer{Choices: choices}, nil
	case len(q.Choices) > 0:
		choice, err := singleChoiceValue(q, in)
		if err != nil {
			return moodle.QuizAnswer{}, err
		}
		return moodle.QuizAnswer{Choice: choice}, nil
	case q.InputName != "":
		text := strings.TrimSpace(in.Text)
		if text == "" {
			return moodle.QuizAnswer{}, fmt.Errorf("%w: слот %d: нужен text", ErrQuizBadInput, q.Slot)
		}
		return moodle.QuizAnswer{Text: text}, nil
	case len(q.MatchOptions) > 0:
		matches, err := matchValues(q, in.Matches)
		if err != nil {
			return moodle.QuizAnswer{}, err
		}
		return moodle.QuizAnswer{Matches: matches}, nil
	default:
		return moodle.QuizAnswer{}, fmt.Errorf("%w: слот %d типа %q", ErrUnsupportedQuestion, q.Slot, q.QType)
	}
}

// multiChoiceValues нормализует чекбоксы: различает их name, потому что
// value у всех вариантов обычно «1».
func multiChoiceValues(q moodle.QuizQuestion, wanted []string) ([]string, error) {
	if len(wanted) == 0 {
		return nil, fmt.Errorf("%w: слот %d: нужен непустой choices", ErrQuizBadInput, q.Slot)
	}
	out := make([]string, 0, len(wanted))
	for _, want := range wanted {
		canonical, ok := resolveChoice(q, want, true)
		if !ok {
			return nil, fmt.Errorf("%w: слот %d: варианта %q нет", ErrQuizBadInput, q.Slot, want)
		}
		if !slices.Contains(out, canonical) {
			out = append(out, canonical)
		}
	}
	return out, nil
}

// singleChoiceValue нормализует radio/truefalse к value варианта.
func singleChoiceValue(q moodle.QuizQuestion, in QuizAnswerInput) (string, error) {
	want := strings.TrimSpace(in.Choice)
	if want == "" && len(in.Choices) == 1 {
		want = strings.TrimSpace(in.Choices[0])
	}
	if want == "" {
		return "", fmt.Errorf("%w: слот %d: нужен choice", ErrQuizBadInput, q.Slot)
	}
	canonical, ok := resolveChoice(q, want, false)
	if !ok {
		return "", fmt.Errorf("%w: слот %d: варианта %q нет", ErrQuizBadInput, q.Slot, want)
	}
	return canonical, nil
}

// resolveChoice ищет вариант по value, name или тексту. Для чекбоксов
// (multi=true) value у всех вариантов обычно «1», поэтому сравнивать по нему
// нельзя: уникальны только name и текст, а возвращается name.
func resolveChoice(q moodle.QuizQuestion, want string, multi bool) (string, bool) {
	for _, ch := range q.Choices {
		if multi {
			if ch.Name == "" || (want != ch.Name && want != ch.Text) {
				continue
			}
			return ch.Name, true
		}
		if ch.Value == "" || (want != ch.Value && want != ch.Name && want != ch.Text) {
			continue
		}
		return ch.Value, true
	}
	return "", false
}

// matchValues нормализует строки match: ключ может быть именем select или
// текстом строки, значение проверяется по вариантам этого select.
func matchValues(q moodle.QuizQuestion, in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%w: слот %d: нужен непустой matches", ErrQuizBadInput, q.Slot)
	}
	out := make(map[string]string, len(in))
	for key, val := range in {
		var opt *moodle.MatchOption
		for i := range q.MatchOptions {
			if q.MatchOptions[i].Name == key || q.MatchOptions[i].Question == key {
				opt = &q.MatchOptions[i]
				break
			}
		}
		if opt == nil {
			return nil, fmt.Errorf("%w: слот %d: строки %q нет", ErrQuizBadInput, q.Slot, key)
		}
		val = strings.TrimSpace(val)
		if val != "" {
			found := false
			for _, o := range opt.Choices {
				if o.Value == val || o.Text == val {
					val, found = o.Value, true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("%w: слот %d: варианта %q нет для %q", ErrQuizBadInput, q.Slot, val, key)
			}
		}
		out[opt.Name] = val
	}
	return out, nil
}

// finishQuiz завершает попытку под блокировкой cmid.
func finishQuiz(ctx context.Context, sess *Session, guard *quizGuard, in QuizFinishInput) (moodle.QuizResult, error) {
	if err := validateQuizRefs(in.CMID, in.AttemptID, 0); err != nil {
		return moodle.QuizResult{}, err
	}
	if err := requireQuizWrite(sess, in.Confirm); err != nil {
		return moodle.QuizResult{}, err
	}
	release, err := guard.acquire(in.CMID, "finish")
	if err != nil {
		return moodle.QuizResult{}, toolFailure("quiz_busy", "дождитесь завершения текущего действия по этому тесту", err)
	}
	defer release()

	started := time.Now()
	var res *moodle.QuizResult
	err = sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		var ferr error
		res, ferr = c.FinishAttempt(ctx, in.CMID, in.AttemptID)
		return ferr
	})
	if err != nil {
		return moodle.QuizResult{}, quizToolError(err, "проверьте attempt_id и что все страницы отправлены")
	}
	logx.Infof("mcpapp", "тест cmid=%d: finish, attempt=%d, review=%t, %s",
		in.CMID, in.AttemptID, res.ReviewAllowed, time.Since(started).Round(time.Millisecond))
	return *res, nil
}

// reviewQuiz читает разбор завершённой попытки.
func reviewQuiz(ctx context.Context, sess *Session, in QuizReviewInput) (moodle.ReviewPage, error) {
	if err := validateQuizRefs(in.CMID, in.AttemptID, 0); err != nil {
		return moodle.ReviewPage{}, err
	}
	started := time.Now()
	var page *moodle.ReviewPage
	err := sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		var rerr error
		page, rerr = c.GetReview(ctx, in.CMID, in.AttemptID)
		return rerr
	})
	if err != nil {
		return moodle.ReviewPage{}, quizToolError(err, "просмотр может быть закрыт настройками теста; проверьте moodle_quiz_info")
	}
	logx.Infof("mcpapp", "тест cmid=%d: review, attempt=%d, вопросов=%d, %s",
		in.CMID, in.AttemptID, len(page.Questions), time.Since(started).Round(time.Millisecond))
	return *page, nil
}

// quizImage скачивает рисунок из условия вопроса и отдаёт его image content
// или путём к файлу. URL с sesskey не попадает ни в ответ, ни в лог.
func quizImage(ctx context.Context, sess *Session, in QuizImageInput) (*mcp.CallToolResult, QuizImageOutput, error) {
	if err := validateQuizRefs(in.CMID, in.AttemptID, 0); err != nil {
		return nil, QuizImageOutput{}, err
	}
	if in.Slot <= 0 {
		return nil, QuizImageOutput{}, toolFailure("bad_input", "укажите slot вопроса", nil)
	}
	if in.Index < 0 {
		return nil, QuizImageOutput{}, toolFailure("bad_input", "index рисунка начинается с 0", nil)
	}

	started := time.Now()
	var data []byte
	var contentType string
	err := sess.WithClient(ctx, func(ctx context.Context, c *moodle.Client) error {
		var ferr error
		data, contentType, ferr = fetchQuizImage(ctx, c, in)
		return ferr
	})
	if err != nil {
		return nil, QuizImageOutput{}, quizToolError(err, "проверьте slot/index через moodle_quiz_page")
	}

	mimeType := imageMIME(contentType)
	out := QuizImageOutput{
		CMID:      in.CMID,
		AttemptID: in.AttemptID,
		Slot:      in.Slot,
		Index:     in.Index,
		MIMEType:  mimeType,
		Size:      int64(len(data)),
	}
	if path, serr := saveQuizImage(sess.cfg, in, mimeType, data); serr != nil {
		logx.Warnf("mcpapp", "тест cmid=%d: рисунок slot=%d index=%d не сохранён: %v", in.CMID, in.Slot, in.Index, serr)
	} else {
		out.Path = path
	}
	out.Inline = len(data) <= maxInlineImageBytes && inlineImageMIME(mimeType)

	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("Рисунок %d вопроса %d: %s, %d байт", in.Index, in.Slot, mimeType, len(data)),
		}},
	}
	if out.Inline {
		result.Content = append(result.Content, &mcp.ImageContent{MIMEType: mimeType, Data: data})
	}
	logx.Infof("mcpapp", "тест cmid=%d: image, attempt=%d, slot=%d, index=%d, %s, inline=%t, %s",
		in.CMID, in.AttemptID, in.Slot, in.Index, mimeType, out.Inline, time.Since(started).Round(time.Millisecond))
	return result, out, nil
}

// fetchQuizImage ищет слот по страницам попытки: в URL инструмента нет номера
// страницы, а вопрос может быть на любой из них. Цикл ограничен, чтобы
// кривой next_page не зациклил запросы.
func fetchQuizImage(ctx context.Context, c *moodle.Client, in QuizImageInput) ([]byte, string, error) {
	const maxPages = 100
	page := 0
	for i := 0; i < maxPages; i++ {
		ap, err := c.GetAttemptPage(ctx, in.CMID, in.AttemptID, page)
		if err != nil {
			return nil, "", err
		}
		for j := range ap.Questions {
			q := ap.Questions[j]
			if q.Slot != in.Slot {
				continue
			}
			if in.Index >= len(q.Images) {
				return nil, "", fmt.Errorf("%w: у слота %d рисунков %d, запрошен %d", ErrQuizBadInput, in.Slot, len(q.Images), in.Index)
			}
			src := q.Images[in.Index]
			allowed := allowedFileHosts(c)
			if cerr := checkFileURL(src, allowed); cerr != nil {
				return nil, "", cerr
			}
			res, ferr := c.FetchFileLimited(ctx, src, moodle.FileLimitDefault, allowed)
			if ferr != nil {
				return nil, "", classifyFetchError(ferr)
			}
			return res.Data, res.ContentType, nil
		}
		if ap.NextPage < 0 || ap.NextPage == page {
			break
		}
		page = ap.NextPage
	}
	return nil, "", fmt.Errorf("%w: слот %d не найден ни на одной странице попытки", ErrQuizBadInput, in.Slot)
}

// imageMIME оставляет только тип без параметров; пустое — octet-stream.
func imageMIME(contentType string) string {
	if mt, _, err := mime.ParseMediaType(contentType); err == nil && mt != "" {
		return mt
	}
	if ct := strings.TrimSpace(strings.Split(contentType, ";")[0]); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// inlineImageMIME — форматы, которые клиенты MCP показывают как картинку.
func inlineImageMIME(mimeType string) bool {
	switch strings.ToLower(mimeType) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// imageExt подбирает расширение по MIME для файла в StateDir/images.
func imageExt(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	}
	return ".img"
}

// saveQuizImage пишет рисунок в StateDir/images с правами 0600.
func saveQuizImage(cfg Config, in QuizImageInput, mimeType string, data []byte) (string, error) {
	dir := filepath.Join(cfg.StateDir, quizImagesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mcpapp: каталог images: %w", err)
	}
	name := fmt.Sprintf("quiz-%d-a%d-s%d-i%d%s", in.CMID, in.AttemptID, in.Slot, in.Index, imageExt(mimeType))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("mcpapp: сохранить %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("mcpapp: права %s: %w", path, err)
	}
	return path, nil
}

// quizToolError добавляет к ошибке код и действие для LLM.
func quizToolError(err error, action string) error {
	code := "moodle_error"
	switch {
	case errors.Is(err, ErrWritesDisabled):
		code = "writes_disabled"
	case errors.Is(err, ErrConfirmationRequired):
		code = "confirmation_required"
	case errors.Is(err, ErrQuizBusy):
		code = "quiz_busy"
	case errors.Is(err, ErrUnsupportedQuestion):
		code = "unsupported_question"
	case errors.Is(err, ErrQuizBadInput):
		code = "bad_input"
	case errors.Is(err, moodle.ErrReviewClosed):
		code = "review_closed"
	case errors.Is(err, ErrNoCredentials):
		code = "auth_required"
	case errors.Is(err, moodle.ErrUnauthorized):
		code = "auth_expired"
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(err, websess.ErrBodyTooLarge):
		code = "too_large"
	case errors.Is(err, errFileForbiddenHost):
		code = "forbidden_host"
	case errors.Is(err, errFileNotFound):
		code = "not_found"
	}
	return toolFailure(code, action, err)
}
